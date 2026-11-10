// Package llm defines Obatala's provider-neutral completion interface and a
// registry of LLM providers. Concrete providers (OpenAI, Anthropic,
// OpenAI-compatible endpoints) register by name; the engine adapts these
// types to the scenario-go LLMCompletion interface, so scenario-go stays
// sealed inside the engine package.
package llm

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleDeveloper = "developer"
)

// Message is one chat message in a completion request.
type Message struct {
	Role    string
	Content string
}

// Tool declares a function the model may call.
type Tool struct {
	Name        string
	Description string
	Strict      bool
	Parameters  map[string]any
}

// ToolCall is a model-requested function invocation.
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

// Request is one completion call.
type Request struct {
	Messages    []Message
	Temperature *float64
	MaxTokens   *int64
	Tools       []Tool
	ToolChoice  *string
}

// Usage is token accounting when the provider reports it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
}

// Add accumulates other onto u.
func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
}

// Response is the model's answer; either content or tool calls.
type Response struct {
	Choices []Choice
	Usage   Usage
}

// Choice is one candidate response.
type Choice struct {
	Content   string
	ToolCalls []ToolCall
}

// Completion is implemented by every LLM provider.
type Completion interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// Factory builds a Completion from named settings (from obatala.yaml).
type Factory func(settings map[string]any) (Completion, error)

var (
	// ErrDuplicate is returned when registering a provider name twice.
	ErrDuplicate = errors.New("llm: duplicate name")
	// ErrUnknown is returned when looking up an unregistered provider.
	ErrUnknown = errors.New("llm: unknown name")

	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds an LLM provider factory under name.
func Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("llm: name and factory are required")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := factories[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	factories[name] = f
	return nil
}

// Get returns the factory registered under name.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknown, name)
	}
	return f, nil
}

// Names lists registered provider names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
