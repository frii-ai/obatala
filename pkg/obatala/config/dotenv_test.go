package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/config"
)

// minimalConfig satisfies validation without secrets.
const minimalConfig = `customer:
  provider: openai
  model: m
`

func TestDotEnvLoadsBesideConfig(t *testing.T) {
	dir := t.TempDir()
	dotenv := strings.Join([]string{
		"# comment line",
		"",
		"OBATALA_DOTENV_PLAIN=plain-value",
		`OBATALA_DOTENV_QUOTED="quoted value with spaces"`,
		"OBATALA_DOTENV_SINGLE='single quoted'",
		"export OBATALA_DOTENV_EXPORT=exported",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "obatala.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Config must load; .env vars must be exported by then (validation of
	// api_key_env happens inside Load, so export a full config below).
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("load: %v", err)
	}
	for key := range map[string]string{
		"OBATALA_DOTENV_PLAIN":  "",
		"OBATALA_DOTENV_QUOTED": "",
		"OBATALA_DOTENV_SINGLE": "",
		"OBATALA_DOTENV_EXPORT": "",
	} {
		key := key
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	for key, want := range map[string]string{
		"OBATALA_DOTENV_PLAIN":  "plain-value",
		"OBATALA_DOTENV_QUOTED": "quoted value with spaces",
		"OBATALA_DOTENV_SINGLE": "single quoted",
		"OBATALA_DOTENV_EXPORT": "exported",
	} {
		if got := os.Getenv(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	t.Setenv("OBATALA_DOTENV_KEEP", "from-shell")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OBATALA_DOTENV_KEEP=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "obatala.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("OBATALA_DOTENV_KEEP"); got != "from-shell" {
		t.Fatalf("value = %q, want shell environment to win", got)
	}
}

func TestDotEnvMalformedLineFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("NOT_A_PAIR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "obatala.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(cfgPath)
	if err == nil || !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("err = %v, want KEY=VALUE parse error", err)
	}
}

func TestDotEnvMissingFileIsFine(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "obatala.yaml")
	if err := os.WriteFile(cfgPath, []byte(minimalConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(cfgPath); err != nil {
		t.Fatalf("load without .env: %v", err)
	}
}

func TestDotEnvFeedsProviderKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OBATALA_TEST_DOTENV_KEY=secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "obatala.yaml")
	yaml := `transport:
  name: webhook
customer:
  provider: openai
  model: some-model
  api_key_env: OBATALA_TEST_DOTENV_KEY
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	settings, err := cfg.Customer.Settings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings["api_key"] != "secret-from-file" {
		t.Fatalf("api_key = %v, want value from .env", settings["api_key"])
	}
}
