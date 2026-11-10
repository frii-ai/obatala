package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/report"
)

const reconcilerSystemPrompt = `You are a prompt-engineering specialist for a conversational bot under regression test.
You are given: the bot's full system prompt, one failed scenario's judge verdict (reasoning, met/unmet success criteria, triggered failure criteria), and the full conversation transcript.

Propose CONCRETE edits to the system prompt that would have made the unmet criteria met in this exact conversation, without breaking the met criteria.

Rules:
- Only suggest changes to the system prompt (never to scenario criteria, the customer simulator, or code).
- Every suggestion must quote the exact current prompt text it replaces or augments in "current_excerpt" (a verbatim substring of the prompt; empty only when proposing a brand-new block).
- "proposed_text" must be drop-in replacement text for that spot.
- Prefer the smallest change that fixes the behavior.
- If a failure is not fixable by prompt changes (infrastructure flake, model capability gap), say so in "diagnosis" with an empty "suggestions" array.
- Output JSON only, no prose, matching exactly:
{"diagnosis": "...", "suggestions": [{"title": "...", "section": "...", "current_excerpt": "...", "proposed_text": "...", "targets_criteria": ["..."], "confidence": "high"|"medium"|"low"}]}`

// reconcilerResponse is the strict JSON the reconciler LLM must produce.
type reconcilerResponse struct {
	Diagnosis   string       `json:"diagnosis"`
	Suggestions []Suggestion `json:"suggestions"`
}

// analyze makes one reconciler LLM call for one failed scenario.
func analyze(ctx context.Context, deps Deps, meta report.RunMeta, s report.ScenarioResult, prompt, botDescription string) (Result, error) {
	var b strings.Builder
	if d := strings.TrimSpace(botDescription); d != "" {
		fmt.Fprintf(&b, "## About the bot under test\n%s\n\n", d)
	}
	fmt.Fprintf(&b, "## System prompt under analysis\n\n%s\n\n", prompt)
	fmt.Fprintf(&b, "## Scenario: %s", s.ID)
	if s.Name != "" {
		fmt.Fprintf(&b, " (%s)", s.Name)
	}
	b.WriteString("\n")
	if s.Fixture != "" {
		fmt.Fprintf(&b, "fixture: %s\n", s.Fixture)
	}
	if meta.BotModel != "" {
		fmt.Fprintf(&b, "bot model: %s\n", meta.BotModel)
	}
	if meta.PromptVersion != "" {
		fmt.Fprintf(&b, "prompt version: %s\n", meta.PromptVersion)
	}
	b.WriteString("\n## Judge verdict: FAIL\n")
	if s.Reasoning != "" {
		fmt.Fprintf(&b, "\n### Reasoning\n%s\n", s.Reasoning)
	}
	if len(s.MetCriteria) > 0 {
		b.WriteString("\n### Success criteria (met — must keep working)\n")
		for _, c := range s.MetCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(s.UnmetCriteria) > 0 {
		b.WriteString("\n### Success criteria (unmet — must fix)\n")
		for _, c := range s.UnmetCriteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(s.TriggeredFailures) > 0 {
		b.WriteString("\n### Triggered failure criteria\n")
		for _, c := range s.TriggeredFailures {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(s.BudgetViolations) > 0 {
		b.WriteString("\n### Budget violations (latency, not prompt-fixable)\n")
		for _, v := range s.BudgetViolations {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	b.WriteString("\n## Conversation transcript\n")
	for _, line := range s.Transcript {
		fmt.Fprintf(&b, "%s\n", line)
	}
	b.WriteString("\n## Task\n")
	b.WriteString("Propose the smallest system-prompt edits that would have made every unmet criterion met given this transcript, keeping the met criteria working.\n")

	resp, err := completeJSON(ctx, deps, b.String())
	if err != nil {
		return Result{}, fmt.Errorf("reconcile: %s: %w", s.ID, err)
	}
	return Result{
		ScenarioID:    s.ID,
		Name:          s.Name,
		Fixture:       s.Fixture,
		Reasoning:     s.Reasoning,
		UnmetCriteria: s.UnmetCriteria,
		Diagnosis:     resp.Diagnosis,
		Suggestions:   resp.Suggestions,
	}, nil
}

// completeJSON calls the reconciler LLM and parses its strict-JSON reply,
// retrying with the parse error appended as feedback (harden pattern).
func completeJSON(ctx context.Context, deps Deps, userContent string) (reconcilerResponse, error) {
	attempts := deps.RetryAttempts
	if attempts <= 0 {
		attempts = defaultRetryAttempts
	}
	backoff := deps.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		content := userContent
		if lastErr != nil {
			content += fmt.Sprintf("\n\nYour previous reply was not valid: %v\nReply again with corrected JSON only.", lastErr)
		}
		req := llm.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: reconcilerSystemPrompt},
			{Role: llm.RoleUser, Content: content},
		}}
		completion, err := deps.Completion.Complete(ctx, req)
		if err != nil {
			lastErr = err
		} else if len(completion.Choices) == 0 {
			lastErr = errors.New("no choices in completion")
		} else {
			parsed, parseErr := parseReconcilerResponse(completion.Choices[0].Content)
			if parseErr != nil {
				lastErr = parseErr
			} else if strings.TrimSpace(parsed.Diagnosis) == "" {
				lastErr = errors.New("diagnosis is empty")
			} else {
				return parsed, nil
			}
		}
		if attempt < attempts {
			if err := sleepCtx(ctx, backoff*time.Duration(attempt)); err != nil {
				return reconcilerResponse{}, err
			}
		}
	}
	return reconcilerResponse{}, fmt.Errorf("reconciler reply unusable after %d attempts: %w", attempts, lastErr)
}

func parseReconcilerResponse(content string) (reconcilerResponse, error) {
	trimmed := strings.TrimSpace(content)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	var out reconcilerResponse
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return reconcilerResponse{}, fmt.Errorf("parse JSON: %w", err)
	}
	return out, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
