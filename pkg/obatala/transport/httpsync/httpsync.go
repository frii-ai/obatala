// Package httpsync is the language-neutral request/response transport,
// registered as "http". Each customer message is one POST to the bot's
// test endpoint, and the bot answers in the response body. A bot written
// in any language can be tested this way without writing Go:
//
//	POST <url>
//	{"session_id": "...", "identity": "...", "fixture": "...",
//	 "meta": {"k": "v"}, "text": "customer message"}
//
//	200 OK
//	{"replies": [{"text": "bot reply", "at": "2026-01-02T15:04:05Z"}]}
//
// "at" is optional (the receive time is used). The bot keys conversation
// state by session_id. See spec/http-adapter.md for the full protocol.
//
// obatala.yaml:
//
//	transport:
//	  name: http
//	  settings:
//	    url: http://localhost:8080/obatala/messages
//	    timeout: 120s                       # per request (default 120s)
//	    headers:
//	      Authorization: "Bearer ${BOT_TEST_TOKEN}"   # expanded from env
package httpsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/internal/settings"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Name is the transport registry name.
const Name = "http"

// DefaultTimeout bounds one request when settings.timeout is unset. It
// covers the bot's full reply generation, so it is generous.
const DefaultTimeout = 120 * time.Second

// maxBody caps how much of a response is read (replies are text).
const maxBody = 4 << 20

func init() {
	_ = transport.Register(Name, New)
}

// Request is the JSON body POSTed for every customer message.
type Request struct {
	SessionID string            `json:"session_id"`
	Identity  string            `json:"identity"`
	Fixture   string            `json:"fixture,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Text      string            `json:"text"`
}

// Response is the JSON body the bot returns.
type Response struct {
	Replies []Reply `json:"replies"`
}

// Reply is one bot message.
type Reply struct {
	Text string    `json:"text"`
	At   time.Time `json:"at,omitempty"`
}

// Transport implements transport.Transport over one HTTP endpoint.
type Transport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	pending map[string][]transport.Message // by Session.ID
}

// New builds the transport from transport.settings.
func New(m map[string]any) (transport.Transport, error) {
	url, err := settings.Required(m, Name, "url")
	if err != nil {
		return nil, err
	}
	timeout, err := settings.Duration(m, Name, "timeout", DefaultTimeout)
	if err != nil {
		return nil, err
	}
	headers, err := settings.Headers(m, Name, "headers")
	if err != nil {
		return nil, err
	}
	return &Transport{
		url:     url,
		headers: headers,
		client:  &http.Client{Timeout: timeout},
		pending: map[string][]transport.Message{},
	}, nil
}

func (t *Transport) Open(_ context.Context, s transport.Session) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[s.ID] = nil
	return nil
}

// Send POSTs the message and buffers the bot's replies for Receive. The
// returned time is taken before the request, so turn latency includes the
// bot's generation time.
func (t *Transport) Send(ctx context.Context, s transport.Session, text string) (time.Time, error) {
	if !t.isOpen(s) {
		return time.Time{}, fmt.Errorf("http: session %q is not open", s.ID)
	}
	body, err := json.Marshal(Request{
		SessionID: s.ID,
		Identity:  s.Identity,
		Fixture:   s.FixtureKey,
		Meta:      s.Meta,
		Text:      text,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("http: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return time.Time{}, fmt.Errorf("http: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	sentAt := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("http: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	received := time.Now()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The body is not echoed: it may contain backend data.
		return time.Time{}, fmt.Errorf("http: bot rejected message: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return time.Time{}, fmt.Errorf("http: read response: %w", err)
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return time.Time{}, fmt.Errorf("http: response is not {\"replies\": [...]} JSON: %w", err)
	}
	msgs := make([]transport.Message, 0, len(out.Replies))
	for i, r := range out.Replies {
		if r.Text == "" {
			continue
		}
		at := r.At
		if at.IsZero() {
			at = received
		}
		rawReply, _ := json.Marshal(out.Replies[i])
		msgs = append(msgs, transport.Message{Text: r.Text, At: at, Raw: rawReply})
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pending[s.ID]; !ok {
		return time.Time{}, errors.New("http: session closed during send")
	}
	t.pending[s.ID] = append(t.pending[s.ID], msgs...)
	return sentAt, nil
}

// Receive returns the replies from the last Send. Replies arrived with
// the response, so there is nothing to wait for.
func (t *Transport) Receive(_ context.Context, s transport.Session, _ transport.ReceiveOpts) ([]transport.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	msgs, ok := t.pending[s.ID]
	if !ok {
		return nil, fmt.Errorf("http: session %q is not open", s.ID)
	}
	t.pending[s.ID] = nil
	return msgs, nil
}

// Close drops buffered replies. Idempotent. Server-side cleanup is the
// manager's job (http-control deletes identities on teardown).
func (t *Transport) Close(_ context.Context, s transport.Session) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, s.ID)
	return nil
}

func (t *Transport) isOpen(s transport.Session) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.pending[s.ID]
	return ok
}
