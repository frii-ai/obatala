// Package creator drafts scenario YAML from a described real chat
// ("obatala create"): an LLM distills the chat into the declarative schema
// (persona, customer-side strategy, judge criteria), which is validated and
// written in house style. Pure text generation — no backend contact.
package creator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"gopkg.in/yaml.v3"
)

// Defaults applied post-generation (house conventions).
const (
	defaultLatencyBudgetMS = 30000
	maxTurnsHeadroom       = 1
)

const (
	defaultRetryAttempts = 3
	defaultRetryBackoff  = 2 * time.Second
)

var groupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// systemPrompt instructs the creator LLM on the exact obatala scenario
// schema and house style.
const systemPrompt = `You are an author of regression scenarios for "obatala", a regression-test harness for conversational bots. You turn a real chat between a user and the bot (plus a short description of what to reproduce) into ONE obatala scenario file.

Obatala scenarios are DECLARATIVE — there are no scripted turns or expected bot replies. A scenario is only:
- customer.description: a short persona for an LLM-simulated customer.
- customer.strategy: second-person imperative instructions telling the simulator what to say, message by message, following the CUSTOMER (user) side of the real chat (including the exact numbers/names/values the customer used). Branch with "If the bot …, then …". ALWAYS end with an explicit stop condition ("Finish after …" / "Stop as soon as …").
- customer.max_turns: enough for the customer side of the chat (number of customer messages in the transcript, ±1).
- criteria.success: third-person statements about what the bot MUST do (testable from a transcript alone). At least one.
- criteria.failure: third-person statements about what the bot MUST NOT do — always include the unwanted behavior seen in the real chat.

Rules:
- id: kebab-case, 2-64 chars, [a-z0-9-], unique and descriptive (e.g. "below-minimum", "sourcing-boots-insist").
- name: short human title of the behavior under test.
- tags: 1-3 lowercase tags (e.g. pricing, booking, refund, security, escalation).
- fixture: pick EXACTLY ONE key from the "Available fixture keys" list (the scenario runs against that fixture's seeded data). Mention concrete fixture facts (names, prices, dates, limits) in strategy and criteria so the judge can verify them.
- suggested_group: a single lowercase folder name (kebab-case) for organizing the suite, or "" for the suite root. Examples: pricing, booking, security.
- comments: 1-3 short lines of context for the file header (e.g. the relevant fixture facts and why they matter).
- Criteria statements must be judgeable from the conversation transcript alone: name the expected value/refusal/step, not internal states.
- Never invent schema fields (no turns:, no expected_replies, no timeouts).
- Output JSON only, no prose, matching exactly:
{"id":"...","name":"...","tags":["..."],"fixture":"...","suggested_group":"...","comments":["..."],"customer":{"description":"...","strategy":"...","max_turns":3},"criteria":{"success":["..."],"failure":["..."]}}`

// Options configure one create invocation.
type Options struct {
	// Describe is the one-line description of what to reproduce (required).
	Describe string
	// ChatText is the pasted chat transcript, free-form; "customer:"/"bot:"
	// line prefixes are encouraged (required).
	ChatText string
	// Unwanted is an optional note on the bad bot behavior observed.
	Unwanted string
	// BotDescription is optional free-text context about the bot under
	// test (domain, channel, audience), from config bot_description.
	BotDescription string
	// Fixture overrides the LLM's fixture choice; validated when set.
	Fixture string
	// ID and Tags override the LLM's proposals; Tags is comma-separated.
	ID   string
	Tags string
	// Group overrides the LLM's suggested_group (folder under SuiteDir).
	Group string
	// SuiteDir is the scenario suite (default scenarios).
	SuiteDir string
	// FixturesDir is the fixture directory for valid-key lookup.
	FixturesDir string
	// OutPath overrides the default <suite>/<group>/<id>.yaml location.
	OutPath string
	// Force overwrites an existing file; without it, an existing target
	// is an error.
	Force bool
	// DryRun prints the YAML to Stdout and writes nothing.
	DryRun bool
	// Stdout receives the YAML on dry-run; Stderr operational notes.
	// Nil writers discard.
	Stdout io.Writer
	Stderr io.Writer
}

