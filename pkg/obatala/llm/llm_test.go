package llm_test

import (
	"context"
	"errors"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

type stubCompletion struct{}

func (stubCompletion) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func TestRegisterGetAndComplete(t *testing.T) {
	factory := func(map[string]any) (llm.Completion, error) { return stubCompletion{}, nil }
	if err := llm.Register("stub", factory); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := llm.Get("stub")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	c, err := got(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	factory := func(map[string]any) (llm.Completion, error) { return stubCompletion{}, nil }
	_ = llm.Register("dup-check", factory)
	err := llm.Register("dup-check", factory)
	if !errors.Is(err, llm.ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := llm.Get("missing")
	if !errors.Is(err, llm.ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}
