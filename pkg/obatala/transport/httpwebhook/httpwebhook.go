// Package httpwebhook sends customer messages to a bot as JSON POSTs. It
// is the sending half of a transport: pair it with a reply reader (for
// example the outbox package) whose Receive collects the bot's answers.
//
// The sender never retries; the runner owns retries. Response bodies are
// never echoed into errors, since they may contain backend data.
package httpwebhook

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// DefaultTimeout bounds one POST when Client is nil.
const DefaultTimeout = 15 * time.Second

// Sender POSTs one customer message per Send. Configure the exported
// fields; a Sender is safe for concurrent use.
type Sender struct {
	// URL is the bot's webhook endpoint (required).
	URL string
	// Client defaults to an http.Client with DefaultTimeout.
	Client *http.Client
	// Headers are added to every request (Content-Type is always
	// application/json).
	Headers map[string]string
	// BuildBody encodes one message for the bot (required for Send; Post
	// takes a ready body). Its errors are returned unwrapped.
	BuildBody func(ctx context.Context, s transport.Session, text string) ([]byte, error)
	// StatusHint optionally explains a non-2xx status, e.g. " (bad token)".
	StatusHint func(status int) string
	// ErrPrefix starts every error message (default "httpwebhook").
	ErrPrefix string
}

// Send builds the body for text and POSTs it. It returns the time the
// backend accepted the message.
func (w *Sender) Send(ctx context.Context, s transport.Session, text string) (time.Time, error) {
	if w.BuildBody == nil {
		return time.Time{}, fmt.Errorf("%s: BuildBody is not set", w.prefix())
	}
	body, err := w.BuildBody(ctx, s, text)
	if err != nil {
		return time.Time{}, err
	}
	return w.Post(ctx, body)
}

// Post sends a prepared JSON body. Transport errors and non-2xx statuses
// are errors (the engine classifies them as infrastructure, not
// regressions).
func (w *Sender) Post(ctx context.Context, body []byte) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: build webhook request: %w", w.prefix(), err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: webhook send: %w", w.prefix(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		hint := ""
		if w.StatusHint != nil {
			hint = w.StatusHint(resp.StatusCode)
		}
		return time.Time{}, fmt.Errorf("%s: webhook rejected: HTTP %d%s", w.prefix(), resp.StatusCode, hint)
	}
	return time.Now(), nil
}

func (w *Sender) prefix() string {
	if w.ErrPrefix == "" {
		return "httpwebhook"
	}
	return w.ErrPrefix
}
