package reconcile_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/reconcile"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

type stubFixtureSet struct{}

func (stubFixtureSet) Keys() []string { return []string{"ibe-outdoor"} }

type stubManager struct {
	seeded    []string
	torn      []string
	seedErr   error
	promptErr error
}

func (m *stubManager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{}, nil
}

func (m *stubManager) LoadFixtures(string) (prepare.FixtureSet, error) {
	return stubFixtureSet{}, nil
}

func (m *stubManager) Seed(_ context.Context, key string, _ prepare.FixtureSet) (map[string]string, error) {
	if m.seedErr != nil {
		return nil, m.seedErr
	}
	m.seeded = append(m.seeded, key)
	return nil, nil
}

func (m *stubManager) NewIdentity() (string, error) { return "9991234567890", nil }

func (m *stubManager) Teardown(_ context.Context, _ []string, keys []string) error {
	m.torn = append(m.torn, keys...)
	return nil
}

func (m *stubManager) SystemPrompt(_ context.Context, _ string) (string, error) {
	if m.promptErr != nil {
		return "", m.promptErr
	}
	return "Rule 1: greet the customer.", nil
}

var _ prepare.Manager = (*stubManager)(nil)
var _ prepare.PromptFetcher = (*stubManager)(nil)

// fakeLLM returns queued reply strings (the last one repeats).
type fakeLLM struct {
	replies []string
	calls   int
	err     error
}

func (f *fakeLLM) Complete(_ context.Context, _ llm.Request) (llm.Response, error) {
	f.calls++
	if f.err != nil {
		return llm.Response{}, f.err
	}
	content := f.replies[0]
	if f.calls <= len(f.replies) {
		content = f.replies[f.calls-1]
	}
	return llm.Response{Choices: []llm.Choice{{Content: content}}}, nil
}

var _ llm.Completion = (*fakeLLM)(nil)

const goodReply = `{"diagnosis":"Bot skipped in-stock alternatives before offering sourcing.","suggestions":[{"title":"Suggest alternatives first","section":"Unavailable Products","current_excerpt":"Rule 1: greet the customer.","proposed_text":"Rule 1: greet the customer. Rule 2: if a product is out of stock, suggest two or three in-stock alternatives before offering a sourcing request.","targets_criteria":["The bot first suggests in-stock alternatives"],"confidence":"high"}]}`

func writeReport(t *testing.T, scenarios ...report.ScenarioResult) string {
	t.Helper()
	run := report.RunSummary{
		ReportVersion: report.CurrentVersion,
		StartedAt:     time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC),
		Meta:          report.RunMeta{Environment: "test", BotModel: "gemma4:31b", PromptVersion: "v7"},
		Scenarios:     scenarios,
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunHappyPath(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID:            "source-request",
		Name:          "Sourcing request flow",
		Fixture:       "ibe-outdoor",
		Outcome:       condition.OutcomeFail,
		Reasoning:     "Bot offered sourcing without alternatives.",
		MetCriteria:   []string{"Bot greets the customer"},
		UnmetCriteria: []string{"The bot first suggests in-stock alternatives"},
		Transcript:    []string{"customer: do you have hiking boots?", "bot: we can order them for you"},
	})
	mgr := &stubManager{}
	llmFake := &fakeLLM{replies: []string{goodReply}}
	outPath := filepath.Join(t.TempDir(), "reconcile.html")
	var stdout, stderr bytes.Buffer

	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager:      mgr,
		Completion:   llmFake,
		RetryBackoff: time.Millisecond,
	}, reconcile.Options{
		ReportPath:  reportPath,
		FixturesDir: "fixtures/shops",
		OutPath:     outPath,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(mgr.seeded) != 1 || mgr.seeded[0] != "ibe-outdoor" {
		t.Fatalf("seeded = %v", mgr.seeded)
	}
	if len(mgr.torn) != 1 || mgr.torn[0] != "ibe-outdoor" {
		t.Fatalf("torn = %v", mgr.torn)
	}
	if llmFake.calls != 1 {
		t.Fatalf("llm calls = %d", llmFake.calls)
	}

	console := stdout.String()
	for _, want := range []string{
		"source-request", "ibe-outdoor", "Suggest alternatives first",
		"diagnosis:", "Bot skipped in-stock alternatives",
		"The bot first suggests in-stock alternatives",
	} {
		if !strings.Contains(console, want) {
			t.Fatalf("console missing %q:\n%s", want, console)
		}
	}

	html, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	for _, want := range []string{
		"source-request", "Suggest alternatives first",
		"Rule 1: greet the customer. Rule 2:", "high", "gemma4:31b",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("html missing %q", want)
		}
	}
	if strings.Contains(string(html), "__OBATALA_CSS__") {
		t.Fatal("splice marker left in html")
	}
	if !strings.Contains(stderr.String(), outPath) {
		t.Fatalf("stderr missing output path: %s", stderr.String())
	}
}

