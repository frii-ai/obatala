package prepare_test

import (
	"context"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

type stubFixtureSet struct{ keys []string }

func (s stubFixtureSet) Keys() []string { return s.keys }

type stubManager struct{}

func (stubManager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{}, nil
}

func (stubManager) LoadFixtures(string) (prepare.FixtureSet, error) { return stubFixtureSet{}, nil }

func (stubManager) Seed(context.Context, string, prepare.FixtureSet) (map[string]string, error) {
	return nil, nil
}

func (stubManager) NewIdentity() (string, error) { return "id-1", nil }

func (stubManager) Teardown(context.Context, []string, []string) error { return nil }

var _ prepare.Manager = stubManager{}

func stubFactory(config.Config) (prepare.Manager, error) { return stubManager{}, nil }

func TestManagerRegistry(t *testing.T) {
	const name = "prepare-test-manager"
	if err := prepare.RegisterManager(name, stubFactory); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := prepare.RegisterManager(name, stubFactory); err == nil {
		t.Fatal("expected duplicate error")
	}
	f, err := prepare.GetManager(name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := f(config.Config{}); err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, err := prepare.GetManager("nope"); err == nil {
		t.Fatal("expected unknown manager error")
	}
	names := prepare.ManagerNames()
	found := false
	for _, n := range names {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("names = %v", names)
	}
}

func TestGetManagerEmptyNamePicksSoleRegistered(t *testing.T) {
	// The registry is package-global; the previous test registered one
	// manager, so the empty-name lookup must succeed with exactly one.
	f, err := prepare.GetManager("")
	if err != nil {
		// Another test may have registered more; the error must then
		// demand an explicit name.
		if !strings.Contains(err.Error(), "fixtures_manager") {
			t.Fatalf("err = %v", err)
		}
		return
	}
	if _, err := f(config.Config{}); err != nil {
		t.Fatalf("factory: %v", err)
	}
}
