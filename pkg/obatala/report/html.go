package report

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/condition"
)

//go:embed templates/report.html.tmpl templates/report.css templates/report.js
var htmlFS embed.FS

func init() {
	_ = Register("html", newHTML)
}

func newHTML(settings map[string]any) (Reporter, error) {
	return &fileReporter{
		path:   settingPath(settings, "reports/report.html"),
		render: renderHTML,
	}, nil
}

var (
	htmlTmplOnce sync.Once
	htmlTmpl     *template.Template
	htmlTmplErr  error
)

func htmlTemplate() (*template.Template, error) {
	htmlTmplOnce.Do(func() {
		htmlTmpl, htmlTmplErr = template.ParseFS(htmlFS, "templates/report.html.tmpl")
	})
	return htmlTmpl, htmlTmplErr
}

// renderHTML writes one self-contained HTML file: markup comes from the
// embedded template (html/template escapes all run data), and the embedded
// CSS/JS are spliced in behind literal markers afterwards so their content
// never passes through contextual escaping. The markers are bare
// identifiers, not comments — html/template strips comments inside
// <style>/<script> but passes identifiers through untouched.
func renderHTML(run RunSummary) ([]byte, error) {
	t, err := htmlTemplate()
	if err != nil {
		return nil, fmt.Errorf("report: parse html template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, buildHTMLReport(run)); err != nil {
		return nil, fmt.Errorf("report: render html: %w", err)
	}
	out := buf.Bytes()
	for _, part := range []struct{ marker, file string }{
		{"__OBATALA_CSS__", "templates/report.css"},
		{"__OBATALA_JS__", "templates/report.js"},
	} {
		raw, err := htmlFS.ReadFile(part.file)
		if err != nil {
			return nil, fmt.Errorf("report: read %s: %w", part.file, err)
		}
		if !bytes.Contains(out, []byte(part.marker)) {
			return nil, fmt.Errorf("report: html template missing %s", part.marker)
		}
		out = bytes.Replace(out, []byte(part.marker), raw, 1)
	}
	return out, nil
}

// htmlReport is the view model renderHTML builds from a RunSummary.
type htmlReport struct {
	Run            RunSummary
	Started        string
	Finished       string
	Duration       string
	ReportVersion  int
	Pass, Fail     int
	Errs           int
	VerdictClass   string // pass | fail | error
	VerdictText    string
	ViolationCount int
	LatencyAny     bool
	LatencyBars    []htmlBar
	Tokens         htmlTokens
	Tags           []string
	Extra          []htmlKV
	Scenarios      []htmlScenario
	Groups         []htmlGroup
}

type htmlBar struct {
	Label string
	Value string
	Pct   int
}

type htmlTokens struct {
	Any        bool
	Prompt     int
	Completion int
}

type htmlKV struct{ K, V string }

// htmlGroup is one suite folder's scenarios plus header display fields.
// The suite root (Key == "") renders first under the title "scenarios";
// folders follow alphabetically.
type htmlGroup struct {
	Key       string
	Title     string
	Pass      int
	Fail      int
	Errs      int
	Scenarios []htmlScenario
}

// htmlScenario is one ScenarioResult plus display-only fields (the
// template never formats durations or computes percentages itself).
type htmlScenario struct {
	ScenarioResult
	Notes           string
	NotesKind       string // error | violation | reasoning
	NotesLabel      string
	OutcomeRank     int // pass 0, fail 1, error 2 (sort key)
	DurationText    string
	P50Text         string
	P95Text         string
	MaxText         string
	DurationNs      int64
	LatencyNs       int64
	HasLatency      bool
	TagsText        string
	TurnBars        []htmlTurnBar
	TranscriptLines []htmlLine
}

type htmlTurnBar struct {
	N     int
	Value string
	Pct   int
}

type htmlLine struct {
	Speaker string // "customer" | "bot" | ""
	Text    string
}

func buildHTMLReport(run RunSummary) htmlReport {
	pass, fail, errs := Tally(run)
	h := htmlReport{
		Run:           run,
		Started:       run.StartedAt.UTC().Format(time.RFC3339),
		Finished:      run.StartedAt.Add(run.Duration).UTC().Format(time.RFC3339),
		Duration:      fmtDur(run.Duration),
		ReportVersion: run.ReportVersion,
		Pass:          pass,
		Fail:          fail,
		Errs:          errs,
		LatencyAny:    run.Latency.FirstMax > 0,
	}
	switch {
	case errs > 0:
		h.VerdictClass, h.VerdictText = "error", "errors"
	case fail > 0:
		h.VerdictClass, h.VerdictText = "fail", "failures"
	default:
		h.VerdictClass, h.VerdictText = "pass", "all passed"
	}
	h.LatencyBars = []htmlBar{
		{Label: "p50", Value: fmtDur(run.Latency.FirstP50), Pct: percentOf(run.Latency.FirstP50, run.Latency.FirstMax)},
		{Label: "p95", Value: fmtDur(run.Latency.FirstP95), Pct: percentOf(run.Latency.FirstP95, run.Latency.FirstMax)},
		{Label: "max", Value: fmtDur(run.Latency.FirstMax), Pct: percentOf(run.Latency.FirstMax, run.Latency.FirstMax)},
	}

	tags := map[string]bool{}
	for _, s := range run.Scenarios {
		h.ViolationCount += len(s.BudgetViolations)
		if s.TokenUsage != nil {
			h.Tokens.Any = true
			h.Tokens.Prompt += s.TokenUsage.PromptTokens
			h.Tokens.Completion += s.TokenUsage.CompletionTokens
		}
		for _, t := range s.Tags {
			tags[t] = true
		}
		h.Scenarios = append(h.Scenarios, buildHTMLScenario(s))
	}
	for t := range tags {
		h.Tags = append(h.Tags, t)
	}
	sort.Strings(h.Tags)
	for k, v := range run.Meta.Extra {
		h.Extra = append(h.Extra, htmlKV{K: k, V: v})
	}
	sort.Slice(h.Extra, func(i, j int) bool { return h.Extra[i].K < h.Extra[j].K })
	h.Groups = groupScenarios(h.Scenarios)
	return h
}

// groupScenarios partitions scenarios by their suite folder, preserving
// incoming run order within each group: the suite root first, then
// folders alphabetically.
func groupScenarios(scenarios []htmlScenario) []htmlGroup {
	var groups []htmlGroup
	byKey := map[string]int{}
	for _, s := range scenarios {
		idx, ok := byKey[s.Group]
		if !ok {
			idx = len(groups)
			byKey[s.Group] = idx
			groups = append(groups, htmlGroup{Key: s.Group, Title: groupTitle(s.Group)})
		}
		g := &groups[idx]
		g.Scenarios = append(g.Scenarios, s)
		switch s.Outcome {
		case condition.OutcomePass:
			g.Pass++
		case condition.OutcomeFail:
			g.Fail++
		default:
			g.Errs++
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i].Key, groups[j].Key
		if a == b {
			return false
		}
		if a == "" || b == "" {
			return a == ""
		}
		return a < b
	})
	return groups
}

