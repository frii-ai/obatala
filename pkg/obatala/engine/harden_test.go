package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

type fnCompletion func(context.Context, llm.Request) (llm.Response, error)

func (f fnCompletion) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return f(ctx, req)
}

func TestNormalizeRewritesUserlessConversation(t *testing.T) {
	req := normalizeRequest(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleAssistant, Content: "Hello, how can I help you today?"},
	}})
	if req.Messages[1].Role != llm.RoleUser {
		t.Fatalf("greeting role = %q, want user", req.Messages[1].Role)
	}
}

func TestNormalizeKeepsExistingUserTurn(t *testing.T) {
	req := normalizeRequest(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	}})
	if req.Messages[2].Role != llm.RoleAssistant {
		t.Fatalf("assistant message rewritten: %+v", req.Messages)
	}
}

func TestNormalizeStripsToolsWithoutToolChoice(t *testing.T) {
	req := normalizeRequest(llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:    []llm.Tool{{Name: verdictToolName}},
	})
	if req.Tools != nil {
		t.Fatalf("tools = %v, want nil", req.Tools)
	}
}

func TestNormalizeKeepsToolsWhenJudging(t *testing.T) {
	choice := toolChoiceRequired
	req := normalizeRequest(llm.Request{
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.Tool{{Name: verdictToolName}},
		ToolChoice: &choice,
	})
	if len(req.Tools) != 1 {
		t.Fatalf("tools = %v", req.Tools)
	}
}

func TestHardenRetriesEmptyContent(t *testing.T) {
	calls := 0
	inner := fnCompletion(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{Choices: []llm.Choice{{Content: ""}}}, nil
		}
		return llm.Response{Choices: []llm.Choice{{Content: "ok"}}}, nil
	})
	h := harden(inner, 3, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Choices[0].Content != "ok" || calls != 2 {
		t.Fatalf("content=%q calls=%d", resp.Choices[0].Content, calls)
	}
}

func TestHardenRetriesSpuriousToolCall(t *testing.T) {
	calls := 0
	inner := fnCompletion(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{Choices: []llm.Choice{{
				ToolCalls: []llm.ToolCall{{
					Name:      verdictToolName,
					Arguments: map[string]any{"success": true},
				}},
			}}}, nil
		}
		return llm.Response{Choices: []llm.Choice{{Content: "ok"}}}, nil
	})
	h := harden(inner, 3, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Choices[0].Content != "ok" || calls != 2 {
		t.Fatalf("content=%q calls=%d", resp.Choices[0].Content, calls)
	}
}

func TestHardenKeepsContentBesideSpuriousToolCall(t *testing.T) {
	calls := 0
	inner := fnCompletion(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		calls++
		return llm.Response{Choices: []llm.Choice{{
			Content:   "one pair, 12 Main Street",
			ToolCalls: []llm.ToolCall{{Name: "place_order"}},
		}}}, nil
	})
	h := harden(inner, 3, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry when content is usable)", calls)
	}
	if resp.Choices[0].Content != "one pair, 12 Main Street" {
		t.Fatalf("content = %q", resp.Choices[0].Content)
	}
	if len(resp.Choices[0].ToolCalls) != 0 {
		t.Fatalf("tool calls = %v, want stripped", resp.Choices[0].ToolCalls)
	}
}

func TestHardenExhaustsAttempts(t *testing.T) {
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Choices: []llm.Choice{{Content: ""}}}, nil
	})
	h := harden(inner, 2, time.Millisecond)
	_, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
}

func TestHardenPropagatesInnerError(t *testing.T) {
	boom := errors.New("boom")
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{}, boom
	})
	h := harden(inner, 3, time.Millisecond)
	_, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestHardenRejectsVerdictWithoutToolCall(t *testing.T) {
	choice := toolChoiceRequired
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Choices: []llm.Choice{{Content: "inconclusive I guess"}}}, nil
	})
	h := harden(inner, 2, time.Millisecond)
	_, err := h.Complete(context.Background(), llm.Request{
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.Tool{{Name: verdictToolName}},
		ToolChoice: &choice,
	})
	if err == nil {
		t.Fatal("expected error when judge does not call the verdict tool")
	}
}

func TestHardenPassesValidVerdict(t *testing.T) {
	choice := toolChoiceRequired
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				Name: verdictToolName,
				Arguments: map[string]any{
					"verdict":   "success",
					"reasoning": "ok",
					"details":   map[string]any{"met_criteria": []any{"a"}},
				},
			}},
		}}}, nil
	})
	h := harden(inner, 2, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.Tool{{Name: verdictToolName}},
		ToolChoice: &choice,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(resp.Choices[0].ToolCalls) != 1 {
		t.Fatalf("tool calls = %v", resp.Choices[0].ToolCalls)
	}
}

func TestValidVerdictCall(t *testing.T) {
	good := []llm.ToolCall{{
		Name: verdictToolName,
		Arguments: map[string]any{
			"verdict":   "success",
			"reasoning": "r",
			"details":   map[string]any{},
		},
	}}
	if !validVerdictCall(good, true) {
		t.Fatal("valid call rejected")
	}
	if validVerdictCall(good, false) {
		t.Fatal("call accepted without advertised tools")
	}
	if validVerdictCall(good[:0], true) {
		t.Fatal("empty calls accepted")
	}
	bad := []llm.ToolCall{{Name: "other", Arguments: map[string]any{}}}
	if validVerdictCall(bad, true) {
		t.Fatal("wrong tool name accepted")
	}
}

func TestHardenCustomerRetriesURLLeak(t *testing.T) {
	calls := 0
	inner := fnCompletion(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{Choices: []llm.Choice{{
				Content: "Your order is confirmed! https://pay.example.com/order_123",
			}}}, nil
		}
		return llm.Response{Choices: []llm.Choice{{Content: "ok thanks"}}}, nil
	})
	h := hardenCustomer(inner, 3, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if calls != 2 || resp.Choices[0].Content != "ok thanks" {
		t.Fatalf("calls=%d content=%q", calls, resp.Choices[0].Content)
	}
}

func TestHardenCustomerExhaustsOnPersistentURLLeak(t *testing.T) {
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Choices: []llm.Choice{{
			Content: "pay here: https://evil.example/pay",
		}}}, nil
	})
	h := hardenCustomer(inner, 2, time.Millisecond)
	_, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("err = %v, want URL role-leakage error", err)
	}
}

func TestHardenWithoutURLGuardAllowsLinks(t *testing.T) {
	inner := fnCompletion(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Choices: []llm.Choice{{
			Content: "check https://example.com/docs",
		}}}, nil
	})
	h := harden(inner, 3, time.Millisecond)
	resp, err := h.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil || !strings.Contains(resp.Choices[0].Content, "https://") {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}
