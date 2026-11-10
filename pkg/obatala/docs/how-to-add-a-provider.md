# How to add an LLM provider

Obatala is provider-neutral: the customer simulator and the judge talk to
your bot's model through one interface. Two providers ship built in —
`openai` (any OpenAI-compatible endpoint: Ollama, vLLM, Gemini's compat
layer) and `anthropic` — and switching between them is config-only:

```yaml
customer:
  provider: anthropic
  model: claude-sonnet-4-5
  api_key_env: ANTHROPIC_API_KEY
judge:                     # can differ from the customer
  provider: openai
  model: gpt-4.1
  api_key_env: OPENAI_API_KEY
```

## The contract

```go
package llm

type Message struct{ Role, Content string } // system/user/assistant/developer

type Request struct {
    Messages    []Message
    Temperature *float64
    MaxTokens   *int64
    Tools       []Tool        // judge-only: the finish_test verdict tool
    ToolChoice  *string       // non-nil = tool use is required
}

type Response struct {
    Choices []Choice  // Choice{Content string; ToolCalls []ToolCall}
    Usage   Usage     // token counts when the API reports them
}

type Completion interface {
    Complete(ctx context.Context, req Request) (Response, error)
}
```

Two call shapes:

- **Customer simulator**: plain chat messages → `Choice.Content` (the
  buyer's next message). No tools.
- **Judge**: same conversation plus the `finish_test` tool and a required
  tool choice → one `ToolCall` named `finish_test` whose arguments carry
  `verdict` (`success`/`failure`/`inconclusive`), `reasoning`, and
  `details` (met/unmet criteria). Map the tool's JSON input to
  `ToolCall.Arguments`.

## Rules

- **API keys come from settings, never hardcoded.** Follow the convention
  `api_key: settings["api_key"]` filled by config from `api_key_env`
  (env var name in YAML, secret value in env only).
- **Report usage when available** (`Response.Usage`) — it aggregates into
  the run summary and JSON reports.
- **Errors**: return them; the engine retries degenerate/empty responses
  with backoff (and permanently rejects customer messages containing URLs
  — role-leak prevention) and surfaces hard failures as scenario errors.
  Don't retry inside the provider unless the API demands it.
- **Timeout**: set an `http.Client` timeout (5 minutes is the convention
  here); respect `ctx`.

## Register it

```go
func init() {
    _ = llm.Register("myprovider", func(settings map[string]any) (llm.Completion, error) {
        model, _ := settings["model"].(string)
        if model == "" {
            return nil, fmt.Errorf("llm: myprovider requires a model")
        }
        return &provider{model: model, key: settings["api_key"].(string)}, nil
    })
}
```

Use it by name from `obatala.yaml` (`customer.provider`/`judge.provider`).
References: `pkg/obatala/llm/openai.go` (chat completions) and
`pkg/obatala/llm/anthropic.go` (Messages API); fake-HTTP tests in
`*_test.go` there show the request/response mapping without live keys.
