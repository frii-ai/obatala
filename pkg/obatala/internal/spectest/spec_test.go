// Package spectest checks that the engine still matches spec/: scenario
// files against scenario.schema.json, and real JSON reports against
// report.schema.json. (The verdict schema is checked in package engine.)
package spectest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/frii-ai/obatala/pkg/obatala"
	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

const repoRoot = "../../../.."

func schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	s, err := c.Compile(filepath.Join(repoRoot, "spec", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// asJSON converts decoded YAML or raw JSON bytes into the value model the
// validator expects.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func yamlDoc(t *testing.T, src string) any {
	t.Helper()
	var doc any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return asJSON(t, doc)
}

// TestRepoScenariosMatchSchema validates every YAML file under any
// scenarios/ directory in the repo (examples, conformance, suites).
func TestRepoScenariosMatchSchema(t *testing.T) {
	sch := schema(t, "scenario.schema.json")
	n := 0
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		ext := filepath.Ext(path)
		if d.IsDir() || (ext != ".yaml" && ext != ".yml") || !strings.Contains(filepath.ToSlash(path), "/scenarios/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := sch.Validate(yamlDoc(t, string(data))); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no scenario files found")
	}
}

// TestSchemaAgreesWithValidate: the JSON Schema and scenariodef.Validate
// accept and reject the same documents.
func TestSchemaAgreesWithValidate(t *testing.T) {
	sch := schema(t, "scenario.schema.json")
	base := "id: ok-id\ncustomer:\n  description: d\n  strategy: s\n  max_turns: 2\ncriteria:\n  success: [the bot greets]\n"
	cases := map[string]string{
		"valid":               base,
		"valid with extras":   base + "name: N\ntags: [a]\nfixture: demo-store\nbudgets:\n  max_turns: 3\n  turn_latency_p95_ms: 100\n",
		"bad id":              strings.Replace(base, "ok-id", "Bad_ID", 1),
		"one-char id":         strings.Replace(base, "ok-id", "a", 1),
		"short fixture":       base + "fixture: ab\n",
		"no description":      strings.Replace(base, "  description: d\n", "", 1),
		"empty strategy":      strings.Replace(base, "strategy: s", `strategy: ""`, 1),
		"negative turns":      strings.Replace(base, "max_turns: 2", "max_turns: -1", 1),
		"no success":          strings.Replace(base, "success: [the bot greets]", "success: []", 1),
		"blank success":       strings.Replace(base, "success: [the bot greets]", `success: ["  "]`, 1),
		"negative budget":     base + "budgets:\n  turn_latency_p95_ms: -5\n",
		"repeat no threshold": base + "repeat: 3\n",
		"repeat threshold":    base + "repeat: 3\npass_threshold: 2\n",
	}
	for name, src := range cases {
		var s scenariodef.Scenario
		if err := yaml.Unmarshal([]byte(src), &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		goErr := scenariodef.Validate(s)
		schemaErr := sch.Validate(yamlDoc(t, src))
		if (goErr == nil) != (schemaErr == nil) {
			t.Errorf("%s: Validate=%v schema=%v", name, goErr, schemaErr)
		}
	}
}

// --- report ---

type stubManager struct{ n int }

type noKeys struct{}

func (noKeys) Keys() []string { return nil }

func (m *stubManager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{Environment: "spec", TestMode: true, Extra: map[string]string{"k": "v"}}, nil
}
func (m *stubManager) LoadFixtures(string) (prepare.FixtureSet, error) { return noKeys{}, nil }
func (m *stubManager) Seed(context.Context, string, prepare.FixtureSet) (map[string]string, error) {
	return nil, nil
}
func (m *stubManager) NewIdentity() (string, error)                       { m.n++; return "id-" + string(rune('a'+m.n)), nil }
func (m *stubManager) Teardown(context.Context, []string, []string) error { return nil }

func init() {
	// Transport behavior comes from settings: "mode" error|slow|ok.
	_ = transport.Register("spectest", func(s map[string]any) (transport.Transport, error) {
		tr := fakes.NewTransport([]string{"Plans start at $10.", "Anything else?"}, []string{"Bye."})
		switch s["mode"] {
		case "error":
			tr.SendErr = errors.New("bot down")
		case "slow":
			tr.ReplyDelay = 30 * time.Millisecond
		}
		return tr, nil
	})
	_ = prepare.RegisterManager("spectest", func(config.Config) (prepare.Manager, error) { return &stubManager{}, nil })
	_ = llm.Register("spectest-customer", func(map[string]any) (llm.Completion, error) {
		return fakes.NewCustomer("how much", "thanks"), nil
	})
	_ = llm.Register("spectest-judge", func(s map[string]any) (llm.Completion, error) {
		verdict, _ := s["model"].(string)
		return fakes.NewVerdict(verdict, "scripted"), nil
	})
}

func TestReportsMatchSchema(t *testing.T) {
	sch := schema(t, "report.schema.json")
	for _, tc := range []struct {
		name, mode, verdict, budget string
		want                        string
		code                        int
	}{
		{"pass", "ok", "success", "", "pass", 0},
		{"fail", "ok", "failure", "", "fail", 1},
		{"inconclusive", "ok", "inconclusive", "", "fail", 1},
		{"error", "error", "success", "", "error", 2},
		{"budget", "slow", "success", "budgets:\n  turn_latency_p95_ms: 1\n", "fail", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			must(t, os.MkdirAll(filepath.Join(root, "scenarios", "group-a"), 0o750))
			must(t, os.WriteFile(filepath.Join(root, "scenarios", "group-a", "s.yaml"), []byte(
				"id: spec-scenario\nname: Spec\ntags: [smoke]\ncustomer:\n  description: d\n  strategy: s\n  max_turns: 2\ncriteria:\n  success: [the bot states a price]\n  failure: [the bot is rude]\n"+tc.budget), 0o600))
			must(t, os.WriteFile(filepath.Join(root, "obatala.yaml"), []byte(
				"transport:\n  name: spectest\n  settings:\n    mode: "+tc.mode+
					"\nfixtures_manager: spectest\ncustomer:\n  provider: spectest-customer\n  model: x\njudge:\n  provider: spectest-judge\n  model: "+tc.verdict+"\nretry_attempts: 1\n"), 0o600))
			t.Chdir(root)
			_, code, _ := obatala.Execute(context.Background(), obatala.Options{Reports: []string{"json"}})
			if code != tc.code {
				t.Fatalf("exit = %d, want %d", code, tc.code)
			}
			data, err := os.ReadFile(filepath.Join(root, "reports", "report.json"))
			must(t, err)
			doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
			must(t, err)
			if err := sch.Validate(doc); err != nil {
				t.Fatalf("report does not match schema: %v\n%s", err, data)
			}
			var r report.RunSummary
			must(t, json.Unmarshal(data, &r))
			if got := string(r.Scenarios[0].Outcome); got != tc.want {
				t.Fatalf("outcome = %s, want %s", got, tc.want)
			}
			if tc.name == "budget" && len(r.Scenarios[0].BudgetViolations) == 0 {
				t.Fatal("expected a budget violation")
			}
			if r.Scenarios[0].Group != "group-a" {
				t.Fatalf("group = %q", r.Scenarios[0].Group)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
