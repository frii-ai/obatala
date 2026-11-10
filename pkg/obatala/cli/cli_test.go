package cli_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/cli"
	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"github.com/frii-ai/obatala/pkg/obatala/transport"
)

func init() {
	_ = transport.Register("clitest", func(map[string]any) (transport.Transport, error) {
		return fakes.NewTransport([]string{"Sure, the blue mug is $12."}), nil
	})
	_ = prepare.RegisterManager("clitest", func(config.Config) (prepare.Manager, error) {
		return &stubManager{}, nil
	})
	_ = llm.Register("clitest-customer", func(map[string]any) (llm.Completion, error) {
		return fakes.NewCustomer("How much is the blue mug?"), nil
	})
	// The judge's model name scripts its verdict ("success" or "failure").
	_ = llm.Register("clitest-judge", func(settings map[string]any) (llm.Completion, error) {
		verdict, _ := settings["model"].(string)
		return fakes.NewVerdict(verdict, "scripted "+verdict), nil
	})
}

// stubManager treats every fixture file stem as a key and seeds nothing.
type stubManager struct{ next int }

type keySet []string

func (k keySet) Keys() []string { return k }

func (m *stubManager) Preflight(context.Context) (report.RunMeta, error) {
	return report.RunMeta{Environment: "test", TestMode: true}, nil
}

func (m *stubManager) LoadFixtures(dir string) (prepare.FixtureSet, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var keys keySet
	for _, e := range entries {
		keys = append(keys, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return keys, nil
}

func (m *stubManager) Seed(context.Context, string, prepare.FixtureSet) (map[string]string, error) {
	return nil, nil
}

func (m *stubManager) NewIdentity() (string, error) {
	m.next++
	return fmt.Sprintf("user-%d", m.next), nil
}

func (m *stubManager) Teardown(context.Context, []string, []string) error { return nil }

const scenario = `id: mug-price
name: Mug price
tags: [smoke]
fixture: shop
customer:
  description: A shopper
  strategy: Ask the price of the blue mug, then stop.
  max_turns: 1
criteria:
  success:
    - The bot states the price of the blue mug
`

// project writes a minimal suite into a temp dir, chdirs there, and
// returns the dir. verdict scripts the judge.
func project(t *testing.T, verdict string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "scenarios", "mug.yaml"), scenario)
	write(t, filepath.Join(root, "fixtures", "shop.yaml"), "name: shop\n")
	write(t, filepath.Join(root, "obatala.yaml"), `transport:
  name: clitest
fixtures_manager: clitest
customer:
  provider: clitest-customer
  model: fake
judge:
  provider: clitest-judge
  model: `+verdict+"\n")
	t.Chdir(root)
	return root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageUsesConfig(t *testing.T) {
	var stdout, stderr strings.Builder
	code := cli.Main([]string{"help"}, &stdout, &stderr, cli.Config{
		Name: "mybot-tests", Tagline: "tests for mybot", DefaultFixturesDir: "fixtures/tenants",
	})
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	out := stdout.String()
	for _, want := range []string{"mybot-tests — tests for mybot", "mybot-tests run", "[--fixtures fixtures/tenants]"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
}

func TestNoArgsAndUnknownCommandExit2(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := cli.Main(nil, &stdout, &stderr, cli.Config{}); code != 2 {
		t.Errorf("no args exit=%d, want 2", code)
	}
	stderr.Reset()
	if code := cli.Main([]string{"bogus"}, &stdout, &stderr, cli.Config{}); code != 2 {
		t.Errorf("unknown exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `obatala: unknown command "bogus"`) {
		t.Errorf("stderr = %s", stderr.String())
	}
}

func TestValidateAndListUseDefaults(t *testing.T) {
	project(t, "success")
	var stdout, stderr strings.Builder
	if code := cli.Main([]string{"validate"}, &stdout, &stderr, cli.Config{}); code != 0 {
		t.Fatalf("validate exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 scenarios, 1 fixtures") {
		t.Fatalf("validate output = %s", stdout.String())
	}
	stdout.Reset()
	if code := cli.Main([]string{"list"}, &stdout, &stderr, cli.Config{}); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mug-price") {
		t.Fatalf("list output = %s", stdout.String())
	}
}

func TestRunExitCodes(t *testing.T) {
	for _, tc := range []struct {
		verdict string
		want    int
	}{{"success", 0}, {"failure", 1}} {
		t.Run(tc.verdict, func(t *testing.T) {
			root := project(t, tc.verdict)
			var stdout, stderr strings.Builder
			code := cli.Main([]string{"run", "--report", "json"}, &stdout, &stderr, cli.Config{})
			if code != tc.want {
				t.Fatalf("exit=%d, want %d\nstdout=%s\nstderr=%s", code, tc.want, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(root, "reports", "report.json")); err != nil {
				t.Fatalf("json report not written: %v", err)
			}
		})
	}
}

func TestRunMissingConfigExit2(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr strings.Builder
	if code := cli.Main([]string{"run"}, &stdout, &stderr, cli.Config{Name: "x"}); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.HasPrefix(stderr.String(), "x: ") {
		t.Fatalf("stderr should use configured name: %s", stderr.String())
	}
}
