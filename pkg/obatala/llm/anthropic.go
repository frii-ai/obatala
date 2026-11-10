package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	_ = Register("anthropic", newAnthropic)
}

// Settings keys for the anthropic provider: "model" (required), "api_key"
// (required; falls back to ANTHROPIC_API_KEY), "base_url" (optional;
// defaults to https://api.anthropic.com/).
const anthropicVersion = "2023-06-01"

func newAnthropic(settings map[string]any) (Completion, error) {
	model, _ := settings["model"].(string)
	if model == "" {
		return nil, fmt.Errorf("llm: anthropic provider requires a model")
	}
	key, _ := settings["api_key"].(string)
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("llm: anthropic provider requires an api_key")
	}
	base := "https://api.anthropic.com/"
	if b, _ := settings["base_url"].(string); b != "" {
		base = b
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return &anthropicProvider{
		model: model,
		key:   key,
		base:  base,
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}, nil
}

type anthropicProvider struct {
	model  string
	key    string
	base   string
	client *http.Client
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int64              `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  *anthropicChoice   `json:"tool_choice,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicChoice struct {
	Type string `json:"type"` // "any", "tool", "none"
	Name string `json:"name,omitempty"`
}

type anthropicResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *anthropicProvider) Complete(ctx context.Context, req Request) (Response, error) {
	areq := anthropicRequest{
		Model:     p.model,
		MaxTokens: 4096,
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		areq.MaxTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		areq.Temperature = req.Temperature
	}
	var system []string
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem, RoleDeveloper:
			system = append(system, m.Content)
		case RoleUser:
			areq.Messages = append(areq.Messages, anthropicMessage{Role: "user", Content: m.Content})
		case RoleAssistant:
			areq.Messages = append(areq.Messages, anthropicMessage{Role: "assistant", Content: m.Content})
		default:
			return Response{}, fmt.Errorf("llm: anthropic: unknown message role %q", m.Role)
		}
	}
	areq.System = strings.Join(system, "\n\n")
	if len(areq.Messages) == 0 {
		return Response{}, fmt.Errorf("llm: anthropic: at least one user/assistant message is required")
	}
	for _, t := range req.Tools {
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		areq.Tools = append(areq.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	if len(areq.Tools) > 0 && req.ToolChoice != nil && *req.ToolChoice != "" {
		areq.ToolChoice = &anthropicChoice{Type: "any"}
	}

	body, err := json.Marshal(areq)
	if err != nil {
		return Response{}, fmt.Errorf("llm: anthropic encode: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.key)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("llm: anthropic completion: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("llm: anthropic read: %w", err)
	}
	var parsed anthropicResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, fmt.Errorf("llm: anthropic decode (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Type + ": " + parsed.Error.Message
		}
		return Response{}, fmt.Errorf("llm: anthropic: status %d: %s", resp.StatusCode, msg)
	}

	out := Response{
		Choices: make([]Choice, 0, 1),
		Usage: Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
		},
	}
	var choice Choice
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			choice.Content += block.Text
		case "tool_use":
			var args map[string]any
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &args); err != nil {
					return Response{}, fmt.Errorf("llm: anthropic parse tool input: %w", err)
				}
			}
			choice.ToolCalls = append(choice.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: args,
			})
		}
	}
	out.Choices = append(out.Choices, choice)
	return out, nil
}
