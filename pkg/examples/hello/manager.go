package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

func init() {
	_ = prepare.RegisterManager("hello", func(config.Config) (prepare.Manager, error) {
		return &memoryManager{}, nil
	})
}

// memoryManager is an in-memory control plane: it parses the fixture
// directory as opaque files (keys are file stems), seeds nothing, and
// hands out counter identities. A real adapter would call its backend's
// control API here instead.
type memoryManager struct {
	mu   sync.Mutex
	next int
}

// demoSet is the example's FixtureSet: keys are fixture file stems; the
// YAML body is adapter-defined and ignored here.
type demoSet struct{ keys []string }

func (d demoSet) Keys() []string { return d.keys }

func (m *memoryManager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{Environment: "example", BotModel: "hello-bot", TestMode: true}, nil
}

func (m *memoryManager) LoadFixtures(dir string) (prepare.FixtureSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("hello: read fixtures: %w", err)
	}
	set := demoSet{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, err := os.ReadFile(path); err != nil {
			return nil, fmt.Errorf("hello: read fixture %s: %w", path, err)
		}
		set.keys = append(set.keys, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return set, nil
}

func (m *memoryManager) Seed(_ context.Context, key string, _ prepare.FixtureSet) (map[string]string, error) {
	return map[string]string{"greeting": "hello from " + key}, nil
}

func (m *memoryManager) NewIdentity() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	return fmt.Sprintf("user-%d", m.next), nil
}

func (m *memoryManager) Teardown(context.Context, []string, []string) error { return nil }
