package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
	"github.com/frii-ai/obatala/pkg/obatala/transport/inproc"
)

// botSystemPrompt pins the demo bot's ground truth so the judge's
// criteria stay decidable: one real price, no invented features.
const botSystemPrompt = `You are Acme's customer-support assistant, chatting with customers.

Rules:
- Keep every reply under two sentences.
- Plans start at $10 per seat. When asked about pricing, share that exact starting price and mention you can send the full price sheet on request.
- You offer exactly two things: a shared team inbox and a knowledge base. Never invent other features, discounts, or prices.
- Never send links. Never claim something was ordered, paid, or confirmed.`

// helloBot is an LLM-powered bot served through the inproc transport:
// inproc calls Reply on every customer message and hands the returned
// text to the engine. One instance serves every session; history is
// keyed by session id so sessions never bleed into each other.
type helloBot struct {
	completion llm.Completion

	mu       sync.Mutex
	sessions map[string]*conversation
}

// conversation is one session's history. Its own lock serializes turns
// within a session without blocking other sessions.
type conversation struct {
	mu      sync.Mutex
	history []llm.Message
}

func init() {
	_ = transport.Register("hello", newHelloTransport)
}

// newHelloTransport builds the bot's LLM from transport.settings and wraps
// the bot in an inproc transport. The engine does not resolve api_key_env
// for transports, so this factory does it (mirroring
// config.Provider.Settings) before handing the settings to the openai
// provider factory.
func newHelloTransport(settings map[string]any) (transport.Transport, error) {
	model, _ := settings["model"].(string)
	if model == "" {
		return nil, fmt.Errorf("hello: transport settings require a model")
	}
	key := ""
	if env, _ := settings["api_key_env"].(string); env != "" {
		key = os.Getenv(env)
		if key == "" {
			return nil, fmt.Errorf("hello: env %s (bot api key) is not set", env)
		}
	}
	factory, err := llm.Get("openai")
	if err != nil {
		return nil, err
	}
	completion, err := factory(map[string]any{
		"model":    model,
		"base_url": settings["base_url"],
		"api_key":  key,
	})
	if err != nil {
		return nil, err
	}
	b := &helloBot{completion: completion, sessions: map[string]*conversation{}}
	return inproc.New(b.Reply, inproc.OnOpen(b.open), inproc.OnClose(b.close)), nil
}

func (b *helloBot) open(_ context.Context, s transport.Session) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions[s.ID] = &conversation{
		history: []llm.Message{{Role: llm.RoleSystem, Content: botSystemPrompt}},
	}
	return nil
}

func (b *helloBot) close(_ context.Context, s transport.Session) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessions, s.ID)
	return nil
}

// Reply appends the customer's message, asks the LLM for the bot's
// answer, and returns it. inproc timestamps the send before calling
// Reply, so turn latency includes the LLM call.
func (b *helloBot) Reply(ctx context.Context, s transport.Session, message string) ([]string, error) {
	b.mu.Lock()
	conv := b.sessions[s.ID]
	b.mu.Unlock()
	if conv == nil {
		return nil, fmt.Errorf("hello: session %q is not open", s.ID)
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()

	conv.history = append(conv.history, llm.Message{Role: llm.RoleUser, Content: message})
	resp, err := b.completion.Complete(ctx, llm.Request{Messages: conv.history})
	if err != nil {
		return nil, fmt.Errorf("hello: bot completion: %w", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
		return nil, fmt.Errorf("hello: bot returned an empty reply")
	}
	text := resp.Choices[0].Content
	conv.history = append(conv.history, llm.Message{Role: llm.RoleAssistant, Content: text})
	return []string{text}, nil
}
