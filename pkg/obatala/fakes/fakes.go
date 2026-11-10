// Package fakes provides scriptable Transport and LLM fakes so runners,
// reporters and user code can be tested offline without real backends or
// paid LLM calls.
package fakes

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// Transport is a scriptable transport.Transport. Each Send consumes the next
// scripted reply batch; Replies may be exhausted to force an error.
type Transport struct {
	mu sync.Mutex

	// Replies are the bot's reply texts, one slice per turn.
	Replies [][]string
	// ReplyDelay is inserted before each batch is delivered.
	ReplyDelay time.Duration
	// SendErr, when set, fails every Send.
	SendErr error
	// ReceiveErr, when set, fails every Receive.
	ReceiveErr error
	// OpenErr, when set, fails every Open.
	OpenErr error
	// CloseErr, when set, fails every Close.
	CloseErr error

	Sends  []string
	Opens  int
	Closes int
}

// NewTransport returns a Transport with the given scripted reply batches.
func NewTransport(replies ...[]string) *Transport {
	return &Transport{Replies: replies}
}

func (t *Transport) Open(context.Context, transport.Session) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Opens++
	return t.OpenErr
}

func (t *Transport) Send(_ context.Context, _ transport.Session, text string) (time.Time, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.SendErr != nil {
		return time.Time{}, t.SendErr
	}
	t.Sends = append(t.Sends, text)
	return time.Now(), nil
}

func (t *Transport) Receive(ctx context.Context, _ transport.Session, _ transport.ReceiveOpts) ([]transport.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ReceiveErr != nil {
		return nil, t.ReceiveErr
	}
	if t.ReplyDelay > 0 {
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			t.mu.Lock()
			return nil, ctx.Err()
		case <-time.After(t.ReplyDelay):
		}
		t.mu.Lock()
	}
	if len(t.Replies) == 0 {
		return nil, fmt.Errorf("fakes: no scripted replies left")
	}
	batch := t.Replies[0]
	t.Replies = t.Replies[1:]
	now := time.Now()
	msgs := make([]transport.Message, len(batch))
	for i, text := range batch {
		msgs[i] = transport.Message{Text: text, At: now.Add(time.Duration(i) * time.Millisecond)}
	}
	return msgs, nil
}

func (t *Transport) Close(context.Context, transport.Session) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Closes++
	return t.CloseErr
}

// Completion is a scriptable llm.Completion. Non-verdict calls return
// Contents in order (the last entry repeats); a call with ToolChoice set
// returns the configured finish_test verdict.
type Completion struct {
	mu sync.Mutex

	// Contents are customer-sim messages in order; the last repeats.
	Contents []string
	// Verdict is "success", "failure" or "inconclusive".
	Verdict   string
	Reasoning string
	Met       []string
	Unmet     []string
	Triggered []string
	// Usage is reported on every response (zero = provider didn't say).
	Usage llm.Usage
	// Err, when set, fails every call.
	Err error
	// EmptyFirst makes the first call return empty content (degenerate).
	EmptyFirst bool

	Calls        int
	VerdictCalls int
	contentCalls int
}

// NewCustomer returns a Completion that chats with the given messages.
func NewCustomer(contents ...string) *Completion {
	return &Completion{Contents: contents, Verdict: "success", Reasoning: "scripted success"}
}

// NewVerdict returns a Completion that returns the given verdict on the
// judge turn and one scripted customer message before it.
func NewVerdict(verdict, reasoning string, contents ...string) *Completion {
	return &Completion{
		Contents:  contents,
		Verdict:   verdict,
		Reasoning: reasoning,
	}
}

func (c *Completion) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return llm.Response{}, c.Err
	}

	if req.ToolChoice != nil {
		c.VerdictCalls++
		details := map[string]any{
			"met_criteria":       toAny(c.Met),
			"unmet_criteria":     toAny(c.Unmet),
			"triggered_failures": toAny(c.Triggered),
		}
		return llm.Response{Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				ID:   fmt.Sprintf("call-%d", c.VerdictCalls),
				Name: "finish_test",
				Arguments: map[string]any{
					"verdict":   c.Verdict,
					"reasoning": c.Reasoning,
					"details":   details,
				},
			}},
		}}, Usage: c.Usage}, nil
	}

	if c.EmptyFirst && c.contentCalls == 0 {
		c.contentCalls++
		return llm.Response{Choices: []llm.Choice{{Content: ""}}, Usage: c.Usage}, nil
	}
	if len(c.Contents) == 0 {
		return llm.Response{}, fmt.Errorf("fakes: no scripted contents left")
	}
	idx := c.contentCalls
	c.contentCalls++
	if idx >= len(c.Contents) {
		idx = len(c.Contents) - 1
	}
	return llm.Response{Choices: []llm.Choice{{Content: c.Contents[idx]}}, Usage: c.Usage}, nil
}

func toAny(in []string) []any {
	if in == nil {
		return []any{}
	}
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
