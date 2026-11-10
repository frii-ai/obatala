package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	scenario "github.com/langwatch/scenario-go"
)

func TestCustomerPromptHasNoToolMentions(t *testing.T) {
	var gotSystem string
	customer := fnCompletion(func(_ context.Context, req llm.Request) (llm.Response, error) {
		gotSystem = req.Messages[0].Content
		if len(req.Tools) != 0 {
			t.Errorf("customer request advertised tools: %v", req.Tools)
		}
		return llm.Response{Choices: []llm.Choice{{Content: "do you have cotton socks?"}}}, nil
	})
	agent := newTestingAgent(customer, nil)
	msg, result, err := agent.GenerateNextMessage(context.Background(),
		"enquire about socks", "ask for socks then a payment link",
		[]string{"bot sends a payment link"}, []string{"bot is rude"},
		nil, false, false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if result != nil || msg == nil || *msg != "do you have cotton socks?" {
		t.Fatalf("msg=%v result=%v", msg, result)
	}
	for _, banned := range []string{"finish_test", "tool", "verdict"} {
		if strings.Contains(strings.ToLower(gotSystem), banned) {
			t.Errorf("customer system prompt mentions %q:\n%s", banned, gotSystem)
		}
	}
}

func TestJudgePromptKeepsFinishToolInstruction(t *testing.T) {
	var gotTools int
	judge := fnCompletion(func(_ context.Context, req llm.Request) (llm.Response, error) {
		gotTools = len(req.Tools)
		return llm.Response{Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				Name: verdictToolName,
				Arguments: map[string]any{
					"verdict":   "success",
					"reasoning": "ok",
					"details": map[string]any{
						"met_criteria":       []string{"link sent"},
						"unmet_criteria":     []string{},
						"triggered_failures": []string{},
					},
				},
			}},
		}}}, nil
	})
	agent := newTestingAgent(nil, judge)
	msg, result, err := agent.GenerateNextMessage(context.Background(),
		"enquire about socks", "get a payment link",
		[]string{"bot sends a payment link"}, []string{"bot is rude"},
		[]scenario.Message{{Role: "assistant", Content: "Hi!"}}, false, true)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if msg != nil || result == nil {
		t.Fatalf("msg=%v result=%v", msg, result)
	}
	if gotTools != 1 {
		t.Fatalf("judge tools = %d, want 1", gotTools)
	}
}
