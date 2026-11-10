package reconcile

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/frii-ai/obatala/pkg/obatala/report"
)

//go:embed templates/reconcile.html.tmpl templates/reconcile.css
var tmplFS embed.FS

// printf writes to a CLI/report stream, ignoring write errors (output
// failures surface via exit codes, as elsewhere in the codebase).
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// writeConsole prints the suggestions as a readable markdown-ish block.
func writeConsole(w io.Writer, results []Result) {
	total := 0
	for _, r := range results {
		total += len(r.Suggestions)
	}
	printf(w, "\nreconcile: %d failed scenario(s), %d prompt-change suggestion(s)\n", len(results), total)
	for _, r := range results {
		printf(w, "\n## %s", r.ScenarioID)
		if r.Name != "" {
			printf(w, " — %s", r.Name)
		}
		printf(w, " (fixture %s)\n", r.Fixture)
		if len(r.UnmetCriteria) > 0 {
			printf(w, "unmet criteria:\n")
			for _, c := range r.UnmetCriteria {
				printf(w, "  - %s\n", c)
			}
		}
		printf(w, "diagnosis: %s\n", r.Diagnosis)
		if len(r.Suggestions) == 0 {
			printf(w, "no prompt-change suggestions.\n")
			continue
		}
		printf(w, "suggestions:\n")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for i, s := range r.Suggestions {
			printf(tw, "  %d. [%s] %s\t(section: %s)\n", i+1, strings.ToUpper(s.Confidence), s.Title, orDash(s.Section))
			if s.CurrentExcerpt != "" {
				printf(tw, "     current:\t%s\n", oneLine(s.CurrentExcerpt))
			}
			printf(tw, "     proposed:\t%s\n", oneLine(s.ProposedText))
			if len(s.TargetsCriteria) > 0 {
				printf(tw, "     targets:\t%s\n", strings.Join(s.TargetsCriteria, "; "))
			}
		}
		_ = tw.Flush()
	}
	printf(w, "\nApply the edits to your system prompt, then re-run the failed scenarios with `obatala run -id <scenario>`.\n")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	const max = 200
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

// writeHTML renders the suggestions report (styled like report.html).
func writeHTML(path, source string, run report.RunSummary, results []Result) error {
	tmpl, err := template.New("reconcile").Funcs(template.FuncMap{
		"totalSuggestions": func(results []Result) int {
			total := 0
			for _, r := range results {
				total += len(r.Suggestions)
			}
			return total
		},
		"orDash": orDash,
	}).ParseFS(tmplFS, "templates/reconcile.html.tmpl")
	if err != nil {
		return fmt.Errorf("reconcile: parse template: %w", err)
	}
	type view struct {
		Source      string
		Environment string
		Model       string
		StartedAt   string
		Results     []Result
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "reconcile.html.tmpl", view{
		Source:      source,
		Environment: run.Meta.Environment,
		Model:       run.Meta.BotModel,
		StartedAt:   run.StartedAt.Format("2006-01-02 15:04:05 MST"),
		Results:     results,
	})
	if err != nil {
		return fmt.Errorf("reconcile: render template: %w", err)
	}
	css, err := tmplFS.ReadFile("templates/reconcile.css")
	if err != nil {
		return fmt.Errorf("reconcile: read css: %w", err)
	}
	html := strings.Replace(buf.String(), "__OBATALA_CSS__", string(css), 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("reconcile: create output dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return fmt.Errorf("reconcile: write %s: %w", path, err)
	}
	return nil
}
