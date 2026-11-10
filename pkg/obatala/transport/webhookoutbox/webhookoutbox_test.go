package webhookoutbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpsync"
	"github.com/frii-ai/obatala/pkg/obatala/transport/webhookoutbox"
)

// asyncBot accepts the POST, then XADDs two replies (one duplicated by a
// retried XADD) and a typing marker to the identity's stream.
func asyncBot(t *testing.T, mr *miniredis.Miniredis) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req httpsync.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
		key := "obatala:outbox:" + req.Identity
		go func() {
			time.Sleep(20 * time.Millisecond)
			_, _ = mr.XAdd(key, "*", []string{"typing", "1"})
			_, _ = mr.XAdd(key, "*", []string{"id", "r1", "text", "echo: " + req.Text})
			_, _ = mr.XAdd(key, "*", []string{"id", "r1", "text", "echo: " + req.Text})
			_, _ = mr.XAdd(key, "*", []string{"id", "r2", "text", "anything else?", "at", "2026-01-02T15:04:05Z"})
		}()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAsyncRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	srv := asyncBot(t, mr)
	tr, err := webhookoutbox.New(map[string]any{"url": srv.URL, "redis_url": "redis://" + mr.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := transport.Session{ID: "run-1", Identity: "obatala-abc"}
	if err := tr.Open(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Send(ctx, s, "hi"); err != nil {
		t.Fatal(err)
	}
	msgs, err := tr.Receive(ctx, s, transport.ReceiveOpts{Timeout: 2 * time.Second, SettleWindow: 100 * time.Millisecond})
	if err != nil || len(msgs) != 2 || msgs[0].Text != "echo: hi" || msgs[1].Text != "anything else?" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if !msgs[1].At.Equal(time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)) {
		t.Fatalf("at = %v", msgs[1].At)
	}
	if err := tr.Close(ctx, s); err != nil {
		t.Fatal(err)
	}
	if mr.Exists("obatala:outbox:obatala-abc") {
		t.Fatal("stream should be deleted on close")
	}
}

func TestSettings(t *testing.T) {
	if _, err := webhookoutbox.New(map[string]any{"url": "http://x"}); err == nil {
		t.Fatal("missing redis_url should error")
	}
	t.Setenv("OBATALA_TEST_REDIS", "redis://127.0.0.1:6379/2")
	if _, err := webhookoutbox.New(map[string]any{"url": "http://x", "redis_url_env": "OBATALA_TEST_REDIS"}); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Get("webhook-outbox"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRequiresIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	tr, _ := webhookoutbox.New(map[string]any{"url": "http://x", "redis_url": "redis://" + mr.Addr()})
	if err := tr.Open(context.Background(), transport.Session{ID: "a"}); err == nil {
		t.Fatal("expected error")
	}
}
