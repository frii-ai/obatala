// Package httpcontrol is the language-neutral fixture manager, registered
// as "http-control". It drives a small test-mode control API that the bot
// exposes, so a bot written in any language can have its test data seeded
// and cleaned up without writing Go:
//
//	GET    <url>/meta                          preflight; must report test_mode true
//	PUT    <url>/fixtures/{key}                seed one fixture (body: the fixture file as JSON)
//	DELETE <url>/fixtures/{key}                remove it after the run
//	DELETE <url>/identities/{identity}         remove one scenario's synthetic user
//	GET    <url>/fixtures/{key}/system-prompt  optional; enables "obatala reconcile"
//
// Fixture files are fixtures/<key>.yaml (or .yml/.json); their content is
// the bot's own schema and is sent as JSON without interpretation. See
// spec/http-adapter.md for request and response bodies.
//
// obatala.yaml:
//
//	fixtures_manager: http-control
//	fixtures_manager_settings:
//	  url: http://localhost:8080/obatala
//	  timeout: 30s
//	  headers:
//	    Authorization: "Bearer ${BOT_CONTROL_TOKEN}"
package httpcontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/internal/settings"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/report"
	"gopkg.in/yaml.v3"
)

// Name is the manager registry name.
const Name = "http-control"

// DefaultTimeout bounds one control request.
const DefaultTimeout = 30 * time.Second

const maxBody = 4 << 20