func TestRunRetriesOnBadJSON(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "source-request", Fixture: "ibe-outdoor", Outcome: condition.OutcomeFail,
	})
	mgr := &stubManager{}
	llmFake := &fakeLLM{replies: []string{"not json", goodReply}}
	outPath := filepath.Join(t.TempDir(), "reconcile.html")

	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager: mgr, Completion: llmFake, RetryBackoff: time.Millisecond,
	}, reconcile.Options{ReportPath: reportPath, OutPath: outPath})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if llmFake.calls != 2 {
		t.Fatalf("llm calls = %d, want 2", llmFake.calls)
	}
}

func TestRunAllPassExitsOne(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "socks-enquiry", Outcome: condition.OutcomePass,
	})
	mgr := &stubManager{}
	llmFake := &fakeLLM{replies: []string{goodReply}}
	var stdout bytes.Buffer

	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager: mgr, Completion: llmFake,
	}, reconcile.Options{ReportPath: reportPath, Stdout: &stdout})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if llmFake.calls != 0 || len(mgr.seeded) != 0 {
		t.Fatalf("must not seed or call llm: calls=%d seeded=%v", llmFake.calls, mgr.seeded)
	}
	if !strings.Contains(stdout.String(), "nothing to reconcile") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunPassedIDExitsOne(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "socks-enquiry", Outcome: condition.OutcomePass,
	})
	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager: &stubManager{}, Completion: &fakeLLM{replies: []string{goodReply}},
	}, reconcile.Options{ReportPath: reportPath, ID: "socks-enquiry"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
}

func TestRunMissingIDExitsTwo(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "source-request", Outcome: condition.OutcomeFail,
	})
	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager: &stubManager{}, Completion: &fakeLLM{replies: []string{goodReply}},
	}, reconcile.Options{ReportPath: reportPath, ID: "ghost"})
	if err == nil || code != 2 {
		t.Fatalf("code=%d err=%v, want 2 with error", code, err)
	}
}

func TestRunSeedFailureExitsTwo(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "source-request", Fixture: "ibe-outdoor", Outcome: condition.OutcomeFail,
	})
	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager:    &stubManager{seedErr: context.DeadlineExceeded},
		Completion: &fakeLLM{replies: []string{goodReply}},
	}, reconcile.Options{ReportPath: reportPath})
	if err == nil || code != 2 {
		t.Fatalf("code=%d err=%v, want 2 with error", code, err)
	}
}

func TestRunTeardownRunsOnLLMFailure(t *testing.T) {
	reportPath := writeReport(t, report.ScenarioResult{
		ID: "source-request", Fixture: "ibe-outdoor", Outcome: condition.OutcomeFail,
	})
	mgr := &stubManager{}
	llmFake := &fakeLLM{err: context.DeadlineExceeded}

	code, err := reconcile.Run(context.Background(), reconcile.Deps{
		Manager: mgr, Completion: llmFake, RetryAttempts: 2, RetryBackoff: time.Millisecond,
	}, reconcile.Options{ReportPath: reportPath})
	if err == nil || code != 2 {
		t.Fatalf("code=%d err=%v, want 2 with error", code, err)
	}
	if len(mgr.torn) != 1 || mgr.torn[0] != "ibe-outdoor" {
		t.Fatalf("torn = %v", mgr.torn)
	}
}
