package scenariodef

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	fixturePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)
)

// Load reads every *.yaml file under dir (one scenario per file) and
// returns the scenarios sorted by id. Subdirectories are walked
// recursively, so suites can nest by theme (security/, payments/, …).
// File names are free-form; the scenario's own id field is authoritative.
func Load(dir string) ([]Scenario, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isYAML(d.Name()) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scenariodef: read suite %s: %w", dir, err)
	}
	var scenarios []Scenario
	seen := map[string]string{} // id -> file
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("scenariodef: read %s: %w", path, err)
		}
		var s Scenario
		if err := yaml.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("scenariodef: parse %s: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil, fmt.Errorf("scenariodef: rel %s: %w", path, err)
		}
		if s.Group = filepath.ToSlash(filepath.Dir(rel)); s.Group == "." {
			s.Group = ""
		}
		if err := Validate(s); err != nil {
			return nil, fmt.Errorf("scenariodef: %s: %w", path, err)
		}
		if prev, dup := seen[s.ID]; dup {
			return nil, fmt.Errorf("scenariodef: duplicate id %q in %s and %s", s.ID, prev, path)
		}
		seen[s.ID] = path
		scenarios = append(scenarios, s)
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("scenariodef: suite %s has no scenario files", dir)
	}
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })
	return scenarios, nil
}

func isYAML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".yml"
}

// Validate checks one scenario definition for the mistakes obatala
// validate must catch: missing criteria, bad budgets, malformed ids.
func Validate(s Scenario) error {
	var problems []string
	if !idPattern.MatchString(s.ID) {
		problems = append(problems, "id must be kebab-case ([a-z0-9-], 2-64 chars)")
	}
	if s.Fixture != "" && !fixturePattern.MatchString(s.Fixture) {
		problems = append(problems, fmt.Sprintf("fixture %q must be a slug ([a-z0-9-], 3-41 chars)", s.Fixture))
	}
	if s.Customer.Description == "" {
		problems = append(problems, "customer.description is required")
	}
	if s.Customer.Strategy == "" {
		problems = append(problems, "customer.strategy is required")
	}
	if s.Customer.MaxTurns < 0 {
		problems = append(problems, "customer.max_turns must be >= 0")
	}
	if len(s.Criteria.Success) == 0 {
		problems = append(problems, "criteria.success must list at least one statement")
	}
	for i, c := range s.Criteria.Success {
		if strings.TrimSpace(c) == "" {
			problems = append(problems, fmt.Sprintf("criteria.success[%d] is empty", i))
		}
	}
	if s.Budgets.MaxTurns < 0 {
		problems = append(problems, "budgets.max_turns must be >= 0")
	}
	if s.Budgets.TurnLatencyP95MS < 0 {
		problems = append(problems, "budgets.turn_latency_p95_ms must be >= 0")
	}
	if s.Repeat < 0 || (s.Repeat > 0 && s.PassThreshold <= 0) {
		problems = append(problems, "repeat requires a positive pass_threshold")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

// Filter returns the scenarios matching the tag and id filters; empty
// filters match everything.
func Filter(scenarios []Scenario, tag, id string) []Scenario {
	var out []Scenario
	for _, s := range scenarios {
		if id != "" && s.ID != id {
			continue
		}
		if tag != "" && !contains(s.Tags, tag) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
