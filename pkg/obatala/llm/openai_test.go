package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

func TestOpenAIProviderRequiresModel(t *testing.T) {
	factory, err := llm.Get("openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := factory(map[string]any{}); err == nil {
		t.Fatal("expected error when model is missing")
	}
}

func TestOpenAIProviderMapsRequestAndResponse(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "",
					"tool_calls": [{
						"id": "call-9",
						"type": "function",
						"function": {"name": "finish_test", "arguments": "{\"verdict\":\"success\",\"reasoning\":\"ok\",\"details\":{\"met_criteria\":[\"a\"]}}"}
					}]
				},
				"finish_reason": "tool_calls"
			}]
		}`))
	}))
	defer server.Close()

	factory, err := llm.Get("openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	c, err := factory(map[string]any{
		"model":    "test-model",
		"base_url": server.URL + "/v1/",
		"api_key":  "test-key",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}

	toolChoice := "required"
	temperature := 0.0
	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "sys"},
			{Role: llm.RoleUser, Content: "hi"},
		},
		Temperature: &temperature,
		Tools: []llm.Tool{{
			Name:        "finish_test",
			Description: "verdict",
			Parameters:  map[string]any{"type": "object"},
		}},
		ToolChoice: &toolChoice,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if gotBody["model"] != "test-model" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["temperature"] != 0.0 {
		t.Fatalf("temperature = %v", gotBody["temperature"])
	}
	if gotBody["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v", gotBody["tool_choice"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", gotBody["tools"])
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %v", resp.Choices)
	}
	choice := resp.Choices[0]
	if len(choice.ToolCalls) != 1 || choice.ToolCalls[0].Name != "finish_test" {
		t.Fatalf("tool calls = %v", choice.ToolCalls)
	}
	if choice.ToolCalls[0].Arguments["verdict"] != "success" {
		t.Fatalf("args = %v", choice.ToolCalls[0].Arguments)
	}
	details, ok := choice.ToolCalls[0].Arguments["details"].(map[string]any)
	if !ok || details == nil {
		t.Fatalf("details = %v", choice.ToolCalls[0].Arguments["details"])
	}
}

func TestOpenAIProviderRejectsUnknownRole(t *testing.T) {
	factory, err := llm.Get("openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	c, err := factory(map[string]any{"model": "m", "base_url": "http://127.0.0.1:0/", "api_key": "k"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: "tool", Content: "x"}},
	}); err == nil {
		t.Fatal("expected error for unknown role")
	}
}
