package httpcontrol_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/config"
	"github.com/frii-ai/obatala/pkg/obatala/prepare"
	"github.com/frii-ai/obatala/pkg/obatala/prepare/httpcontrol"
)

// control is a stub control API recording calls.
type control struct {
	mu        sync.Mutex
	meta      string
	calls     []string
	seeded    map[string]any
	auth      string
	failPaths map[string]int
}

func (c *control) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls = append(c.calls, r.Method+" "+r.URL.Path)
		c.auth = r.Header.Get("Authorization")
		if code := c.failPaths[r.Method+" "+r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		switch {
		case r.URL.Path == "/meta":
			_, _ = io.WriteString(w, c.meta)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/fixtures/"):
			var doc any
			_ = json.NewDecoder(r.Body).Decode(&doc)
			c.seeded[strings.TrimPrefix(r.URL.Path, "/fixtures/")] = doc
			_, _ = io.WriteString(w, `{"meta":{"tenant_token":"tok-1"}}`)
		case strings.HasSuffix(r.URL.Path, "/system-prompt"):
			_, _ = io.WriteString(w, `{"system_prompt":"You are a helpful bot."}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newManager(t *testing.T, c *control) *httpcontrol.Manager {
	t.Helper()
	if c.meta == "" {
		c.meta = `{"test_mode":true,"environment":"local","bot_model":"m1","extra":{"region":"eu"}}`
	}
	c.seeded = map[string]any{}
	srv := c.server(t)
	t.Setenv("BOT_CONTROL_TOKEN", "ctl")
	m, err := httpcontrol.New(config.Config{FixturesManagerSettings: map[string]any{
		"url":     srv.URL + "/",
		"headers": map[string]any{"Authorization": "Bearer ${BOT_CONTROL_TOKEN}"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*httpcontrol.Manager)
}

func TestPreflight(t *testing.T) {
	c := &control{}
	m := newManager(t, c)
	meta, err := m.Preflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.Environment != "local" || meta.BotModel != "m1" || !meta.TestMode || meta.Extra["region"] != "eu" {
		t.Fatalf("meta = %+v", meta)
	}
	if c.auth != "Bearer ctl" {
		t.Fatalf("auth = %q", c.auth)
	}
}

func TestPreflightRefusesUnsafeBackends(t *testing.T) {
	for _, meta := range []string{
		`{"test_mode":false,"environment":"local"}`,
		`{"test_mode":true,"environment":"Production"}`,
		`not json`,
	} {
		m := newManager(t, &control{meta: meta})
		if _, err := m.Preflight(context.Background()); err == nil {
			t.Errorf("meta %s: expected refusal", meta)
		}
	}
}

func TestLoadSeedTeardown(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("demo-store.yaml", "name: Demo\nplans:\n  - seats: 1\n    price: 10\n")
	write("other.json", `{"name":"Other"}`)
	write("README.md", "ignored")

	c := &control{}
	m := newManager(t, c)
	fx, err := m.LoadFixtures(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fx.Keys(), ","); got != "demo-store,other" {
		t.Fatalf("keys = %s", got)
	}
	meta, err := m.Seed(context.Background(), "demo-store", fx)
	if err != nil || meta["tenant_token"] != "tok-1" {
		t.Fatalf("meta=%v err=%v", meta, err)
	}
	doc, _ := json.Marshal(c.seeded["demo-store"])
	if string(doc) != `{"name":"Demo","plans":[{"price":10,"seats":1}]}` {
		t.Fatalf("seeded = %s", doc)
	}
	c.failPaths = map[string]int{"DELETE /identities/gone": http.StatusNotFound}
	if err := m.Teardown(context.Background(), []string{"obatala-1", "gone"}, []string{"demo-store"}); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	want := []string{"DELETE /identities/obatala-1", "DELETE /identities/gone", "DELETE /fixtures/demo-store"}
	got := c.calls[len(c.calls)-3:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", got)
	}
}

func TestTeardownJoinsFailures(t *testing.T) {
	c := &control{}
	m := newManager(t, c)
	c.failPaths = map[string]int{"DELETE /identities/a": 500, "DELETE /fixtures/k": 500}
	err := m.Teardown(context.Background(), []string{"a", "b"}, []string{"k"})
	if err == nil || !strings.Contains(err.Error(), `identity "a"`) || !strings.Contains(err.Error(), `fixture "k"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadFixturesRules(t *testing.T) {
	m := newManager(t, &control{})
	if fx, err := m.LoadFixtures(filepath.Join(t.TempDir(), "missing")); err != nil || len(fx.Keys()) != 0 {
		t.Fatalf("missing dir: keys=%v err=%v", fx, err)
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Bad Key.yaml"), []byte("a: 1"), 0o600)
	if _, err := m.LoadFixtures(dir); err == nil {
		t.Fatal("bad key should error")
	}
	dir = t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "dup.yaml"), []byte("a: 1"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "dup.json"), []byte(`{"a":1}`), 0o600)
	if _, err := m.LoadFixtures(dir); err == nil {
		t.Fatal("duplicate key should error")
	}
}

func TestSystemPrompt(t *testing.T) {
	c := &control{}
	m := newManager(t, c)
	var pf prepare.PromptFetcher = m
	p, err := pf.SystemPrompt(context.Background(), "demo")
	if err != nil || p != "You are a helpful bot." {
		t.Fatalf("p=%q err=%v", p, err)
	}
	c.failPaths = map[string]int{"GET /fixtures/demo/system-prompt": 404}
	if _, err := m.SystemPrompt(context.Background(), "demo"); err == nil || !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("err = %v", err)
	}
}

func TestIdentitiesAreUnique(t *testing.T) {
	m := newManager(t, &control{})
	a, _ := m.NewIdentity()
	b, _ := m.NewIdentity()
	if a == b || !strings.HasPrefix(a, "obatala-") {
		t.Fatalf("a=%s b=%s", a, b)
	}
}

func TestRequiresURL(t *testing.T) {
	if _, err := httpcontrol.New(config.Config{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := prepare.GetManager("http-control"); err != nil {
		t.Fatal(err)
	}
}
