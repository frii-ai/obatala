package report

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func init() {
	_ = Register("json", newJSON)
}

func newJSON(settings map[string]any) (Reporter, error) {
	return &fileReporter{
		path:   settingPath(settings, "reports/report.json"),
		render: renderJSON,
	}, nil
}

func renderJSON(run RunSummary) ([]byte, error) {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("report: marshal json: %w", err)
	}
	return append(data, '\n'), nil
}

// fileReporter renders a run and writes it to a file, creating parent
// directories. Reporters are settings-driven (obatala.yaml) so the CLI can
// compose several per invocation (--report json,junit,md,html).
type fileReporter struct {
	path   string
	render func(RunSummary) ([]byte, error)
}

func (f *fileReporter) Report(_ context.Context, run RunSummary) error {
	data, err := f.render(run)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(f.path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("report: %w", err)
		}
	}
	if err := os.WriteFile(f.path, data, 0o644); err != nil {
		return fmt.Errorf("report: write %s: %w", f.path, err)
	}
	return nil
}

// Path reports where the reporter wrote (for CLI logging).
func (f *fileReporter) Path() string { return f.path }

// settingPath reads a string setting with a default.
func settingPath(settings map[string]any, def string) string {
	if s, _ := settings["path"].(string); s != "" {
		return s
	}
	return def
}