// Deps are the live dependencies Run needs.
type Deps struct {
	// Manager supplies valid fixture keys via LoadFixtures (no seeding).
	Manager prepare.Manager
	// Completion is the creator LLM (config.Creator).
	Completion llm.Completion
	// RetryAttempts/RetryBackoff tune JSON/validation retries (0 = defaults).
	RetryAttempts int
	RetryBackoff  time.Duration
}

// Result describes what Run produced.
type Result struct {
	// Path is the written file ("" on dry-run).
	Path string
	// ID and Group are the final scenario identity (Group "" = suite root).
	ID    string
	Group string
	// YAML is the full rendered file contents.
	YAML []byte
}

// draft is the creator LLM's strict-JSON reply.
type draft struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Tags           []string `json:"tags"`
	Fixture        string   `json:"fixture"`
	SuggestedGroup string   `json:"suggested_group"`
	Comments       []string `json:"comments"`
	Customer       struct {
		Description string `json:"description"`
		Strategy    string `json:"strategy"`
		MaxTurns    int    `json:"max_turns"`
	} `json:"customer"`
	Criteria struct {
		Success []string `json:"success"`
		Failure []string `json:"failure"`
	} `json:"criteria"`
}

// Run generates the scenario, validates it, and writes (or prints) the YAML.
func Run(ctx context.Context, deps Deps, opts Options) (Result, error) {
	if deps.Manager == nil || deps.Completion == nil {
		return Result{}, errors.New("creator: manager and creator LLM are required")
	}
	if strings.TrimSpace(opts.Describe) == "" {
		return Result{}, errors.New("creator: -describe is required")
	}
	if strings.TrimSpace(opts.ChatText) == "" {
		return Result{}, errors.New("creator: chat text is required (-chat FILE or -chat -)")
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	suiteDir := opts.SuiteDir
	if suiteDir == "" {
		suiteDir = "scenarios"
	}

	existing, err := loadExistingIDs(suiteDir)
	if err != nil {
		return Result{}, err
	}
	fixtureKeys, err := fixtureKeySet(deps.Manager, opts.FixturesDir)
	if err != nil {
		return Result{}, err
	}
	if opts.Fixture != "" && len(fixtureKeys) > 0 && !fixtureKeys[opts.Fixture] {
		return Result{}, fmt.Errorf("creator: fixture %q not in %s (have: %s)",
			opts.Fixture, opts.FixturesDir, strings.Join(sortedKeys(fixtureKeys), ", "))
	}

	d, err := generate(ctx, deps, opts, fixtureKeys, existing)
	if err != nil {
		return Result{}, err
	}

	// Apply overrides and house invariants.
	if opts.Fixture != "" {
		d.Fixture = opts.Fixture
	}
	if opts.ID != "" {
		d.ID = opts.ID
	}
	if opts.Tags != "" {
		d.Tags = splitCSV(opts.Tags)
	}
	group := strings.TrimSpace(d.SuggestedGroup)
	if opts.Group != "" {
		group = opts.Group
	}
	if !groupPattern.MatchString(group) {
		group = ""
	}
	if d.Customer.MaxTurns <= 0 {
		d.Customer.MaxTurns = len(d.Criteria.Success) + 2
	}

	sc := scenariodef.Scenario{
		ID:      d.ID,
		Name:    d.Name,
		Tags:    d.Tags,
		Fixture: d.Fixture,
		Customer: scenariodef.Customer{
			Description: d.Customer.Description,
			Strategy:    d.Customer.Strategy,
			MaxTurns:    d.Customer.MaxTurns,
		},
		Criteria: scenariodef.Criteria{
			Success: d.Criteria.Success,
			Failure: d.Criteria.Failure,
		},
		Budgets: scenariodef.Budgets{
			TurnLatencyP95MS: defaultLatencyBudgetMS,
			MaxTurns:         d.Customer.MaxTurns + maxTurnsHeadroom,
		},
	}
	if err := finalValidate(sc, existing); err != nil {
		return Result{}, err
	}

	yamlBytes, err := render(sc, opts.Describe, d.Comments)
	if err != nil {
		return Result{}, err
	}

	if opts.DryRun {
		_, _ = fmt.Fprintf(stdout, "%s", yamlBytes)
		return Result{ID: sc.ID, Group: group, YAML: yamlBytes}, nil
	}

	outPath := opts.OutPath
	if outPath == "" {
		outPath = filepath.Join(suiteDir, group, sc.ID+".yaml")
	}
	if !opts.Force {
		if _, err := os.Stat(outPath); err == nil {
			return Result{}, fmt.Errorf("creator: %s already exists (use -force to overwrite)", outPath)
		} else if !os.IsNotExist(err) {
			return Result{}, fmt.Errorf("creator: stat %s: %w", outPath, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return Result{}, fmt.Errorf("creator: create output dir: %w", err)
	}
	if err := os.WriteFile(outPath, yamlBytes, 0o644); err != nil {
		return Result{}, fmt.Errorf("creator: write %s: %w", outPath, err)
	}
	errLog := opts.Stderr
	if errLog == nil {
		errLog = io.Discard
	}
	_, _ = fmt.Fprintf(errLog, "obatala: wrote %s\n", outPath)
	return Result{Path: outPath, ID: sc.ID, Group: group, YAML: yamlBytes}, nil
}

// generate makes the LLM call(s), retrying with parse/validation feedback.
func generate(ctx context.Context, deps Deps, opts Options, fixtureKeys map[string]bool, existing map[string]bool) (draft, error) {
	attempts := deps.RetryAttempts
	if attempts <= 0 {
		attempts = defaultRetryAttempts
	}
	backoff := deps.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	user := buildUserContent(opts, fixtureKeys)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		content := user
		if lastErr != nil {
			content += fmt.Sprintf("\n\nYour previous reply was not usable: %v\nReply again with corrected JSON only.", lastErr)
		}
		req := llm.Request{Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: systemPrompt},
			{Role: llm.RoleUser, Content: content},
		}}
		resp, err := deps.Completion.Complete(ctx, req)
		if err != nil {
			lastErr = err
		} else if len(resp.Choices) == 0 {
			lastErr = errors.New("no choices in completion")
		} else {
			d, parseErr := parseDraft(resp.Choices[0].Content)
			if parseErr != nil {
				lastErr = parseErr
			} else if err := validateDraft(d, fixtureKeys, existing, opts.Fixture != ""); err != nil {
				lastErr = err
			} else {
				return d, nil
			}
		}
		if attempt < attempts {
			if err := sleepCtx(ctx, backoff*time.Duration(attempt)); err != nil {
				return draft{}, err
			}
		}
	}
	return draft{}, fmt.Errorf("creator: no usable draft after %d attempts: %w", attempts, lastErr)
}

