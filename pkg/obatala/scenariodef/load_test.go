package scenariodef_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
)

func writeSuiteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const goodScenario = `id: socks-enquiry
name: Enquire about socks
tags: [smoke]
fixture: shoes-and-socks
customer:
  description: Buyer who wants cotton socks
  strategy: Ask for the product and a payment link
  max_turns: 5
criteria:
  success:
    - The bot sends a payment link
`

func TestLoadSuite(t *testing.T) {
	dir := t.TempDir()
	writeSuiteFile(t, dir, "b.yaml", strings.ReplaceAll(goodScenario, "socks-enquiry", "zebra-last"))
	writeSuiteFile(t, dir, "a.yaml", goodScenario)
	writeSuiteFile(t, dir, "notes.txt", "ignored")

	scenarios, err := scenariodef.Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("scenarios = %d", len(scenarios))
	}
	// Sorted by id, not filename.
	if scenarios[0].ID != "socks-enquiry" || scenarios[1].ID != "zebra-last" {
		t.Fatalf("order = %v, %v", scenarios[0].ID, scenarios[1].ID)
	}
}

func TestLoadRejectsInvalidScenario(t *testing.T) {
	dir := t.TempDir()
	writeSuiteFile(t, dir, "bad.yaml", "id: x\nfixture: m\n") // missing everything else
	_, err := scenariodef.Load(dir)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"customer.description", "criteria.success", "customer.strategy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q missing %q", err, want)
		}
	}
}

func TestLoadRejectsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	writeSuiteFile(t, dir, "one.yaml", goodScenario)
	writeSuiteFile(t, dir, "two.yaml", goodScenario)
	if _, err := scenariodef.Load(dir); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("err = %v, want duplicate id", err)
	}
}

func TestLoadEmptySuite(t *testing.T) {
	if _, err := scenariodef.Load(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no scenario files") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateTable(t *testing.T) {
	base := func() scenariodef.Scenario {
		return scenariodef.Scenario{
			ID:       "socks-enquiry",
			Fixture:  "shoes-and-socks",
			Customer: scenariodef.Customer{Description: "d", Strategy: "s"},
			Criteria: scenariodef.Criteria{Success: []string{"ok"}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*scenariodef.Scenario)
		want   string
	}{
		{"bad id", func(s *scenariodef.Scenario) { s.ID = "Bad_ID" }, "id must be"},
		{"bad fixture slug", func(s *scenariodef.Scenario) { s.Fixture = "Bad Slug" }, "must be a slug"},
		{"missing success", func(s *scenariodef.Scenario) { s.Criteria.Success = nil }, "criteria.success"},
		{"negative budget", func(s *scenariodef.Scenario) { s.Budgets.MaxTurns = -1 }, "budgets.max_turns"},
		{"negative latency budget", func(s *scenariodef.Scenario) { s.Budgets.TurnLatencyP95MS = -5 }, "turn_latency_p95_ms"},
		{"repeat without threshold", func(s *scenariodef.Scenario) { s.Repeat = 3 }, "pass_threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := base()
			tc.mutate(&s)
			err := scenariodef.Validate(s)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if err := scenariodef.Validate(base()); err != nil {
		t.Fatalf("base scenario should be valid: %v", err)
	}
}

func TestFilter(t *testing.T) {
	scenarios := []scenariodef.Scenario{
		{ID: "a", Tags: []string{"smoke"}},
		{ID: "b", Tags: []string{"payment"}},
		{ID: "c", Tags: []string{"smoke", "payment"}},
	}
	if got := scenariodef.Filter(scenarios, "smoke", ""); len(got) != 2 {
		t.Fatalf("tag filter = %v", got)
	}
	if got := scenariodef.Filter(scenarios, "", "b"); len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("id filter = %v", got)
	}
	if got := scenariodef.Filter(scenarios, "smoke", "c"); len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("combined filter = %v", got)
	}
	if got := scenariodef.Filter(scenarios, "", ""); len(got) != 3 {
		t.Fatalf("no filter = %v", got)
	}
}

func TestLoadNestedSuite(t *testing.T) {
	root := t.TempDir()
	writeSuiteFile(t, root, "root.yaml", strings.ReplaceAll(goodScenario, "socks-enquiry", "root-scenario"))
	if err := os.MkdirAll(filepath.Join(root, "security"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeSuiteFile(t, filepath.Join(root, "security"), "jailbreak.yaml",
		strings.ReplaceAll(goodScenario, "socks-enquiry", "test-jailbraking"))
	if err := os.MkdirAll(filepath.Join(root, "security", "deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeSuiteFile(t, filepath.Join(root, "security", "deep"), "leak.yaml",
		strings.ReplaceAll(goodScenario, "socks-enquiry", "ensure-no-unguarded-info-leak"))

	scenarios, err := scenariodef.Load(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(scenarios) != 3 {
		t.Fatalf("scenarios = %d, want 3 (recursive walk)", len(scenarios))
	}
	// Sorted by id regardless of directory.
	want := []string{"ensure-no-unguarded-info-leak", "root-scenario", "test-jailbraking"}
	wantGroup := []string{"security/deep", "", "security"}
	for i, id := range want {
		if scenarios[i].ID != id {
			t.Fatalf("scenarios[%d].ID = %q, want %q", i, scenarios[i].ID, id)
		}
		if scenarios[i].Group != wantGroup[i] {
			t.Fatalf("scenarios[%d].Group = %q, want %q", i, scenarios[i].Group, wantGroup[i])
		}
	}
}

func TestLoadRejectsDuplicateIDsAcrossDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "security"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeSuiteFile(t, root, "a.yaml", goodScenario)
	writeSuiteFile(t, filepath.Join(root, "security"), "b.yaml", goodScenario)
	if _, err := scenariodef.Load(root); err == nil || !strings.Contains(err.Error(), "duplicate id") {
		t.Fatalf("err = %v, want duplicate id across directories", err)
	}
}
