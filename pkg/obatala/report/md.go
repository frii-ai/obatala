package report

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	_ = Register("md", newMarkdown)
}

func newMarkdown(settings map[string]any) (Reporter, error) {
	return &fileReporter{
		path:   settingPath(settings, "reports/report.md"),
		render: renderMarkdown,
	}, nil
}

func renderMarkdown(run RunSummary) ([]byte, error) {
	var b strings.Builder
	pass, fail, errs := Tally(run)
	fmt.Fprintf(&b, "# Obatala run %s\n\n", run.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Environment `%s` · contract `%d` · prompt `%s` · model `%s`\n\n",
		run.Meta.Environment, run.Meta.ContractVersion, run.Meta.PromptVersion, run.Meta.BotModel)
	fmt.Fprintf(&b, "**%d scenarios: %d pass, %d fail, %d error** · duration %s · turn latency p50 %s / p95 %s / max %s\n\n",
		len(run.Scenarios), pass, fail, errs,
		run.Duration.Round(time.Millisecond),
		run.Latency.FirstP50.Round(time.Millisecond),
		run.Latency.FirstP95.Round(time.Millisecond),
		run.Latency.FirstMax.Round(time.Millisecond))

	b.WriteString("| scenario | outcome | turns | duration | p95 first reply | notes |\n")
	b.WriteString("|---|---|---:|---:|---:|---|\n")
	for _, s := range run.Scenarios {
		notes := s.Error
		if notes == "" && len(s.BudgetViolations) > 0 {
			notes = strings.Join(s.BudgetViolations, "; ")
		}
		if notes == "" {
			notes = s.Reasoning
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s |\n",
			s.ID, s.Outcome, s.TurnsUsed,
			s.Duration.Round(time.Millisecond),
			s.Latency.FirstP95.Round(time.Millisecond),
			escapeCell(notes))
	}
	b.WriteString("\n")
	for _, s := range run.Scenarios {
		fmt.Fprintf(&b, "## %s — %s\n\n", s.ID, s.Outcome)
		if s.Reasoning != "" {
			fmt.Fprintf(&b, "%s\n\n", s.Reasoning)
		}
		writeMDList(&b, "Met criteria", s.MetCriteria)
		writeMDList(&b, "Unmet criteria", s.UnmetCriteria)
		writeMDList(&b, "Triggered failures", s.TriggeredFailures)
		writeMDList(&b, "Budget violations", s.BudgetViolations)
		if len(s.TranscriptHead) > 0 || len(s.TranscriptTail) > 0 {
			b.WriteString("```\n")
			for _, line := range s.TranscriptHead {
				fmt.Fprintf(&b, "%s\n", line)
			}
			if len(s.TranscriptTail) > 0 {
				b.WriteString("…\n")
				for _, line := range s.TranscriptTail {
					fmt.Fprintf(&b, "%s\n", line)
				}
			}
			b.WriteString("```\n\n")
		}
	}
	return []byte(b.String()), nil
}

func writeMDList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "**%s:**\n\n", title)
	for _, it := range items {
		fmt.Fprintf(b, "- %s\n", it)
	}
	b.WriteString("\n")
}

func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}
