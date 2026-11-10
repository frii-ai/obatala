// Package outbox reads a bot's replies from a per-session Redis stream.
// It is an optional building block for transports whose backend mirrors
// outbound messages into a stream (XADD) instead of answering the request
// that triggered them: pair it with a sender (for example
// transport/httpwebhook) to make a full transport.Transport.
//
// Turn completion: Receive blocks until the first entry arrives (up to the
// reply timeout), then keeps reading until the settle window passes with
// no new entry. Entries are deduplicated per session, so a backend that
// retries an XADD does not produce a duplicate reply.
//
// This is the only obatala package that imports go-redis; transports that
// do not use it never pull it in.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/redis/go-redis/v9"
)

// Defaults used when neither ReceiveOpts nor the Reader set a value.
const (
	DefaultReplyTimeout = 90 * time.Second
	DefaultSettleWindow = 4 * time.Second
)

// Decoded is one stream entry mapped by a Decoder.
type Decoded struct {
	// DedupeID identifies the logical message across retried XADDs.
	// Empty means the stream entry id.
	DedupeID string
	// Message is the reply to hand to the engine.
	Message transport.Message
	// Emit is false for entries that count as bot activity (they extend
	// the settle window and are deduplicated) but carry nothing to show.
	Emit bool
}

// Decoder maps a raw stream entry. ok=false skips a malformed entry: the
// cursor still advances past it, but it is not deduplicated or emitted.
type Decoder func(m redis.XMessage) (d Decoded, ok bool)

// Reader is a per-session outbox reader. Configure the exported fields,
// then use Open/Receive/Close from a transport's methods of the same name.
// Safe for concurrent use across sessions.
type Reader struct {
	// Client is the Redis connection (required).
	Client *redis.Client
	// StreamKey returns the stream a session's replies land in (required).
	StreamKey func(s transport.Session) string
	// Decode maps entries (required). JSONField and StreamTime help.
	Decode Decoder
	// ReplyTimeout and SettleWindow apply when ReceiveOpts leaves them
	// zero (defaults DefaultReplyTimeout / DefaultSettleWindow).
	ReplyTimeout time.Duration
	SettleWindow time.Duration
	// KeepOnOpen skips deleting the stream on Open (by default leftovers
	// from an aborted run are removed). KeepOnClose skips deleting it on
	// Close.
	KeepOnOpen  bool
	KeepOnClose bool
	// ErrPrefix starts every error message (default "outbox").
	ErrPrefix string

	mu      sync.Mutex
	cursors map[string]*cursor
}

// cursor is one session's read position and dedupe set.
type cursor struct {
	key    string
	lastID string // exclusive XREAD cursor; starts at 0-0
	seen   map[string]struct{}
}

func (r *Reader) prefix() string {
	if r.ErrPrefix == "" {
		return "outbox"
	}
	return r.ErrPrefix
}

// Open checks Redis is reachable, clears the session's stream (unless
// KeepOnOpen), and starts a cursor at the beginning of the stream.
func (r *Reader) Open(ctx context.Context, s transport.Session) error {
	if err := r.Client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%s: redis unreachable: %w", r.prefix(), err)
	}
	key := r.StreamKey(s)
	if !r.KeepOnOpen {
		if err := r.Client.Del(ctx, key).Err(); err != nil {
			return fmt.Errorf("%s: reset outbox key: %w", r.prefix(), err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cursors == nil {
		r.cursors = map[string]*cursor{}
	}
	r.cursors[s.ID] = &cursor{key: key, lastID: "0-0", seen: map[string]struct{}{}}
	return nil
}

// Receive collects one turn's replies. A reply timeout completes the turn
// (possibly with no replies); Redis failures and cancellation are errors.
func (r *Reader) Receive(ctx context.Context, s transport.Session, o transport.ReceiveOpts) ([]transport.Message, error) {
	cur, err := r.cursor(s)
	if err != nil {
		return nil, err
	}
	timeout := firstPositive(o.Timeout, r.ReplyTimeout, DefaultReplyTimeout)
	settle := firstPositive(o.SettleWindow, r.SettleWindow, DefaultSettleWindow)
	deadline := time.Now().Add(timeout)

	var out []transport.Message
	gotAny := false
	for {
		if time.Now().After(deadline) {
			return out, nil // reply timeout hit: turn complete
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: outbox read: %w", r.prefix(), err)
		}
		block := settle
		if !gotAny {
			block = time.Until(deadline)
		}
		if block < time.Millisecond {
			block = time.Millisecond
		}
		// XREAD BLOCK does not observe context cancellation on its own, so
		// run it detached and abandon it on cancel (it ends within `block`;
		// the buffered channel lets the goroutine exit without leaking).
		type xreadResult struct {
			streams []redis.XStream
			err     error
		}
		ch := make(chan xreadResult, 1)
		streams := []string{cur.key, cur.lastID}
		go func() {
			res, err := r.Client.XRead(context.WithoutCancel(ctx), &redis.XReadArgs{
				Block:   block,
				Streams: streams,
			}).Result()
			ch <- xreadResult{res, err}
		}()
		var result xreadResult
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%s: outbox read: %w", r.prefix(), ctx.Err())
		case result = <-ch:
		}
		if errors.Is(result.err, redis.Nil) {
			return out, nil // settle window expired (or first wait timed out)
		}
		if result.err != nil {
			return nil, fmt.Errorf("%s: outbox read: %w", r.prefix(), result.err)
		}
		for _, stream := range result.streams {
			for _, m := range stream.Messages {
				cur.lastID = m.ID
				gotAny = true
				d, ok := r.Decode(m)
				if !ok {
					continue
				}
				id := d.DedupeID
				if id == "" {
					id = m.ID
				}
				if _, dup := cur.seen[id]; dup {
					continue
				}
				cur.seen[id] = struct{}{}
				if d.Emit {
					out = append(out, d.Message)
				}
			}
		}
	}
}

// Close deletes the session's stream (unless KeepOnClose) and drops its
// cursor. Idempotent: closing an unopened session is a no-op.
func (r *Reader) Close(ctx context.Context, s transport.Session) error {
	r.mu.Lock()
	cur := r.cursors[s.ID]
	delete(r.cursors, s.ID)
	r.mu.Unlock()
	if cur == nil || r.KeepOnClose {
		return nil
	}
	if err := r.Client.Del(ctx, cur.key).Err(); err != nil {
		return fmt.Errorf("%s: delete outbox key: %w", r.prefix(), err)
	}
	return nil
}

func (r *Reader) cursor(s transport.Session) (*cursor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.cursors[s.ID]
	if !ok {
		return nil, fmt.Errorf("%s: session %q is not open", r.prefix(), s.ID)
	}
	return cur, nil
}

// JSONField returns a stream entry field as bytes (string or []byte
// values), for decoders whose entries carry a JSON document in one field.
func JSONField(m redis.XMessage, field string) ([]byte, bool) {
	switch v := m.Values[field].(type) {
	case string:
		return []byte(v), true
	case []byte:
		return v, true
	default:
		return nil, false
	}
}

// StreamTime returns the millisecond timestamp in a Redis stream id
// ("1700000000000-0"), or the zero time when the id has none.
func StreamTime(id string) time.Time {
	var ms int64
	for _, c := range id {
		if c == '-' {
			break
		}
		if c < '0' || c > '9' {
			return time.Time{}
		}
		ms = ms*10 + int64(c-'0')
	}
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func firstPositive(ds ...time.Duration) time.Duration {
	for _, d := range ds {
		if d > 0 {
			return d
		}
	}
	return 0
}
