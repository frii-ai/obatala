package obatala_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala"
	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

// stubFixtureSet is a trivial FixtureSet: keys are fixture file stems.
type stubFixtureSet struct{ keys []string }

func (s stubFixtureSet) Keys() []string { return s.keys }

// stubManager records lifecycle calls and can be told to fail preflight
// or teardown; it stands in for a bot adapter's control-plane manager.
type stubManager struct {
	mu           sync.Mutex
	preflightErr error
	teardownErr  error
	seeds        []string
	identities   []string
	perRun       []string
	perSuite     []string
	nextID       int
}

func (m *stubManager) Preflight(context.Context) (report.RunMeta, error) {
	if m.preflightErr != nil {
		return report.RunMeta{}, m.preflightErr
	}
	return report.RunMeta{Environment: "local", ContractVersion: 1, BotModel: "fake"}, nil
}

func (m *stubManager) LoadFixtures(dir string) (prepare.FixtureSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			keys = append(keys, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	return stubFixtureSet{keys: keys}, nil
}

func (m *stubManager) Seed(_ context.Context, key string, _ prepare.FixtureSet) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seeds = append(m.seeds, key)
	return map[string]string{"instance": "inst-" + key, "instance_token": "tok-" + key}, nil
}

func (m *stubManager) NewIdentity() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("id-%d", m.nextID)
	m.identities = append(m.identities, id)
	return id, nil
}

func (m *stubManager) Teardown(_ context.Context, perRunIDs []string, perSuiteKeys []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.perRun = append(m.perRun, perRunIDs...)
	m.perSuite = append(m.perSuite, perSuiteKeys...)
	return m.teardownErr
}

var (
	stubMu sync.Mutex
	stub   = &stubManager{}
)

func TestMain(m *testing.M) {
	_ = transport.Register("harness-fake", func(settings map[string]any) (transport.Transport, error) {
		batches := 2
		if n, ok := settings["reply_batches"].(int); ok && n > 0 {
			batches = n
		}
		replies := make([][]string, batches)
		for i := range replies {
			replies[i] = []string{"Yes, cotton socks are $1,500. Link: https://pay.example/x"}
		}
		return fakes.NewTransport(replies...), nil
	})
	_ = llm.Register("harness-customer", func(map[string]any) (llm.Completion, error) {
		return fakes.NewCustomer("Do you have cotton socks?"), nil
	})
	_ = llm.Register("harness-judge", func(map[string]any) (llm.Completion, error) {
		verdict := os.Getenv("HARNESS_VERDICT")
		if verdict == "" {
			verdict = "success"
		}
		return fakes.NewVerdict(verdict, os.Getenv("HARNESS_REASONING")), nil
	})
	_ = prepare.RegisterManager("harness", func(config.Config) (prepare.Manager, error) {
		stubMu.Lock()
		defer stubMu.Unlock()
		return stub, nil
	})
	os.Exit(m.Run())
}

func resetStub() {
	stubMu.Lock()
	defer stubMu.Unlock()
	stub = &stubManager{}
}

func TestExecutePass(t *testing.T) {
	root := writeHarnessProject(t, "")
	summary, code, err := execute(t, root, nil)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v summary=%+v", code, err, summary)
	}
	if len(summary.Scenarios) != 1 || summary.Scenarios[0].Outcome != "pass" {
		t.Fatalf("scenarios = %+v", summary.Scenarios)
	}
	assertLifecycle(t, 1, []string{"shoes-and-socks"})
}

func TestExecuteFailVerdictStillTearsDown(t *testing.T) {
	t.Setenv("HARNESS_VERDICT", "failure")
	t.Setenv("HARNESS_REASONING", "Bot never sent a link")
	root := writeHarnessProject(t, "")
	summary, code, err := execute(t, root, nil)
	if err != nil || code != 1 {
		t.Fatalf("code=%d err=%v, want 1/nil", code, err)
	}
	if summary.Scenarios[0].Outcome != "fail" || summary.Scenarios[0].Reasoning != "Bot never sent a link" {
		t.Fatalf("scenario = %+v", summary.Scenarios[0])
	}
	assertLifecycle(t, 1, []string{"shoes-and-socks"})
}

func TestExecuteSetupErrorUnknownFixture(t *testing.T) {
	root := writeHarnessProject(t, "")
	path := filepath.Join(root, "scenarios", "socks.yaml")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "fixture: shoes-and-socks", "fixture: ghost-shop", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code, err := execute(t, root, nil)
	if code != 2 || err == nil || !strings.Contains(err.Error(), `unknown fixture "ghost-shop"`) {
		t.Fatalf("code=%d err=%v", code, err)
	}
	stubMu.Lock()
	defer stubMu.Unlock()
	if len(stub.seeds) != 0 {
		t.Fatalf("seeds = %v, want none after setup failure", stub.seeds)
	}
}

