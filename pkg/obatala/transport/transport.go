// Package transport defines how Obatala sends a customer message to the bot
// and receives the bot's replies, plus a registry so concrete transports
// (webhook + queue, in-process, HTTP APIs, …) plug in by name.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Session identifies one scenario run's conversation. IDs must be unique per
// run so parallel runs never see each other's state or replies.
type Session struct {
	ID string
	// Identity is the run's synthetic actor id (prepare.Manager's
	// NewIdentity); backends key per-conversation state on it.
	Identity string
	// FixtureKey is the fixture this run's scenario references (empty for
	// bots without fixtures).
	FixtureKey string
	Meta       map[string]string
}

// Message is one bot reply collected from the transport.
type Message struct {
	Text string
	At   time.Time
	Raw  json.RawMessage
}

// ReceiveOpts controls how long a turn waits for bot replies.
type ReceiveOpts struct {
	Timeout      time.Duration
	SettleWindow time.Duration
}

// Transport carries the conversation between the simulated customer and the
// bot. One instance may serve many sessions; one session per scenario run.
type Transport interface {
	Open(ctx context.Context, s Session) error
	Send(ctx context.Context, s Session, text string) (sentAt time.Time, err error)
	Receive(ctx context.Context, s Session, o ReceiveOpts) ([]Message, error)
	Close(ctx context.Context, s Session) error
}

// Factory builds a Transport from named settings (from obatala.yaml).
type Factory func(settings map[string]any) (Transport, error)

var (
	// ErrDuplicate is returned when registering a transport name twice.
	ErrDuplicate = errors.New("transport: duplicate name")
	// ErrUnknown is returned when looking up an unregistered transport.
	ErrUnknown = errors.New("transport: unknown name")

	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a transport factory under name.
func Register(name string, f Factory) error {
	if name == "" || f == nil {
		return fmt.Errorf("transport: name and factory are required")
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

// Names lists registered transport names, sorted.
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