// loadExistingIDs reads the suite for the uniqueness check. A missing or
// empty suite is fine (nothing conflicts yet).
func loadExistingIDs(suiteDir string) (map[string]bool, error) {
	scenarios, err := scenariodef.Load(suiteDir)
	if err != nil {
		if strings.Contains(err.Error(), "no scenario files") {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("creator: load suite %s: %w", suiteDir, err)
	}
	out := make(map[string]bool, len(scenarios))
	for _, s := range scenarios {
		out[s.ID] = true
	}
	return out, nil
}

func fixtureKeySet(mgr prepare.Manager, dir string) (map[string]bool, error) {
	fx, err := mgr.LoadFixtures(dir)
	if err != nil {
		return nil, fmt.Errorf("creator: load fixtures: %w", err)
	}
	keys := fx.Keys()
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out, nil
}

// validateDraft applies schema + suite rules to the LLM's reply (used as
// retry feedback and to gate overrides).
func validateDraft(d draft, fixtureKeys map[string]bool, existing map[string]bool, fixtureOverridden bool) error {
	sc := draftToScenario(d)
	if err := scenariodef.Validate(sc); err != nil {
		return err
	}
	if existing[d.ID] {
		return fmt.Errorf("id %q already exists in the suite; pick a different id", d.ID)
	}
	if !fixtureOverridden && len(fixtureKeys) > 0 && !fixtureKeys[d.Fixture] {
		return fmt.Errorf("fixture %q is not available (choose one of the listed fixture keys)", d.Fixture)
	}
	return nil
}

// finalValidate re-checks the assembled scenario after overrides.
func finalValidate(sc scenariodef.Scenario, existing map[string]bool) error {
	if err := scenariodef.Validate(sc); err != nil {
		return fmt.Errorf("creator: generated scenario invalid: %w", err)
	}
	if existing[sc.ID] {
		return fmt.Errorf("creator: id %q already exists in the suite", sc.ID)
	}
	return nil
}

func draftToScenario(d draft) scenariodef.Scenario {
	return scenariodef.Scenario{
		ID:      d.ID,
		Name:    d.Name,
		Tags:    d.Tags,
		Fixture: d.Fixture,
		Customer: scenariodef.Customer{
			Description: d.Customer.Description,
			Strategy:    d.Customer.Strategy,
			MaxTurns:    d.Customer.MaxTurns,
		},
		Criteria: scenariodef.Criteria{
			Success: d.Criteria.Success,
			Failure: d.Criteria.Failure,
		},
	}
}

func parseDraft(content string) (draft, error) {
	trimmed := strings.TrimSpace(content)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	var d draft
	if err := json.Unmarshal([]byte(trimmed), &d); err != nil {
		return draft{}, fmt.Errorf("parse JSON: %w", err)
	}
	return d, nil
}

// render emits the house-style file: comment header, then the marshaled
// struct (field order matches house key order).
func render(sc scenariodef.Scenario, describe string, comments []string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Generated by obatala create from a real chat.\n")
	if describe != "" {
		fmt.Fprintf(&b, "# Describe: %s\n", strings.ReplaceAll(describe, "\n", " "))
	}
	for _, c := range comments {
		for _, line := range strings.Split(strings.TrimSpace(c), "\n") {
			fmt.Fprintf(&b, "# %s\n", line)
		}
	}
	b.WriteString("\n")
	body, err := yaml.Marshal(sc)
	if err != nil {
		return nil, fmt.Errorf("creator: marshal scenario: %w", err)
	}
	b.Write(body)
	return []byte(b.String()), nil
}

func buildUserContent(opts Options, fixtureKeys map[string]bool) string {
	var b strings.Builder
	if d := strings.TrimSpace(opts.BotDescription); d != "" {
		fmt.Fprintf(&b, "## About the bot under test\n%s\n\n", d)
	}
	fmt.Fprintf(&b, "## Task\nDescribe what to reproduce: %s\n\n", strings.TrimSpace(opts.Describe))
	if u := strings.TrimSpace(opts.Unwanted); u != "" {
		fmt.Fprintf(&b, "The unwanted bot behavior observed: %s\n\n", u)
	}
	if len(fixtureKeys) > 0 {
		b.WriteString("## Available fixture keys (choose exactly one)\n")
		for _, k := range sortedKeys(fixtureKeys) {
			fmt.Fprintf(&b, "- %s\n", k)
		}
		b.WriteString("\n")
	}
	b.WriteString("## Real chat (verbatim)\n")
	b.WriteString(strings.TrimSpace(opts.ChatText))
	b.WriteString("\n")
	return b.String()
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