func TestExecuteSetupErrorMissingFixtureRef(t *testing.T) {
	root := writeHarnessProject(t, "")
	path := filepath.Join(root, "scenarios", "socks.yaml")
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "fixture: shoes-and-socks\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, code, err := execute(t, root, nil)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "must reference a fixture") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestExecutePreflightError(t *testing.T) {
	root := writeHarnessProject(t, "")
	stubMu.Lock()
	stub.preflightErr = fmt.Errorf("test_mode is false")
	stubMu.Unlock()
	_, code, err := execute(t, root, nil)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "preflight") || !strings.Contains(err.Error(), "test_mode is false") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestExecuteTeardownRefusalEscalatesToError(t *testing.T) {
	root := writeHarnessProject(t, "")
	stubMu.Lock()
	stub.teardownErr = fmt.Errorf("not synthetic")
	stubMu.Unlock()
	_, code, err := execute(t, root, nil)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "teardown") || !strings.Contains(err.Error(), "not synthetic") {
		t.Fatalf("code=%d err=%v, want escalated teardown error", code, err)
	}
}

func TestExecuteWritesExtraReports(t *testing.T) {
	root := writeHarnessProject(t, "")
	chdir(t, root)
	summary, code, err := execute(t, root, []string{"json"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	data, err := os.ReadFile(filepath.Join("reports", "report.json"))
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	var got report.RunSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ReportVersion != report.CurrentVersion || len(got.Scenarios) != 1 || summary.Scenarios[0].ID != got.Scenarios[0].ID {
		t.Fatalf("report = %+v", got)
	}
}

func TestExecuteRejectsUnknownReporter(t *testing.T) {
	root := writeHarnessProject(t, "")
	_, code, err := execute(t, root, []string{"pdf"})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "pdf") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestRunPass(t *testing.T) {
	root := writeHarnessProject(t, "")
	// Run only fails the test on problems; success is the assertion.
	obatala.Run(t,
		obatala.Config(filepath.Join(root, "obatala.yaml")),
		obatala.Suite(filepath.Join(root, "scenarios")),
		obatala.Fixtures(filepath.Join(root, "fixtures", "shops")),
	)
}

func TestValidate(t *testing.T) {
	root := writeHarnessProject(t, "")
	cfg, scenarios, fixtures, err := obatala.Validate(
		filepath.Join(root, "obatala.yaml"),
		filepath.Join(root, "scenarios"),
		filepath.Join(root, "fixtures", "shops"),
	)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if scenarios != 1 || fixtures != 1 || cfg.Transport.Name != "harness-fake" {
		t.Fatalf("cfg=%+v scenarios=%d fixtures=%d", cfg, scenarios, fixtures)
	}
}

func execute(t *testing.T, root string, reports []string) (report.RunSummary, int, error) {
	t.Helper()
	return obatala.Execute(context.Background(), obatala.Options{
		ConfigPath:  filepath.Join(root, "obatala.yaml"),
		SuiteDir:    filepath.Join(root, "scenarios"),
		FixturesDir: filepath.Join(root, "fixtures", "shops"),
		Reports:     reports,
	})
}

func assertLifecycle(t *testing.T, wantSeeds int, wantKeys []string) {
	t.Helper()
	stubMu.Lock()
	defer stubMu.Unlock()
	if len(stub.seeds) != wantSeeds {
		t.Fatalf("seeds = %v, want %d", stub.seeds, wantSeeds)
	}
	if strings.Join(stub.perSuite, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("teardown suite keys = %v, want %v", stub.perSuite, wantKeys)
	}
	if len(stub.perRun) != wantSeeds {
		t.Fatalf("teardown per-run ids = %v, want %d", stub.perRun, wantSeeds)
	}
	if strings.Join(stub.perRun, ",") != strings.Join(stub.identities, ",") {
		t.Fatalf("teardown ids %v != generated identities %v", stub.perRun, stub.identities)
	}
}

func writeHarnessProject(t *testing.T, _ string) string {
	t.Helper()
	resetStub()
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "scenarios"))
	mustMkdir(t, filepath.Join(root, "fixtures", "shops"))
	writeFile(t, filepath.Join(root, "scenarios", "socks.yaml"), harnessScenario)
	writeFile(t, filepath.Join(root, "fixtures", "shops", "shoes-and-socks.yaml"), harnessFixture)
	writeFile(t, filepath.Join(root, "obatala.yaml"), harnessConfig)
	return root
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
}

const harnessScenario = `id: socks-enquiry
name: Socks enquiry
tags: [smoke]
fixture: shoes-and-socks
customer:
  description: Buyer wants socks
  strategy: Ask and pay
  max_turns: 1
criteria:
  success:
    - The bot sends a payment link
`

const harnessFixture = `# The harness manager treats fixture files as opaque key holders.
key: demo
`

const harnessConfig = `transport:
  name: harness-fake
customer:
  provider: harness-customer
  model: fake
judge:
  provider: harness-judge
  model: fake
`

func TestRunConfigFromEnv(t *testing.T) {
	root := writeHarnessProject(t, "")
	// No Config option: the default path must come from OBATALA_CONFIG.
	t.Setenv("OBATALA_CONFIG", filepath.Join(root, "obatala.yaml"))
	obatala.Run(t,
		obatala.Suite(filepath.Join(root, "scenarios")),
		obatala.Fixtures(filepath.Join(root, "fixtures", "shops")),
	)
}
