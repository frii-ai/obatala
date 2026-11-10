package report_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/metrics"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

func sampleRun() report.RunSummary {
	return report.RunSummary{
		ReportVersion: report.CurrentVersion,
		StartedAt:     time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		Duration:      90 * time.Second,
		Meta: report.RunMeta{
			Environment:     "local",
			ContractVersion: 1,
			AppVersion:      "dev",
			PromptVersion:   "sha256:abc",
			BotModel:        "ollama/gemma4:31b",
			TestMode:        true,
			Extra:           map[string]string{"payments_driver": "fake"},
		},
		Latency: metrics.Aggregate{Turns: 3, FirstP50: time.Second, FirstP95: 2 * time.Second, FirstMax: 3 * time.Second},
		Scenarios: []report.ScenarioResult{
			{
				ID: "socks-enquiry", Name: "Socks enquiry", Fixture: "shoes-and-socks",
				Outcome: condition.OutcomePass, TurnsUsed: 2, Duration: 30 * time.Second,
				Latency:     metrics.Aggregate{Turns: 2, FirstP50: time.Second, FirstP95: 2 * time.Second, FirstMax: 2 * time.Second},
				MetCriteria: []string{"The bot sends a payment link"},
				TokenUsage:  &llm.Usage{PromptTokens: 120, CompletionTokens: 24},
				Transcript:  []string{"customer: hi", "bot: hello"},
			},
			{
				ID: "slow-bot", Outcome: condition.OutcomeFail, TurnsUsed: 1, Duration: 10 * time.Second,
				Group:            "payment",
				Reasoning:        "no link sent",
				UnmetCriteria:    []string{"The bot sends a payment link"},
				BudgetViolations: []string{"turn latency p95 5s exceeds budget 1s"},
				Latency:          metrics.Aggregate{Turns: 1, FirstP95: 5 * time.Second, FirstMax: 5 * time.Second},
			},
			{
				ID: "broken-infra", Outcome: condition.OutcomeError,
				Error: "webhook down",
			},
		},
	}
}

func TestRegisteredReportersExist(t *testing.T) {
	for _, name := range []string{"json", "junit", "md", "html"} {
		if _, err := report.Get(name); err != nil {
			t.Errorf("reporter %s not registered: %v", name, err)
		}
	}
}

func TestJSONReporter(t *testing.T) {
	dir := t.TempDir()
	factory, err := report.Get("json")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := factory(map[string]any{"path": filepath.Join(dir, "r.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Report(context.Background(), sampleRun()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "r.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got report.RunSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Scenarios) != 3 || got.Scenarios[0].ID != "socks-enquiry" || got.Latency.FirstP95 != 2*time.Second {
		t.Fatalf("round-trip = %+v", got)
	}
	if got.Meta.PromptVersion != "sha256:abc" {
		t.Fatalf("meta lost: %+v", got.Meta)
	}
	if got.ReportVersion != report.CurrentVersion {
		t.Fatalf("report_version = %d, want %d", got.ReportVersion, report.CurrentVersion)
	}
	if got.Scenarios[0].TokenUsage == nil || got.Scenarios[0].TokenUsage.PromptTokens != 120 {
		t.Fatalf("token usage lost: %+v", got.Scenarios[0].TokenUsage)
	}
}

func TestJUnitReporter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "junit.xml")
	factory, _ := report.Get("junit")
	rep, err := factory(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Report(context.Background(), sampleRun()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	xml := string(data)
	for _, want := range []string{
		`tests="3"`, `failures="1"`, `errors="1"`,
		`name="socks-enquiry"`, `name="slow-bot"`, `name="broken-infra"`,
		"<failure", "<error", "turn latency p95", "webhook down",
		"customer: hi",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("junit.xml missing %q:\n%s", want, xml)
		}
	}
}

func TestMarkdownReporter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	factory, _ := report.Get("md")
	rep, err := factory(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Report(context.Background(), sampleRun()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	for _, want := range []string{
		"# Obatala run", "sha256:abc", "3 scenarios: 1 pass, 1 fail, 1 error",
		"| socks-enquiry | pass |", "turn latency p95 5s exceeds budget 1s",
		"**Unmet criteria:**",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report.md missing %q:\n%s", want, md)
		}
	}
}

func TestHTMLReporter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.html")
	factory, err := report.Get("html")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := factory(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.Report(context.Background(), sampleRun()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{
		"<!doctype html>",
		"sha256:abc",
		"1 pass", "1 fail", "1 error",
		"socks-enquiry", "slow-bot", "broken-infra",
		"turn latency p95 5s exceeds budget 1s",
		"webhook down",
		"customer:</span> hi", "bot:</span> hello",
		"The bot sends a payment link",
		"no link sent",
		// embedded assets were spliced in:
		".scenario-head",
		"isDetailOpen",
		// grouping by suite folder (root + payment in this run):
		`data-group=""`,
		`data-group="payment"`,
		"group-head", "group-title", "updateGroups",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report.html missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"__OBATALA_",             // splice markers must be consumed
		"<script src=",           // self-contained: no external JS
		`<link rel="stylesheet"`, // self-contained: no external CSS
	} {
		if strings.Contains(html, unwanted) {
			t.Errorf("report.html must not contain %q", unwanted)
		}
	}
}

func TestHTMLReporterEmptyRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.html")
	factory, _ := report.Get("html")
	rep, err := factory(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	run := report.RunSummary{ReportVersion: report.CurrentVersion, StartedAt: time.Now()}
	if err := rep.Report(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{"0 scenarios", "No scenarios in this run", "all passed"} {
		if !strings.Contains(html, want) {
			t.Errorf("empty report.html missing %q", want)
		}
	}
}

func TestHTMLEscapesRunData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.html")
	factory, _ := report.Get("html")
	rep, err := factory(map[string]any{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	run := report.RunSummary{
		ReportVersion: report.CurrentVersion,
		StartedAt:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Scenarios: []report.ScenarioResult{{
			ID:         `xss"<>`,
			Outcome:    condition.OutcomeFail,
			Reasoning:  `<img src=x onerror=alert(1)>`,
			Transcript: []string{`customer: <script>alert(2)</script>`},
		}},
	}
	if err := rep.Report(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, unwanted := range []string{
		"<img src=x onerror=alert(1)>",
		"<script>alert(2)</script>",
		`data-id="xss"<>`,
	} {
		if strings.Contains(html, unwanted) {
			t.Errorf("unescaped run data %q in report.html", unwanted)
		}
	}
	for _, want := range []string{
		"&lt;img src=x onerror=alert(1)&gt;",
		"&lt;script&gt;alert(2)&lt;/script&gt;",
		`data-id="xss&#34;&lt;&gt;"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report.html missing escaped %q", want)
		}
	}
}

func TestTally(t *testing.T) {
	pass, fail, errs := report.Tally(sampleRun())
	if pass != 1 || fail != 1 || errs != 1 {
		t.Fatalf("tally = %d/%d/%d", pass, fail, errs)
	}
}
