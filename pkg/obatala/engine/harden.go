package engine

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

// urlPattern matches URLs in model output. Used to hard-block customer
// role leakage (a customer fabricating payment links — observed with
// gemma4): when forbidURLs is set, any URL makes the response degenerate
// and it is retried like empty content.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// harden wraps a Completion with workarounds for scenario-go v1.0.0 and
// hosted OpenAI-compatible relays:
//   - scenario-go's first call is [system, assistant greeting] with no user
//     turn, which some relays reject with an empty "load" response, so the
//     leading greeting is rewritten to the user role;
//   - scenario-go advertises the finish_test tool on every call, and
//     reasoning models then emit premature verdicts, so tools are only sent
//     when a verdict is expected (toolChoice set);
//   - degenerate responses (empty content, spurious or malformed tool
//     calls) are retried with backoff here, not at scenario level.
func harden(c llm.Completion, attempts int, baseBackoff time.Duration) *hardened {
	if attempts <= 0 {
		attempts = 5
	}
	if baseBackoff <= 0 {
		baseBackoff = 2 * time.Second
	}
	return &hardened{inner: c, attempts: attempts, baseBackoff: baseBackoff}
}

// hardenCustomer is harden plus the no-URL guard for the customer
// simulator (role leakage prevention).
func hardenCustomer(c llm.Completion, attempts int, baseBackoff time.Duration) *hardened {
	h := harden(c, attempts, baseBackoff)
	h.forbidURLs = true
	return h
}

type hardened struct {
	inner       llm.Completion
	attempts    int
	baseBackoff time.Duration
	forbidURLs  bool
}

func (h *hardened) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	req = normalizeRequest(req)

	var lastProblem string
	for attempt := 1; attempt <= h.attempts; attempt++ {
		resp, err := h.inner.Complete(ctx, req)
		if err != nil {
			return llm.Response{}, err
		}
		resp = stripUnrequestedToolCalls(resp, len(req.Tools) > 0)
		problem := degenerate(resp, len(req.Tools) > 0, req.ToolChoice)
		if problem == "" && h.forbidURLs && customerLeakedURL(resp) {
			problem = "customer message contains a URL (role leakage; set customer.allow_urls to permit)"
		}
		if problem == "" {
			return resp, nil
		}
		lastProblem = problem
		if os.Getenv("OBATALA_DEBUG_LLM") != "" && len(resp.Choices) > 0 {
			c := resp.Choices[0]
			_, _ = fmt.Fprintf(os.Stderr, "obatala: degenerate LLM response (attempt %d/%d): %s; content_len=%d tool_calls=%d\n",
				attempt, h.attempts, problem, len(c.Content), len(c.ToolCalls))
			full := os.Getenv("OBATALA_DEBUG_LLM") == "full"
			for i, m := range req.Messages {
				text := head(m.Content, 80)
				if full {
					text = m.Content
				}
				_, _ = fmt.Fprintf(os.Stderr, "    req msg[%d] role=%s len=%d head=%q\n", i, m.Role, len(m.Content), text)
			}
		}
		if attempt < h.attempts {
			backoff := time.Duration(attempt+1) * h.baseBackoff
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return llm.Response{}, fmt.Errorf("engine: degenerate completion after %d attempts: %s", h.attempts, lastProblem)
}

func customerLeakedURL(resp llm.Response) bool {
	return len(resp.Choices) > 0 && urlPattern.MatchString(resp.Choices[0].Content)
}

func normalizeRequest(req llm.Request) llm.Request {
	if !hasUserMessage(req.Messages) {
		req.Messages = rewriteAssistantToUser(req.Messages)
	}
	if req.ToolChoice == nil {
		req.Tools = nil
	}
	return req
}

func hasUserMessage(messages []llm.Message) bool {
	for _, m := range messages {
		if m.Role == llm.RoleUser {
			return true
		}
	}
	return false
}

func rewriteAssistantToUser(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Role == llm.RoleAssistant {
			out[i].Role = llm.RoleUser
			break
		}
	}
	return out
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// stripUnrequestedToolCalls drops tool calls from responses when the
// request advertised no tools — some models (observed with gemma4) emit
// spurious tool calls on plain-chat completions. Any accompanying content
// is kept; an empty response is still rejected by degenerate().
func stripUnrequestedToolCalls(resp llm.Response, toolsAdvertised bool) llm.Response {
	if toolsAdvertised {
		return resp
	}
	for i := range resp.Choices {
		resp.Choices[i].ToolCalls = nil
	}
	return resp
}

func degenerate(resp llm.Response, toolsAdvertised bool, toolChoice *string) string {
	if len(resp.Choices) == 0 {
		return "no choices"
	}
	choice := resp.Choices[0]
	if len(choice.ToolCalls) > 0 {
		if !validVerdictCall(choice.ToolCalls, toolsAdvertised) {
			return "spurious or malformed tool call"
		}
		return ""
	}
	if toolChoice != nil && *toolChoice != "" {
		return "expected a verdict tool call, got content only"
	}
	if choice.Content == "" {
		return "empty content"
	}
	return ""
}

func validVerdictCall(calls []llm.ToolCall, toolsAdvertised bool) bool {
	if !toolsAdvertised || len(calls) != 1 {
		return false
	}
	tc := calls[0]
	if tc.Name != verdictToolName {
		return false
	}
	_, hasVerdict := tc.Arguments["verdict"].(string)
	_, hasReasoning := tc.Arguments["reasoning"].(string)
	_, hasDetails := tc.Arguments["details"].(map[string]any)
	return hasVerdict && hasReasoning && hasDetails
}
