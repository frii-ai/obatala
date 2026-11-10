package outbox_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/frii-ai/obatala/pkg/obatala/outbox"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/redis/go-redis/v9"
)

var fastOpts = transport.ReceiveOpts{Timeout: 3 * time.Second, SettleWindow: 50 * time.Millisecond}

// entry is the test stream format: {"id": "...", "text": "..."} in a
// "json" field; entries without text are activity only.
type entry struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func decode(m redis.XMessage) (outbox.Decoded, bool) {
	b, ok := outbox.JSONField(m, "json")
	if !ok {
		return outbox.Decoded{}, false
	}
	var e entry
	if err := json.Unmarshal(b, &e); err != nil {
		return outbox.Decoded{}, false
	}
	return outbox.Decoded{
		DedupeID: e.ID,
		Message:  transport.Message{Text: e.Text, At: outbox.StreamTime(m.ID)},
		Emit:     e.Text != "",
	}, true
}

func newReader(t *testing.T) (*outbox.Reader, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return &outbox.Reader{
		Client:    redis.NewClient(&redis.Options{Addr: mr.Addr()}),
		StreamKey: func(s transport.Session) string { return "test:outbox:" + s.Identity },
		Decode:    decode,
	}, mr
}

func open(t *testing.T, r *outbox.Reader, id string) transport.Session {
	t.Helper()
	s := transport.Session{ID: id, Identity: "user-" + id}
	if err := r.Open(context.Background(), s); err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestOpenClearsLeftovers(t *testing.T) {
	r, mr := newReader(t)
	_, _ = mr.XAdd("test:outbox:user-a", "*", []string{"json", `{"id":"old","text":"stale"}`})
	s := open(t, r, "a")
	msgs, err := r.Receive(context.Background(), s, transport.ReceiveOpts{Timeout: 50 * time.Millisecond, SettleWindow: 10 * time.Millisecond})
	if err != nil || len(msgs) != 0 {
		t.Fatalf("msgs=%+v err=%v, want leftovers cleared", msgs, err)
	}
}

func TestKeepOnOpenKeepsLeftovers(t *testing.T) {
	r, mr := newReader(t)
	r.KeepOnOpen = true
	_, _ = mr.XAdd("test:outbox:user-a", "*", []string{"json", `{"id":"old","text":"kept"}`})
	s := open(t, r, "a")
	msgs, err := r.Receive(context.Background(), s, fastOpts)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "kept" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
}

func TestReceiveEmptyWaitsOutTimeout(t *testing.T) {
	r, _ := newReader(t)
	s := open(t, r, "empty")
	start := time.Now()
	msgs, err := r.Receive(context.Background(), s, transport.ReceiveOpts{Timeout: 120 * time.Millisecond, SettleWindow: 20 * time.Millisecond})
	if err != nil || len(msgs) != 0 {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("should wait out the reply timeout")
	}
}

func TestReceiveSettles(t *testing.T) {
	r, mr := newReader(t)
	s := open(t, r, "settle")
	_, _ = mr.XAdd("test:outbox:user-settle", "*", []string{"json", `{"id":"a","text":"one"}`})
	start := time.Now()
	msgs, err := r.Receive(context.Background(), s, fastOpts)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "one" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("should wait for the settle window")
	}
	if msgs[0].At.IsZero() {
		t.Fatal("expected StreamTime timestamp")
	}
}

func TestReceiveDedupesSkipsMalformedAndActivityOnly(t *testing.T) {
	r, mr := newReader(t)
	s := open(t, r, "mix")
	key := "test:outbox:user-mix"
	_, _ = mr.XAdd(key, "*", []string{"json", `not json`})
	_, _ = mr.XAdd(key, "*", []string{"other", "field"})
	_, _ = mr.XAdd(key, "*", []string{"json", `{"id":"same","text":"hi"}`})
	_, _ = mr.XAdd(key, "*", []string{"json", `{"id":"same","text":"hi again"}`})
	_, _ = mr.XAdd(key, "*", []string{"json", `{"id":"typing"}`})
	_, _ = mr.XAdd(key, "*", []string{"json", `{"text":"no id uses stream id"}`})
	msgs, err := r.Receive(context.Background(), s, fastOpts)
	if err != nil || len(msgs) != 2 || msgs[0].Text != "hi" || msgs[1].Text != "no id uses stream id" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
}

func TestReceiveCursorAdvancesAcrossTurns(t *testing.T) {
	r, mr := newReader(t)
	s := open(t, r, "turns")
	key := "test:outbox:user-turns"
	_, _ = mr.XAdd(key, "*", []string{"json", `{"id":"1","text":"first"}`})
	if msgs, _ := r.Receive(context.Background(), s, fastOpts); len(msgs) != 1 {
		t.Fatalf("turn 1 msgs=%+v", msgs)
	}
	_, _ = mr.XAdd(key, "*", []string{"json", `{"id":"2","text":"second"}`})
	msgs, err := r.Receive(context.Background(), s, fastOpts)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "second" {
		t.Fatalf("turn 2 msgs=%+v err=%v", msgs, err)
	}
}

func TestReceiveHonorsCancel(t *testing.T) {
	r, _ := newReader(t)
	s := open(t, r, "cancel")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := r.Receive(ctx, s, transport.ReceiveOpts{Timeout: 10 * time.Second, SettleWindow: time.Second}); err == nil {
		t.Fatal("expected context error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not abort promptly")
	}
}

func TestCloseDeletesAndIsIdempotent(t *testing.T) {
	r, mr := newReader(t)
	s := open(t, r, "close")
	_, _ = mr.XAdd("test:outbox:user-close", "*", []string{"json", `{"id":"x","text":"x"}`})
	ctx := context.Background()
	if err := r.Close(ctx, s); err != nil {
		t.Fatal(err)
	}
	if mr.Exists("test:outbox:user-close") {
		t.Fatal("stream should be deleted")
	}
	if err := r.Close(ctx, s); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := r.Receive(ctx, s, fastOpts); err == nil {
		t.Fatal("receive after close should error")
	}
}

func TestOpenRedisDownIsError(t *testing.T) {
	r, mr := newReader(t)
	mr.Close()
	r.ErrPrefix = "mybot"
	err := r.Open(context.Background(), transport.Session{ID: "x", Identity: "x"})
	if err == nil || err.Error()[:len("mybot: redis unreachable")] != "mybot: redis unreachable" {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamTime(t *testing.T) {
	if got := outbox.StreamTime("1700000000123-4"); !got.Equal(time.UnixMilli(1700000000123)) {
		t.Fatalf("got %v", got)
	}
	for _, id := range []string{"", "abc-1", "0-1"} {
		if !outbox.StreamTime(id).IsZero() {
			t.Fatalf("%q should give zero time", id)
		}
	}
}
