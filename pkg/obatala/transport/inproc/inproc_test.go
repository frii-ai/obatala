package inproc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/inproc"
)

var _ transport.Transport = (*inproc.Transport)(nil)

func echo(_ context.Context, s transport.Session, text string) ([]string, error) {
	return []string{s.ID + ": " + text}, nil
}

func TestSyncSendReceive(t *testing.T) {
	tr := inproc.New(echo)
	ctx := context.Background()
	s := transport.Session{ID: "a"}
	if err := tr.Open(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Send(ctx, s, "hi"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	msgs, err := tr.Receive(ctx, s, transport.ReceiveOpts{Timeout: time.Second, SettleWindow: time.Second})
	if err != nil || len(msgs) != 1 || msgs[0].Text != "a: hi" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("sync receive should not wait for the settle window")
	}
	if msgs, _ := tr.Receive(ctx, s, transport.ReceiveOpts{}); len(msgs) != 0 {
		t.Fatalf("replies should be returned once, got %+v", msgs)
	}
}

func TestHandlerErrorIsSendError(t *testing.T) {
	tr := inproc.New(func(context.Context, transport.Session, string) ([]string, error) {
		return nil, errors.New("down")
	})
	s := transport.Session{ID: "a"}
	_ = tr.Open(context.Background(), s)
	if _, err := tr.Send(context.Background(), s, "hi"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnopenedSessionErrors(t *testing.T) {
	tr := inproc.New(echo)
	s := transport.Session{ID: "nope"}
	if _, err := tr.Send(context.Background(), s, "x"); err == nil {
		t.Fatal("send should error")
	}
	if _, err := tr.Receive(context.Background(), s, transport.ReceiveOpts{}); err == nil {
		t.Fatal("receive should error")
	}
	if err := tr.Close(context.Background(), s); err != nil {
		t.Fatalf("close of unopened session should be a no-op: %v", err)
	}
}

func TestAsyncCollectsUntilSettle(t *testing.T) {
	tr := inproc.NewAsync(func(_ context.Context, _ transport.Session, text string, reply func(string)) error {
		go func() {
			time.Sleep(20 * time.Millisecond)
			reply("first")
			time.Sleep(20 * time.Millisecond)
			reply("second")
		}()
		return nil
	})
	ctx := context.Background()
	s := transport.Session{ID: "a"}
	_ = tr.Open(ctx, s)
	_, _ = tr.Send(ctx, s, "hi")
	msgs, err := tr.Receive(ctx, s, transport.ReceiveOpts{Timeout: 2 * time.Second, SettleWindow: 100 * time.Millisecond})
	if err != nil || len(msgs) != 2 || msgs[0].Text != "first" || msgs[1].Text != "second" {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
}

func TestAsyncTimeoutWithNoReply(t *testing.T) {
	tr := inproc.NewAsync(func(context.Context, transport.Session, string, func(string)) error { return nil })
	ctx := context.Background()
	s := transport.Session{ID: "a"}
	_ = tr.Open(ctx, s)
	_, _ = tr.Send(ctx, s, "hi")
	start := time.Now()
	msgs, err := tr.Receive(ctx, s, transport.ReceiveOpts{Timeout: 80 * time.Millisecond, SettleWindow: 10 * time.Millisecond})
	if err != nil || len(msgs) != 0 {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
	if time.Since(start) < 70*time.Millisecond {
		t.Fatal("should wait out the reply timeout")
	}
}

func TestAsyncCancel(t *testing.T) {
	tr := inproc.NewAsync(func(context.Context, transport.Session, string, func(string)) error { return nil })
	s := transport.Session{ID: "a"}
	_ = tr.Open(context.Background(), s)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := tr.Receive(ctx, s, transport.ReceiveOpts{Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected context error")
	}
}

func TestRepliesAfterCloseAreDropped(t *testing.T) {
	var later func(string)
	tr := inproc.NewAsync(func(_ context.Context, _ transport.Session, _ string, reply func(string)) error {
		later = reply
		return nil
	})
	ctx := context.Background()
	s := transport.Session{ID: "a"}
	_ = tr.Open(ctx, s)
	_, _ = tr.Send(ctx, s, "hi")
	_ = tr.Close(ctx, s)
	later("too late") // must not panic
}

func TestHooksAndConcurrentSessions(t *testing.T) {
	var mu sync.Mutex
	opened, closed := 0, 0
	tr := inproc.New(echo,
		inproc.OnOpen(func(context.Context, transport.Session) error { mu.Lock(); opened++; mu.Unlock(); return nil }),
		inproc.OnClose(func(context.Context, transport.Session) error { mu.Lock(); closed++; mu.Unlock(); return nil }),
	)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := transport.Session{ID: id}
			_ = tr.Open(ctx, s)
			_, _ = tr.Send(ctx, s, "x")
			msgs, _ := tr.Receive(ctx, s, transport.ReceiveOpts{})
			if len(msgs) != 1 || msgs[0].Text != id+": x" {
				t.Errorf("session %s got %+v", id, msgs)
			}
			_ = tr.Close(ctx, s)
		}()
	}
	wg.Wait()
	if opened != 4 || closed != 4 {
		t.Fatalf("opened=%d closed=%d", opened, closed)
	}
}

func TestOnOpenErrorFailsOpen(t *testing.T) {
	tr := inproc.New(echo, inproc.OnOpen(func(context.Context, transport.Session) error { return errors.New("no") }))
	if err := tr.Open(context.Background(), transport.Session{ID: "a"}); err == nil {
		t.Fatal("expected error")
	}
}
