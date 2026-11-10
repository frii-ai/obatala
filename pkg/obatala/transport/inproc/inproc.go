// Package inproc is a transport for bots that live in the same Go program
// as the tests: Send calls the bot's handler directly and Receive returns
// its replies. No HTTP, no queue.
//
// A synchronous Handler returns its replies from the call; Receive then
// returns them immediately. An AsyncHandler may reply later (from other
// goroutines); Receive then waits for the first reply up to the reply
// timeout and keeps collecting until the settle window passes quietly,
// like a networked transport would.
//
// Register it under a name your obatala.yaml uses:
//
//	transport.Register("mybot", func(map[string]any) (transport.Transport, error) {
//		return inproc.New(myBot.Handle), nil
//	})
package inproc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Defaults used by async transports when ReceiveOpts leaves a value zero.
const (
	DefaultReplyTimeout = 90 * time.Second
	DefaultSettleWindow = 4 * time.Second
)

// Handler handles one customer message and returns the bot's replies.
// An error is an infrastructure failure (the scenario errors, it does not
// fail).
type Handler func(ctx context.Context, s transport.Session, text string) ([]string, error)

// AsyncHandler handles one customer message; the bot calls reply (any
// number of times, from any goroutine) whenever it answers. Replies after
// the session closes are dropped.
type AsyncHandler func(ctx context.Context, s transport.Session, text string, reply func(text string)) error

// Option configures a Transport.
type Option func(*Transport)

// OnOpen runs when a session opens (e.g. to start a conversation).
func OnOpen(f func(ctx context.Context, s transport.Session) error) Option {
	return func(t *Transport) { t.onOpen = f }
}

// OnClose runs when a session closes (e.g. to drop conversation state).
func OnClose(f func(ctx context.Context, s transport.Session) error) Option {
	return func(t *Transport) { t.onClose = f }
}

// Transport implements transport.Transport over an in-process handler.
// Sessions are keyed by Session.ID; it is safe for concurrent sessions.
type Transport struct {
	handle      Handler
	handleAsync AsyncHandler
	onOpen      func(context.Context, transport.Session) error
	onClose     func(context.Context, transport.Session) error

	mu       sync.Mutex
	sessions map[string]*session
}

// session buffers replies not yet returned by Receive.
type session struct {
	mu     sync.Mutex
	queued []transport.Message
	notify chan struct{} // capacity 1: "something was queued"
	closed bool
}

// New returns a transport over a synchronous handler.
func New(h Handler, opts ...Option) *Transport {
	return build(&Transport{handle: h}, opts)
}

// NewAsync returns a transport over a handler that replies via callback.
func NewAsync(h AsyncHandler, opts ...Option) *Transport {
	return build(&Transport{handleAsync: h}, opts)
}

func build(t *Transport, opts []Option) *Transport {
	t.sessions = map[string]*session{}
	for _, o := range opts {
		o(t)
	}
	return t
}

func (t *Transport) Open(ctx context.Context, s transport.Session) error {
	if t.onOpen != nil {
		if err := t.onOpen(ctx, s); err != nil {
			return fmt.Errorf("inproc: open session %q: %w", s.ID, err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[s.ID] = &session{notify: make(chan struct{}, 1)}
	return nil
}

func (t *Transport) Send(ctx context.Context, s transport.Session, text string) (time.Time, error) {
	sess, err := t.session(s)
	if err != nil {
		return time.Time{}, err
	}
	sentAt := time.Now()
	if t.handleAsync != nil {
		if err := t.handleAsync(ctx, s, text, sess.push); err != nil {
			return time.Time{}, fmt.Errorf("inproc: handler: %w", err)
		}
		return sentAt, nil
	}
	replies, err := t.handle(ctx, s, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("inproc: handler: %w", err)
	}
	for _, r := range replies {
		sess.push(r)
	}
	return sentAt, nil
}

func (t *Transport) Receive(ctx context.Context, s transport.Session, o transport.ReceiveOpts) ([]transport.Message, error) {
	sess, err := t.session(s)
	if err != nil {
		return nil, err
	}
	if t.handleAsync == nil {
		// Synchronous replies are already queued: no waiting.
		return sess.drain(), nil
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultReplyTimeout
	}
	settle := o.SettleWindow
	if settle <= 0 {
		settle = DefaultSettleWindow
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	// The settle timer starts at the first reply and restarts on each
	// later one; until then only the reply timeout applies.
	var quiet *time.Timer
	var quietC <-chan time.Time
	arm := func() {
		if quiet != nil {
			quiet.Stop()
		}
		quiet = time.NewTimer(settle)
		quietC = quiet.C
	}
	defer func() {
		if quiet != nil {
			quiet.Stop()
		}
	}()

	out := sess.drain()
	if len(out) > 0 {
		arm()
	}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("inproc: receive: %w", ctx.Err())
		case <-deadline.C:
			return append(out, sess.drain()...), nil // reply timeout: turn complete
		case <-quietC:
			return append(out, sess.drain()...), nil // settle window passed quietly
		case <-sess.notify:
			if more := sess.drain(); len(more) > 0 {
				out = append(out, more...)
				arm()
			}
		}
	}
}

// Close drops the session; later async replies are discarded. Idempotent.
func (t *Transport) Close(ctx context.Context, s transport.Session) error {
	t.mu.Lock()
	sess := t.sessions[s.ID]
	delete(t.sessions, s.ID)
	t.mu.Unlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	if t.onClose != nil {
		if err := t.onClose(ctx, s); err != nil {
			return fmt.Errorf("inproc: close session %q: %w", s.ID, err)
		}
	}
	return nil
}

func (t *Transport) session(s transport.Session) (*session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sess, ok := t.sessions[s.ID]
	if !ok {
		return nil, fmt.Errorf("inproc: session %q is not open", s.ID)
	}
	return sess, nil
}

func (s *session) push(text string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queued = append(s.queued, transport.Message{Text: text, At: time.Now()})
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *session) drain() []transport.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queued
	s.queued = nil
	return out
}