func groupTitle(key string) string {
	if key == "" {
		return "scenarios"
	}
	return key
}

func buildHTMLScenario(s ScenarioResult) htmlScenario {
	g := htmlScenario{
		ScenarioResult: s,
		OutcomeRank:    outcomeRank(s.Outcome),
		DurationText:   fmtDur(s.Duration),
		P50Text:        fmtDur(s.Latency.FirstP50),
		P95Text:        fmtDur(s.Latency.FirstP95),
		MaxText:        fmtDur(s.Latency.FirstMax),
		DurationNs:     int64(s.Duration),
		LatencyNs:      int64(s.Latency.FirstP95),
		HasLatency:     s.Latency.Turns > 0,
		TagsText:       strings.Join(s.Tags, " "),
	}
	// Same notes precedence as the markdown reporter.
	g.Notes = s.Error
	g.NotesKind, g.NotesLabel = "error", "error"
	if g.Notes == "" && len(s.BudgetViolations) > 0 {
		g.Notes = strings.Join(s.BudgetViolations, "; ")
		g.NotesKind, g.NotesLabel = "violation", "budget"
	}
	if g.Notes == "" {
		g.Notes = s.Reasoning
		g.NotesKind, g.NotesLabel = "reasoning", "judge"
	}

	var tmax time.Duration
	for _, t := range s.Turns {
		if lat := t.FirstReplyLatency(); lat > tmax {
			tmax = lat
		}
	}
	for _, t := range s.Turns {
		lat := t.FirstReplyLatency()
		g.TurnBars = append(g.TurnBars, htmlTurnBar{
			N: t.Turn, Value: fmtDur(lat), Pct: percentOf(lat, tmax),
		})
	}
	g.TranscriptLines = transcriptLines(s)
	return g
}

// transcriptLines prefers the full transcript and falls back to the
// head/tail excerpt (mirrors the markdown reporter's rendering).
func transcriptLines(s ScenarioResult) []htmlLine {
	src := s.Transcript
	if len(src) == 0 && (len(s.TranscriptHead) > 0 || len(s.TranscriptTail) > 0) {
		src = append([]string{}, s.TranscriptHead...)
		if len(s.TranscriptTail) > 0 {
			src = append(src, "…")
			src = append(src, s.TranscriptTail...)
		}
	}
	lines := make([]htmlLine, 0, len(src))
	for _, l := range src {
		speaker, text := splitSpeaker(l)
		lines = append(lines, htmlLine{Speaker: speaker, Text: text})
	}
	return lines
}

// splitSpeaker splits a "customer: …" / "bot: …" transcript line.
func splitSpeaker(line string) (speaker, text string) {
	for _, p := range []string{"customer:", "bot:"} {
		if strings.HasPrefix(line, p) {
			return strings.TrimSuffix(p, ":"), strings.TrimSpace(line[len(p):])
		}
	}
	return "", line
}

func outcomeRank(o condition.Outcome) int {
	switch o {
	case condition.OutcomePass:
		return 0
	case condition.OutcomeFail:
		return 1
	default:
		return 2
	}
}

func fmtDur(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// percentOf scales v against max as 0-100 (any non-zero value shows at
// least 1% so tiny bars stay visible; zero stays zero).
func percentOf(v, max time.Duration) int {
	if v <= 0 || max <= 0 {
		return 0
	}
	p := int(v * 100 / max)
	if p < 1 {
		p = 1
	}
	if p > 100 {
		p = 100
	}
	return p
}
