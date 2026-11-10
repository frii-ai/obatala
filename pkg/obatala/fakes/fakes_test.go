package fakes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

func TestTransportScriptedReplies(t *testing.T) {
	tr := fakes.NewTransport(
		[]string{"first"},
		[]string{"second a", "second b"},
	)
	session := transport.Session{ID: "s1"}
	if err := tr.Open(context.Background(), session); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := tr.Send(context.Background(), session, "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	first, err := tr.Receive(context.Background(), session, transport.ReceiveOpts{})
	if err != nil || len(first) != 1 || first[0].Text != "first" {
		t.Fatalf("first = %v, err = %v", first, err)
	}
	second, err := tr.Receive(context.Background(), session, transport.ReceiveOpts{})
	if err != nil || len(second) != 2 || second[1].Text != "second b" {
		t.Fatalf("second = %v, err = %v", second, err)
	}
	if _, err := tr.Receive(context.Background(), session, transport.ReceiveOpts{}); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if err := tr.Close(context.Background(), session); err != nil {
		t.Fatalf("close: %v", err)
	}
	if tr.Opens != 1 || tr.Closes != 1 || len(tr.Sends) != 1 {
		t.Fatalf("counts: opens=%d closes=%d sends=%d", tr.Opens, tr.Closes, len(tr.Sends))
	}
}

func TestTransportErrors(t *testing.T) {
	boom := errors.New("boom")
	tr := &fakes.Transport{SendErr: boom}
	if _, err := tr.Send(context.Background(), transport.Session{}, "x"); !errors.Is(err, boom) {
		t.Fatalf("send err = %v", err)
	}
	tr = &fakes.Transport{ReceiveErr: boom}
	if _, err := tr.Receive(context.Background(), transport.Session{}, transport.ReceiveOpts{}); !errors.Is(err, boom) {
		t.Fatalf("receive err = %v", err)
	}
}

func TestCompletionChatThenVerdict(t *testing.T) {
	c := fakes.NewCustomer("hi", "ok send link")
	first, err := c.Complete(context.Background(), llm.Request{})
	if err != nil || first.Choices[0].Content != "hi" {
		t.Fatalf("first = %+v err = %v", first, err)
	}
	second, err := c.Complete(context.Background(), llm.Request{})
	if err != nil || second.Choices[0].Content != "ok send link" {
		t.Fatalf("second = %+v err = %v", second, err)
	}
	third, err := c.Complete(context.Background(), llm.Request{})
	if err != nil || third.Choices[0].Content != "ok send link" {
		t.Fatalf("third should repeat last = %+v err = %v", third, err)
	}

	choice := "required"
	verdict, err := c.Complete(context.Background(), llm.Request{ToolChoice: &choice})
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	tc := verdict.Choices[0].ToolCalls[0]
	if tc.Name != "finish_test" || tc.Arguments["verdict"] != "success" {
		t.Fatalf("verdict tool call = %+v", tc)
	}
	if c.VerdictCalls != 1 {
		t.Fatalf("verdict calls = %d", c.VerdictCalls)
	}
}

func TestCompletionFailureVerdict(t *testing.T) {
	c := fakes.NewVerdict("failure", "bot misbehaved", "hi")
	c.Triggered = []string{"bad thing"}
	_, _ = c.Complete(context.Background(), llm.Request{})
	choice := "required"
	verdict, err := c.Complete(context.Background(), llm.Request{ToolChoice: &choice})
	if err != nil {
		t.Fatalf("verdict: %v", err)
	}
	args := verdict.Choices[0].ToolCalls[0].Arguments
	if args["verdict"] != "failure" || args["reasoning"] != "bot misbehaved" {
		t.Fatalf("args = %v", args)
	}
	details := args["details"].(map[string]any)
	triggered := details["triggered_failures"].([]any)
	if len(triggered) != 1 || triggered[0] != "bad thing" {
		t.Fatalf("triggered = %v", triggered)
	}
}
