package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
	"github.com/openai/openai-go/shared/constant"
)

func init() {
	_ = Register("openai", newOpenAI)
}

// Settings keys for the openai provider: "model" (required), "base_url"
// (optional; any OpenAI-compatible endpoint), "api_key" (optional; falls back
// to OPENAI_API_KEY, and may be empty for local endpoints).
func newOpenAI(settings map[string]any) (Completion, error) {
	model, _ := settings["model"].(string)
	if model == "" {
		return nil, fmt.Errorf("llm: openai provider requires a model")
	}
	var opts []option.RequestOption
	if base, _ := settings["base_url"].(string); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	key, _ := settings["api_key"].(string)
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	return &openAIProvider{model: model, client: openai.NewClient(opts...)}, nil
}

type openAIProvider struct {
	model  string
	client openai.Client
}

func (p *openAIProvider) Complete(ctx context.Context, req Request) (Response, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, len(req.Messages))
	for i, m := range req.Messages {
		switch m.Role {
		case RoleUser:
			messages[i] = openai.UserMessage(m.Content)
		case RoleAssistant:
			messages[i] = openai.AssistantMessage(m.Content)
		case RoleSystem:
			messages[i] = openai.SystemMessage(m.Content)
		case RoleDeveloper:
			messages[i] = openai.DeveloperMessage(m.Content)
		default:
			return Response{}, fmt.Errorf("llm: unknown message role %q", m.Role)
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(p.model),
		Messages: messages,
	}
	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}
	if req.MaxTokens != nil {
		params.MaxTokens = openai.Int(*req.MaxTokens)
	}
	if len(req.Tools) > 0 {
		tools := make([]openai.ChatCompletionToolParam, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = openai.ChatCompletionToolParam{
				Type: constant.Function("function"),
				Function: shared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: openai.String(t.Description),
					Strict:      param.NewOpt(t.Strict),
					Parameters:  t.Parameters,
				},
			}
		}
		params.Tools = tools
	}
	if req.ToolChoice != nil && *req.ToolChoice != "" {
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt(*req.ToolChoice),
		}
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("llm: openai completion: %w", err)
	}

	out := Response{
		Choices: make([]Choice, len(completion.Choices)),
		Usage: Usage{
			PromptTokens:     int(completion.Usage.PromptTokens),
			CompletionTokens: int(completion.Usage.CompletionTokens),
		},
	}
	for i, choice := range completion.Choices {
		out.Choices[i].Content = choice.Message.Content
		for _, tc := range choice.Message.ToolCalls {
			var args map[string]any
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					return Response{}, fmt.Errorf("llm: parse tool call arguments: %w", err)
				}
			}
			out.Choices[i].ToolCalls = append(out.Choices[i].ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: args,
			})
		}
	}
	return out, nil
}
