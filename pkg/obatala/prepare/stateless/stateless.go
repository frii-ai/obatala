// Package stateless is the manager for bots with no test data to seed,
// registered as "none". It loads no fixtures (scenarios then omit the
// fixture key), seeds and tears down nothing, and hands out random
// identities so each scenario still gets its own conversation.
//
//	fixtures_manager: none
package stateless

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

// Name is the manager registry name.
const Name = "none"

func init() {
	_ = prepare.RegisterManager(Name, func(config.Config) (prepare.Manager, error) {
		return Manager{}, nil
	})
}

// Manager implements prepare.Manager with no backend.
type Manager struct{}

type emptySet struct{}

func (emptySet) Keys() []string { return nil }

// Preflight has nothing to check; the run is marked as test mode.
func (Manager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{Environment: "stateless", TestMode: true}, nil
}

// LoadFixtures ignores dir and returns an empty set.
func (Manager) LoadFixtures(string) (prepare.FixtureSet, error) { return emptySet{}, nil }

// Seed is never called with an empty fixture set.
func (Manager) Seed(_ context.Context, key string, _ prepare.FixtureSet) (map[string]string, error) {
	return nil, fmt.Errorf("none: fixture %q requested but the none manager has no fixtures", key)
}

// NewIdentity returns "obatala-" plus 12 random hex characters.
func (Manager) NewIdentity() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("none: identity: %w", err)
	}
	return "obatala-" + hex.EncodeToString(b), nil
}

// Teardown has nothing to remove.
func (Manager) Teardown(context.Context, []string, []string) error { return nil }
