package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

type stubTransport struct{}

func (stubTransport) Open(context.Context, transport.Session) error { return nil }

func (stubTransport) Send(context.Context, transport.Session, string) (time.Time, error) {
	return time.Time{}, nil
}

func (stubTransport) Receive(context.Context, transport.Session, transport.ReceiveOpts) ([]transport.Message, error) {
	return nil, nil
}

func (stubTransport) Close(context.Context, transport.Session) error { return nil }

func TestRegisterGetAndNames(t *testing.T) {
	factory := func(map[string]any) (transport.Transport, error) { return stubTransport{}, nil }
	if err := transport.Register("stub", factory); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := transport.Get("stub")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	tr, err := got(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if err := tr.Open(context.Background(), transport.Session{ID: "s1"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	names := transport.Names()
	if len(names) == 0 || names[0] != "stub" {
		t.Fatalf("names = %v, want [stub ...]", names)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	factory := func(map[string]any) (transport.Transport, error) { return stubTransport{}, nil }
	_ = transport.Register("dup-check", factory)
	err := transport.Register("dup-check", factory)
	if !errors.Is(err, transport.ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := transport.Get("missing")
	if !errors.Is(err, transport.ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}

func TestRegisterInvalid(t *testing.T) {
	if err := transport.Register("", nil); err == nil {
		t.Fatal("expected error for empty name and nil factory")
	}
}
