// Package scenariodef holds the YAML schema for scenario files in
// scenarios/*.yaml.
package scenariodef

// Scenario is one test definition; adding a YAML file with this shape adds
// a runnable scenario with no Go changes.
type Scenario struct {
	ID   string   `yaml:"id"`
	Name string   `yaml:"name"`
	Tags []string `yaml:"tags"`
	// Fixture names the fixture key this scenario runs against (the
	// adapter-defined fixture set; see prepare.Manager). Required when
	// the suite's fixture set is non-empty; bots without backend state
	// leave it unset.
	Fixture string `yaml:"fixture"`
	// Group is the suite-relative folder containing the scenario file
	// (slash-separated; "" for files at the suite root). It is derived by
	// Load, never parsed from YAML, and is only used for report grouping.
	Group    string   `yaml:"-"`
	Customer Customer `yaml:"customer"`
	Criteria Criteria `yaml:"criteria"`
	Budgets  Budgets  `yaml:"budgets"`
	// Repeat and PassThreshold are reserved for flaky-scenario support.
	Repeat        int `yaml:"repeat,omitempty"`
	PassThreshold int `yaml:"pass_threshold,omitempty"`
}

// Customer is the simulated buyer persona and strategy.
type Customer struct {
	Description string `yaml:"description"`
	Strategy    string `yaml:"strategy"`
	MaxTurns    int    `yaml:"max_turns"`
	// AllowURLs relaxes the engine's hard no-URL guard on customer
	// messages. The guard prevents role leakage (the simulator fabricating
	// links the real user would never send); only set this for scenarios
	// where the customer must legitimately share a link.
	AllowURLs bool `yaml:"allow_urls"`
}

// Criteria are natural-language success and failure statements for the judge.
type Criteria struct {
	Success []string `yaml:"success"`
	Failure []string `yaml:"failure"`
}

// Budgets are hard limits; violations are reported separately from pass/fail.
type Budgets struct {
	TurnLatencyP95MS int `yaml:"turn_latency_p95_ms"`
	MaxTurns         int `yaml:"max_turns"`
}
