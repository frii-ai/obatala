// Package prepare defines the control-plane seam a bot adapter plugs into:
// fixture loading/seeding, run identity allocation, and teardown. The
// runner in pkg/obatala never imports bot-specific code — adapters
// register a Manager factory (typically from init) and the engine drives
// it through this interface.
package prepare

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

// FixtureSet is one suite's loaded fixtures. The payload behind the
// interface is adapter-defined (any YAML/JSON schema the bot's backend
// needs); the engine only ever asks which fixture keys exist so it can
// validate scenario references and drive Seed per key.
type FixtureSet interface {
	// Keys returns every available fixture key (sorted, or any order).
	Keys() []string
}

// Manager is a bot adapter's control-plane seam: the one place a bot
// plugs in preflight, fixture loading/seeding, run identity generation,
// and teardown. Implementations are registered by the bot's adapter
// package (typically from init), so the generic engine never imports
// bot-specific code.
type Manager interface {
	// Preflight verifies the backend is safe to test against and returns
	// the run metadata stamped into every report (environment, versions,
	// adapter extras).
	Preflight(ctx context.Context) (report.RunMeta, error)
	// LoadFixtures parses the suite's fixture directory (adapter-owned
	// schema) into a FixtureSet. Bots without backend state may return an
	// empty set; scenarios then omit the fixture key.
	LoadFixtures(dir string) (FixtureSet, error)
	// Seed creates (or reuses) the fixture identified by key and returns
	// the per-session meta the transport needs (e.g. webhook tokens).
	Seed(ctx context.Context, key string, fx FixtureSet) (map[string]string, error)
	// NewIdentity returns a fresh synthetic actor id for one scenario
	// run. The engine passes it into the transport session and hands it
	// back in Teardown's perRunIDs. Adapters whose backends validate
	// identity formats implement the format here; others return a UUID.
	NewIdentity() (string, error)
	// Teardown removes everything the run created, per-run identities
	// first and suite-scoped fixtures second. Failures are joined into
	// one error. Called after any outcome.
	Teardown(ctx context.Context, perRunIDs []string, perSuiteKeys []string) error
}

// ManagerFactory builds a Manager from the loaded config (once per run).
type ManagerFactory func(cfg config.Config) (Manager, error)

var (
	// ErrDuplicateManager is returned when registering a manager name twice.
	ErrDuplicateManager = errors.New("prepare: duplicate manager")
	// ErrUnknownManager is returned when looking up an unregistered manager.
	ErrUnknownManager = errors.New("prepare: unknown manager")

	mu       sync.RWMutex
	managers = map[string]ManagerFactory{}
)

// RegisterManager adds a bot's Manager factory under name.
func RegisterManager(name string, f ManagerFactory) error {
	if name == "" || f == nil {
		return fmt.Errorf("prepare: name and factory are required")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := managers[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateManager, name)
	}
	managers[name] = f
	return nil
}

// GetManager returns the manager factory registered under name, or — when
// name is empty — the sole registered manager (erroring when none or
// several are registered).
func GetManager(name string) (ManagerFactory, error) {
	mu.RLock()
	defer mu.RUnlock()
	if name != "" {
		f, ok := managers[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s (registered: %v)", ErrUnknownManager, name, managerNamesLocked())
		}
		return f, nil
	}
	switch len(managers) {
	case 0:
		return nil, fmt.Errorf("prepare: no manager registered (import your bot's adapter package)")
	case 1:
		for _, f := range managers {
			return f, nil
		}
	}
	return nil, fmt.Errorf("prepare: fixtures_manager is required when several managers are registered (%v)", managerNamesLocked())
}

// ManagerNames lists registered manager names, sorted.
func ManagerNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	return managerNamesLocked()
}

func managerNamesLocked() []string {
	names := make([]string, 0, len(managers))
	for n := range managers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// PromptFetcher is an optional Manager capability: render the bot's system
// prompt for one seeded fixture key (used by "obatala reconcile" to analyze
// a prompt against saved report failures without re-running scenarios).
type PromptFetcher interface {
	SystemPrompt(ctx context.Context, key string) (string, error)
}
