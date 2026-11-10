package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnv reads a .env file beside the config file (or in the working
// directory when no config path is given) and exports its variables into
// the process environment. Variables already set (shell, CI secrets) are
// never overridden — real environment always wins. A missing .env is fine.
//
// Format: KEY=VALUE per line; blank lines and # comments are skipped; an
// optional `export ` prefix and matching single/double quotes around the
// value are accepted. Inline comments are not stripped from unquoted
// values.
func loadDotEnv(configPath string) error {
	dir := "."
	if configPath != "" {
		dir = filepath.Dir(configPath)
	}
	path := filepath.Join(dir, ".env")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("config: %s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("config: %s:%d: empty key", path, lineNo)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("config: set %s from %s: %w", key, path, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	return nil
}
