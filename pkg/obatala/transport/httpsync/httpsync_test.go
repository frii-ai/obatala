package httpsync_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpsync"
)

func newTransport(t *testing.T, h http.HandlerFunc, extra map[string]any) transport.Transport {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m := map[string]any{"url": srv.URL}
	for k, v := range extra {
		m[k] = v
	}
	tr, err := httpsync.New(m)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestRoundTrip(t *testing.T) {
	t.Setenv("BOT_TEST_TOKEN", "s3cret")
	var got httpsync.Request
	var auth string
	tr := newTransport(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"replies":[{"text":"one"},{"text":""},{"text":"two","at":"2026-01-02T15:04:05Z"}]}`))
	}, map[string]any{"headers": map[string]any{"Authorization": "Bearer ${BOT_TEST_TOKEN}"}})

	ctx := context.Background()
	s := transport.Session{ID: "run-1", Identity: "obatala-x", FixtureKey: "demo", Meta: map[string]string{"k": "v"}}
	if err := tr.Open(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Send(ctx, s, "hello"); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "run-1" || got.Identity != "obatala-x" || got.Fixture != "demo" || got.Meta["k"] != "v" || got.Text != "hello" {
		t.Fatalf("request = %+v", got)
	}
	if auth != "Bearer s3cret" {
		t.Fatalf("auth = %q", auth)
	}
	msgs, err := tr.Receive(ctx, s, transport.ReceiveOpts{})
	if err != nil || len(msgs) != 2 || msgs[0].Text != "one" || msgs[1].Text != "two" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if !msgs[1].At.Equal(time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)) || msgs[0].At.IsZero() {
		t.Fatalf("timestamps = %v, %v", msgs[0].At, msgs[1].At)
	}
	if again, _ := tr.Receive(ctx, s, transport.ReceiveOpts{}); len(again) != 0 {
		t.Fatalf("replies returned twice: %+v", again)
	}
}

func TestErrorsDoNotLeakBody(t *testing.T) {
	tr := newTransport(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("customer secret"))
	}, nil)
	s := transport.Session{ID: "a"}
	_ = tr.Open(context.Background(), s)
	_, err := tr.Send(context.Background(), s, "x")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidResponseIsError(t *testing.T) {
	tr := newTransport(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("plain text"))
	}, nil)
	s := transport.Session{ID: "a"}
	_ = tr.Open(context.Background(), s)
	if _, err := tr.Send(context.Background(), s, "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnopenedSession(t *testing.T) {
	tr := newTransport(t, func(http.ResponseWriter, *http.Request) {}, nil)
	s := transport.Session{ID: "nope"}
	if _, err := tr.Send(context.Background(), s, "x"); err == nil {
		t.Fatal("send should error")
	}
	if _, err := tr.Receive(context.Background(), s, transport.ReceiveOpts{}); err == nil {
		t.Fatal("receive should error")
	}
}

func TestSettingsValidation(t *testing.T) {
	if _, err := httpsync.New(map[string]any{}); err == nil {
		t.Fatal("missing url should error")
	}
	if _, err := httpsync.New(map[string]any{"url": "http://x", "timeout": "soon"}); err == nil {
		t.Fatal("bad timeout should error")
	}
	if _, err := httpsync.New(map[string]any{"url": "http://x", "headers": map[string]any{"A": "${OBATALA_TEST_UNSET_VAR}"}}); err == nil {
		t.Fatal("unset env in header should error")
	}
}

func TestRegistered(t *testing.T) {
	if _, err := transport.Get("http"); err != nil {
		t.Fatal(err)
	}
}
