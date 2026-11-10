package report_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

type stubReporter struct{ runs []report.RunSummary }

func (r *stubReporter) Report(_ context.Context, run report.RunSummary) error {
	r.runs = append(r.runs, run)
	return nil
}

func TestRegisterGetAndReport(t *testing.T) {
	var rep *stubReporter
	factory := func(map[string]any) (report.Reporter, error) {
		rep = &stubReporter{}
		return rep, nil
	}
	if err := report.Register("stub", factory); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := report.Get("stub")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	r, err := got(nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	summary := report.RunSummary{
		StartedAt: time.Now(),
		Meta:      report.RunMeta{PromptVersion: "sha256:test", ContractVersion: 1},
		Scenarios: []report.ScenarioResult{{ID: "s1", Outcome: condition.OutcomePass}},
	}
	if err := r.Report(context.Background(), summary); err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.runs) != 1 || rep.runs[0].Meta.PromptVersion != "sha256:test" {
		t.Fatalf("runs = %+v", rep.runs)
	}
}

func TestRegisterDuplicate(t *testing.T) {
	factory := func(map[string]any) (report.Reporter, error) { return &stubReporter{}, nil }
	_ = report.Register("dup-check", factory)
	err := report.Register("dup-check", factory)
	if !errors.Is(err, report.ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := report.Get("missing")
	if !errors.Is(err, report.ErrUnknown) {
		t.Fatalf("err = %v, want ErrUnknown", err)
	}
}

func TestLoadRun(t *testing.T) {
	orig := report.RunSummary{
		ReportVersion: report.CurrentVersion,
		StartedAt:     time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC),
		Meta:          report.RunMeta{Environment: "test", BotModel: "gemma4:31b"},
		Scenarios: []report.ScenarioResult{{
			ID:      "source-request",
			Fixture: "ibe-outdoor",
			Outcome: condition.OutcomeFail,
		}},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	run, err := report.LoadRun(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if run.ReportVersion != report.CurrentVersion || len(run.Scenarios) != 1 || run.Scenarios[0].ID != "source-request" {
		t.Fatalf("run = %+v", run)
	}
}

func TestLoadRunErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := report.LoadRun(bad); err == nil {
		t.Fatal("expected parse error")
	}

	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"report_version": 999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := report.LoadRun(future); err == nil {
		t.Fatal("expected newer-version error")
	}

	if _, err := report.LoadRun(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("expected missing-file error")
	}
}

// TestLoadRunReadsV2 keeps older reports usable for reconcile: v2 wrote
// turn timings with PascalCase keys, which v3 drops on load.
func TestLoadRunReadsV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.json")
	v2 := `{"report_version": 2, "scenarios": [{"id": "old", "outcome": "fail",
		"reasoning": "unmet", "transcript": ["customer: hi", "bot: hello"],
		"turns": [{"Turn": 1, "SentAt": "2026-01-02T15:04:05Z", "ReplyCount": 1}]}]}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := report.LoadRun(path)
	if err != nil {
		t.Fatalf("load v2: %v", err)
	}
	s := run.Scenarios[0]
	if s.ID != "old" || s.Reasoning != "unmet" || len(s.Transcript) != 2 {
		t.Fatalf("scenario = %+v", s)
	}
}

func TestTurnAndCheckKeysAreSnakeCase(t *testing.T) {
	data, err := json.Marshal(report.ScenarioResult{
		Turns:  []metrics.TurnTiming{{Turn: 1, ReplyCount: 2}},
		Checks: []condition.CheckResult{{Name: "n", Passed: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"turn":1`, `"sent_at"`, `"first_reply_at"`, `"last_reply_at"`, `"reply_count":2`, `"name":"n"`, `"passed":true`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("missing %s in %s", key, data)
		}
	}
}
