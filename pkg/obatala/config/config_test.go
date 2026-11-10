package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/config"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "obatala.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaultsAndParses(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: gemma4:31b
  base_url: https://ollama.com/v1/
  api_key_env: OLLAMA_API_KEY
transport:
  name: webhook
  settings:
    backend_url: http://localhost:3000
`)
	t.Setenv("OLLAMA_API_KEY", "secret")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Concurrency != config.DefaultConcurrency {
		t.Fatalf("concurrency = %d", cfg.Concurrency)
	}
	if cfg.ReplyTimeoutDuration() != 90*time.Second {
		t.Fatalf("reply timeout = %v", cfg.ReplyTimeoutDuration())
	}
	if cfg.SettleWindowDuration() != 4*time.Second {
		t.Fatalf("settle window = %v", cfg.SettleWindowDuration())
	}
	if cfg.Judge.Provider != "openai" || cfg.Judge.Model != "gemma4:31b" {
		t.Fatalf("judge should default to customer: %+v", cfg.Judge)
	}
	settings, err := cfg.Customer.Settings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings["api_key"] != "secret" || settings["model"] != "gemma4:31b" {
		t.Fatalf("settings = %v", settings)
	}
	if cfg.Transport.Name != "webhook" || cfg.Transport.Settings["backend_url"] != "http://localhost:3000" {
		t.Fatalf("transport = %+v", cfg.Transport)
	}
}

func TestLoadMissingAPIKeyEnv(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: m
  api_key_env: DEFINITELY_NOT_SET_ENV
`)
	t.Setenv("DEFINITELY_NOT_SET_ENV", "")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for missing api key env")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: m
reply_timeout: banana
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for bad reply_timeout")
	}
}

func TestLoadRejectsMissingModel(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestLoadNoFile(t *testing.T) {
	if _, err := config.Load(""); err == nil {
		t.Fatal("expected error when no customer provider is configured")
	}
}

func TestBudgetViolationsMode(t *testing.T) {
	const base = `
customer:
  provider: openai
  model: gemma4:31b
  base_url: https://ollama.com/v1/
transport:
  name: webhook
`
	path := writeConfig(t, base+"budget_violations: warn\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load warn: %v", err)
	}
	if cfg.BudgetViolations != "warn" {
		t.Fatalf("mode = %q", cfg.BudgetViolations)
	}
	path = writeConfig(t, base+"budget_violations: explode\n")
	if _, err := config.Load(path); err == nil {
		t.Fatal("expected error for invalid budget_violations")
	}
	cfg, err = config.Load("")
	if err == nil {
		// defaults-only config has no provider; mode default is applied
		// before validation fails, so check via a valid minimal file instead.
		t.Skip("defaults-only config invalid (no provider)")
	}
	path = writeConfig(t, base)
	cfg, err = config.Load(path)
	if err != nil || cfg.BudgetViolations != "fatal" {
		t.Fatalf("default = %q err=%v", cfg.BudgetViolations, err)
	}
}

func TestReconcilerDefaultsToJudge(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: gemma4:31b
  api_key_env: OLLAMA_API_KEY
`)
	t.Setenv("OLLAMA_API_KEY", "secret")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Reconciler.Provider != "openai" || cfg.Reconciler.Model != "gemma4:31b" {
		t.Fatalf("reconciler should default to judge: %+v", cfg.Reconciler)
	}
}

func TestReconcilerOverridesJudge(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: gemma4:31b
  api_key_env: OLLAMA_API_KEY
judge:
  provider: anthropic
  model: claude-sonnet-4
  api_key_env: ANTHROPIC_API_KEY
reconciler:
  provider: openai
  model: gemma4:31b
  api_key_env: RECONCILER_API_KEY
`)
	t.Setenv("OLLAMA_API_KEY", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("RECONCILER_API_KEY", "secret")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Reconciler.Provider != "openai" || cfg.Reconciler.Model != "gemma4:31b" {
		t.Fatalf("reconciler = %+v", cfg.Reconciler)
	}
}

func TestCreatorDefaultsThroughChain(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: gemma4:31b
  api_key_env: OLLAMA_API_KEY
`)
	t.Setenv("OLLAMA_API_KEY", "secret")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Creator.Provider != "openai" || cfg.Creator.Model != "gemma4:31b" {
		t.Fatalf("creator should default to reconciler: %+v", cfg.Creator)
	}
}

func TestCreatorOverride(t *testing.T) {
	path := writeConfig(t, `
customer:
  provider: openai
  model: gemma4:31b
  api_key_env: OLLAMA_API_KEY
creator:
  provider: anthropic
  model: claude-sonnet-4
  api_key_env: ANTHROPIC_API_KEY
`)
	t.Setenv("OLLAMA_API_KEY", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Creator.Provider != "anthropic" || cfg.Creator.Model != "claude-sonnet-4" {
		t.Fatalf("creator = %+v", cfg.Creator)
	}
	if cfg.Reconciler.Provider != "openai" {
		t.Fatalf("reconciler should stay default: %+v", cfg.Reconciler)
	}
}
