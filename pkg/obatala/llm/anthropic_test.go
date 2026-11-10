package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

func anthropicFactory(t *testing.T, baseURL string) llm.Completion {
	t.Helper()
	factory, err := llm.Get("anthropic")
	if err != nil {
		t.Fatalf("get anthropic: %v", err)
	}
	c, err := factory(map[string]any{
		"model":    "test-model",
		"api_key":  "test-key",
		"base_url": baseURL,
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return c
}

func TestAnthropicRequiresModelAndKey(t *testing.T) {
	factory, _ := llm.Get("anthropic")
	if _, err := factory(map[string]any{}); err == nil {
		t.Fatal("expected error when model is missing")
	}
	if _, err := factory(map[string]any{"model": "m"}); err == nil {
		t.Fatal("expected error when api_key is missing")
	}
}

func TestAnthropicMapsTextResponseAndUsage(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{
			"content": [{"type": "text", "text": "do you have socks?"}],
			"usage": {"input_tokens": 120, "output_tokens": 9}
		}`))
	}))
	defer server.Close()

	c := anthropicFactory(t, server.URL)
	choice := 0.0
	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "sys"},
			{Role: llm.RoleAssistant, Content: "Hello!"},
			{Role: llm.RoleUser, Content: "hi"},
		},
		Temperature: &choice,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Choices[0].Content != "do you have socks?" {
		t.Fatalf("content = %q", resp.Choices[0].Content)
	}
	if resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 9 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if gotBody["system"] != "sys" {
		t.Fatalf("system = %v", gotBody["system"])
	}
	msgs := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestAnthropicMapsToolCall(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{
			"content": [{"type": "tool_use", "id": "toolu_1", "name": "finish_test",
				"input": {"verdict": "success", "reasoning": "ok", "details": {"met_criteria": ["a"], "unmet_criteria": [], "triggered_failures": []}}}],
			"usage": {"input_tokens": 50, "output_tokens": 20}
		}`))
	}))
	defer server.Close()

	c := anthropicFactory(t, server.URL)
	required := "required"
	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "judge now"}},
		Tools: []llm.Tool{{
			Name:        "finish_test",
			Description: "complete the test",
			Parameters:  map[string]any{"type": "object"},
		}},
		ToolChoice: &required,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	tc := resp.Choices[0].ToolCalls[0]
	if tc.Name != "finish_test" || tc.Arguments["verdict"] != "success" {
		t.Fatalf("tool call = %+v", tc)
	}
	tools := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	tc2 := gotBody["tool_choice"].(map[string]any)
	if tc2["type"] != "any" {
		t.Fatalf("tool_choice = %v", tc2)
	}
}

func TestAnthropicErrorSurfacesMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer server.Close()

	c := anthropicFactory(t, server.URL)
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicRejectsSystemOnlyConversation(t *testing.T) {
	c := anthropicFactory(t, "http://localhost:0")
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "sys only"}},
	})
	if err == nil {
		t.Fatal("expected error for system-only conversation")
	}
}
