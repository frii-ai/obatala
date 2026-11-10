// Package webhookoutbox is the language-neutral transport for bots that
// reply asynchronously, registered as "webhook-outbox". Each customer
// message is POSTed to the bot (same body as the "http" transport; any
// 2xx accepts it), and the bot XADDs its replies to a Redis stream named
// <stream_prefix><identity>:
//
//	XADD obatala:outbox:<identity> * text "bot reply" [id "<dedupe id>"] [at "<RFC 3339>"]
//
// Replies are collected until the settle window passes with no new entry
// (reply_timeout / settle_window in obatala.yaml). Entries without a
// "text" field count as activity but are not shown; a repeated "id" is
// dropped. See spec/http-adapter.md.
//
// obatala.yaml:
//
//	transport:
//	  name: webhook-outbox
//	  settings:
//	    url: http://localhost:8080/obatala/messages
//	    redis_url: redis://localhost:6379/0       # or ${...} via redis_url_env
//	    stream_prefix: "obatala:outbox:"            # default
//	    headers:
//	      Authorization: "Bearer ${BOT_TEST_TOKEN}"
package webhookoutbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/internal/settings"
	"github.com/frii-ai/obatala/pkg/obatala/outbox"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpsync"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpwebhook"
	"github.com/redis/go-redis/v9"
)

// Name is the transport registry name.
const Name = "webhook-outbox"

// DefaultStreamPrefix prefixes the identity to form each session's stream.
const DefaultStreamPrefix = "obatala:outbox:"

func init() {
	_ = transport.Register(Name, New)
}

// Transport composes an httpwebhook.Sender and an outbox.Reader.
type Transport struct {
	sender *httpwebhook.Sender
	reader *outbox.Reader
}

// New builds the transport from transport.settings.
func New(m map[string]any) (transport.Transport, error) {
	url, err := settings.Required(m, Name, "url")
	if err != nil {
		return nil, err
	}
	redisURL := settings.String(m, "redis_url")
	if env := settings.String(m, "redis_url_env"); env != "" {
		redisURL = os.Getenv(env)
		if redisURL == "" {
			return nil, fmt.Errorf("%s: env %s (redis_url_env) is not set", Name, env)
		}
	}
	if redisURL == "" {
		return nil, fmt.Errorf("%s: settings.redis_url (or redis_url_env) is required", Name)
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("%s: redis_url: %w", Name, err)
	}
	timeout, err := settings.Duration(m, Name, "timeout", httpwebhook.DefaultTimeout)
	if err != nil {
		return nil, err
	}
	headers, err := settings.Headers(m, Name, "headers")
	if err != nil {
		return nil, err
	}
	prefix := settings.String(m, "stream_prefix")
	if prefix == "" {
		prefix = DefaultStreamPrefix
	}
	return &Transport{
		sender: &httpwebhook.Sender{
			URL:       url,
			Client:    &http.Client{Timeout: timeout},
			Headers:   headers,
			BuildBody: buildBody,
			ErrPrefix: Name,
		},
		reader: &outbox.Reader{
			Client:    redis.NewClient(opts),
			StreamKey: func(s transport.Session) string { return prefix + s.Identity },
			Decode:    decode,
			ErrPrefix: Name,
		},
	}, nil
}

func buildBody(_ context.Context, s transport.Session, text string) ([]byte, error) {
	b, err := json.Marshal(httpsync.Request{
		SessionID: s.ID,
		Identity:  s.Identity,
		Fixture:   s.FixtureKey,
		Meta:      s.Meta,
		Text:      text,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", Name, err)
	}
	return b, nil
}

// decode maps a stream entry with fields text, id and at.
func decode(m redis.XMessage) (outbox.Decoded, bool) {
	field := func(k string) string {
		b, _ := outbox.JSONField(m, k)
		return string(b)
	}
	text := field("text")
	at := outbox.StreamTime(m.ID)
	if s := field("at"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			at = t
		}
	}
	raw, _ := json.Marshal(m.Values)
	return outbox.Decoded{
		DedupeID: field("id"),
		Message:  transport.Message{Text: text, At: at, Raw: raw},
		Emit:     text != "",
	}, true
}

func (t *Transport) Open(ctx context.Context, s transport.Session) error {
	if s.Identity == "" {
		return fmt.Errorf("%s: session %q has no identity", Name, s.ID)
	}
	return t.reader.Open(ctx, s)
}

func (t *Transport) Send(ctx context.Context, s transport.Session, text string) (time.Time, error) {
	return t.sender.Send(ctx, s, text)
}

func (t *Transport) Receive(ctx context.Context, s transport.Session, o transport.ReceiveOpts) ([]transport.Message, error) {
	return t.reader.Receive(ctx, s, o)
}

func (t *Transport) Close(ctx context.Context, s transport.Session) error {
	return t.reader.Close(ctx, s)
}