// keyPattern matches the scenario schema's fixture key rule, so every
// loaded key is one a scenario can reference.
var keyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,40}$`)

func init() {
	_ = prepare.RegisterManager(Name, New)
}

// Meta is the GET /meta response.
type Meta struct {
	TestMode      bool              `json:"test_mode"`
	Environment   string            `json:"environment"`
	AppVersion    string            `json:"app_version"`
	PromptVersion string            `json:"prompt_version"`
	BotModel      string            `json:"bot_model"`
	Extra         map[string]string `json:"extra"`
}

// SeedResponse is the optional PUT /fixtures/{key} response body. Meta is
// handed to the transport in Session.Meta (and on to the bot).
type SeedResponse struct {
	Meta map[string]string `json:"meta"`
}

// Manager implements prepare.Manager and prepare.PromptFetcher.
type Manager struct {
	base    string
	headers map[string]string
	client  *http.Client
}

var _ prepare.PromptFetcher = (*Manager)(nil)

// New builds the manager from fixtures_manager_settings.
func New(cfg config.Config) (prepare.Manager, error) {
	m := cfg.FixturesManagerSettings
	base, err := settings.Required(m, Name, "url")
	if err != nil {
		return nil, fmt.Errorf("%w (under fixtures_manager_settings)", err)
	}
	timeout, err := settings.Duration(m, Name, "timeout", DefaultTimeout)
	if err != nil {
		return nil, err
	}
	headers, err := settings.Headers(m, Name, "headers")
	if err != nil {
		return nil, err
	}
	return &Manager{
		base:    strings.TrimRight(base, "/"),
		headers: headers,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

// Preflight fetches /meta and refuses anything not in test mode or
// reporting a production environment.
func (c *Manager) Preflight(ctx context.Context) (report.RunMeta, error) {
	var meta Meta
	if _, err := c.do(ctx, http.MethodGet, "/meta", nil, &meta); err != nil {
		return report.RunMeta{}, fmt.Errorf("%s: meta: %w", Name, err)
	}
	if !meta.TestMode {
		return report.RunMeta{}, fmt.Errorf("%s: refusing to run: /meta reports test_mode false", Name)
	}
	if strings.EqualFold(meta.Environment, "production") {
		return report.RunMeta{}, fmt.Errorf("%s: refusing to run: /meta reports environment production", Name)
	}
	return report.RunMeta{
		Environment:   meta.Environment,
		AppVersion:    meta.AppVersion,
		PromptVersion: meta.PromptVersion,
		BotModel:      meta.BotModel,
		TestMode:      true,
		Extra:         meta.Extra,
	}, nil
}

// fixtureSet maps keys to their JSON documents.
type fixtureSet map[string]json.RawMessage

func (f fixtureSet) Keys() []string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LoadFixtures reads every .yaml/.yml/.json file in dir (not recursive).
// A missing dir is an empty set, for bots without test data.
func (c *Manager) LoadFixtures(dir string) (prepare.FixtureSet, error) {
	set := fixtureSet{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: read fixtures: %w", Name, err)
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ext)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%s: fixture file %s: key %q must match %s", Name, e.Name(), key, keyPattern)
		}
		if _, dup := set[key]; dup {
			return nil, fmt.Errorf("%s: fixture key %q defined twice", Name, key)
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: read fixture %s: %w", Name, path, err)
		}
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil { // YAML is a superset of JSON
			return nil, fmt.Errorf("%s: parse fixture %s: %w", Name, path, err)
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: fixture %s is not JSON-representable: %w", Name, path, err)
		}
		set[key] = body
	}
	return set, nil
}

// Seed PUTs the fixture document and returns the bot's session meta.
func (c *Manager) Seed(ctx context.Context, key string, fx prepare.FixtureSet) (map[string]string, error) {
	set, ok := fx.(fixtureSet)
	if !ok {
		return nil, fmt.Errorf("%s: unexpected fixture set type %T", Name, fx)
	}
	doc, ok := set[key]
	if !ok {
		return nil, fmt.Errorf("%s: unknown fixture %q", Name, key)
	}
	var resp SeedResponse
	if _, err := c.do(ctx, http.MethodPut, "/fixtures/"+url.PathEscape(key), doc, &resp); err != nil {
		return nil, fmt.Errorf("%s: seed %q: %w", Name, key, err)
	}
	return resp.Meta, nil
}

// NewIdentity returns "obatala-" plus 12 random hex characters.
func (c *Manager) NewIdentity() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("%s: identity: %w", Name, err)
	}
	return "obatala-" + hex.EncodeToString(b), nil
}

// Teardown deletes identities, then fixtures. 404 counts as already gone.
func (c *Manager) Teardown(ctx context.Context, perRunIDs []string, perSuiteKeys []string) error {
	var errs []error
	for _, id := range perRunIDs {
		if err := c.del(ctx, "/identities/"+url.PathEscape(id)); err != nil {
			errs = append(errs, fmt.Errorf("%s: delete identity %q: %w", Name, id, err))
		}
	}
	for _, key := range perSuiteKeys {
		if err := c.del(ctx, "/fixtures/"+url.PathEscape(key)); err != nil {
			errs = append(errs, fmt.Errorf("%s: delete fixture %q: %w", Name, key, err))
		}
	}
	return errors.Join(errs...)
}

// SystemPrompt renders the bot's prompt for a seeded fixture. Bots that
// do not implement the endpoint answer 404, which reads as unsupported.
func (c *Manager) SystemPrompt(ctx context.Context, key string) (string, error) {
	var out struct {
		SystemPrompt string `json:"system_prompt"`
	}
	status, err := c.do(ctx, http.MethodGet, "/fixtures/"+url.PathEscape(key)+"/system-prompt", nil, &out)
	if status == http.StatusNotFound {
		return "", fmt.Errorf("%s: the bot does not implement GET /fixtures/{key}/system-prompt", Name)
	}
	if err != nil {
		return "", fmt.Errorf("%s: system prompt %q: %w", Name, key, err)
	}
	if out.SystemPrompt == "" {
		return "", fmt.Errorf("%s: system prompt %q: empty system_prompt", Name, key)
	}
	return out.SystemPrompt, nil
}

func (c *Manager) del(ctx context.Context, path string) error {
	status, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// do sends one request. out may be nil; an empty 2xx body is fine.
// Response bodies are never echoed into errors.
func (c *Manager) do(ctx context.Context, method, path string, body []byte, out any) (int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: invalid JSON response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}
