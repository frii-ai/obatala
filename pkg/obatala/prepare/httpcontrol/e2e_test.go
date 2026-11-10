package httpcontrol_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala"
	"github.com/frii-ai/obatala/pkg/obatala/fakes"
	"github.com/frii-ai/obatala/pkg/obatala/llm"
	_ "github.com/frii-ai/obatala/pkg/obatala/prepare/stateless"
	"github.com/frii-ai/obatala/pkg/obatala/transport/httpsync"
)

// End-to-end: the full engine against a bot speaking the HTTP adapter
// protocol (transport "http" + manager "http-control", or "none").

func init() {
	_ = llm.Register("e2e-customer", func(map[string]any) (llm.Completion, error) {
		return fakes.NewCustomer("What do your plans cost?"), nil
	})
	_ = llm.Register("e2e-judge", func(map[string]any) (llm.Completion, error) {
		return fakes.NewVerdict("success", "states $10 per seat"), nil
	})
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// referenceBot is the HTTP adapter protocol implemented the way a bot in
// any language would: a message endpoint plus the control API.
type referenceBot struct {
	mu       sync.Mutex
	seeded   map[string]bool
	sessions map[string]string // session_id -> fixture
	calls    []string
}

func (b *referenceBot) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		var req httpsync.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		b.mu.Lock()
		b.sessions[req.SessionID] = req.Fixture
		ok := req.Fixture == "" || b.seeded[req.Fixture]
		b.mu.Unlock()
		if !ok {
			http.Error(w, "fixture not seeded", http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(httpsync.Response{Replies: []httpsync.Reply{{Text: "Plans start at $10 per seat."}}})
	})
	mux.HandleFunc("GET /control/meta", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"test_mode":true,"environment":"local","bot_model":"reference"}`))
	})
	mux.HandleFunc("PUT /control/fixtures/{key}", func(w http.ResponseWriter, r *http.Request) {
		b.record(r)
		b.mu.Lock()
		b.seeded[r.PathValue("key")] = true
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /control/fixtures/{key}", func(w http.ResponseWriter, r *http.Request) {
		b.record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /control/identities/{id}", func(w http.ResponseWriter, r *http.Request) {
		b.record(r)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (b *referenceBot) record(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/control"))
}

func writeHTTPProject(t *testing.T, cfg string, withFixture bool) string {
	t.Helper()
	root := t.TempDir()
	scenario := `id: pricing
name: Pricing
customer:
  description: A team lead
  strategy: Ask what plans cost, then stop.
  max_turns: 1
criteria:
  success:
    - The bot states a per-seat price
`
	if withFixture {
		scenario = "fixture: demo-store\n" + scenario
		mustMkdir(t, filepath.Join(root, "fixtures"))
		writeFile(t, filepath.Join(root, "fixtures", "demo-store.yaml"), "name: Demo\n")
	}
	mustMkdir(t, filepath.Join(root, "scenarios"))
	writeFile(t, filepath.Join(root, "scenarios", "pricing.yaml"), scenario)
	writeFile(t, filepath.Join(root, "obatala.yaml"), cfg+`
customer:
  provider: e2e-customer
  model: fake
judge:
  provider: e2e-judge
  model: fake
`)
	return root
}

func runHTTPProject(t *testing.T, root string) int {
	t.Helper()
	_, code, err := obatala.Execute(context.Background(), obatala.Options{
		ConfigPath:  filepath.Join(root, "obatala.yaml"),
		SuiteDir:    filepath.Join(root, "scenarios"),
		FixturesDir: filepath.Join(root, "fixtures"),
	})
	if err != nil {
		t.Logf("execute: %v", err)
	}
	return code
}

func TestHTTPAdapterEndToEnd(t *testing.T) {
	bot := &referenceBot{seeded: map[string]bool{}, sessions: map[string]string{}}
	srv := httptest.NewServer(bot.handler())
	defer srv.Close()
	root := writeHTTPProject(t, `transport:
  name: http
  settings:
    url: `+srv.URL+`/messages
fixtures_manager: http-control
fixtures_manager_settings:
  url: `+srv.URL+`/control
`, true)
	t.Chdir(root)

	if code := runHTTPProject(t, root); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	calls := strings.Join(bot.calls, "|")
	if !strings.HasPrefix(calls, "PUT /fixtures/demo-store|DELETE /identities/obatala-") || !strings.HasSuffix(calls, "|DELETE /fixtures/demo-store") {
		t.Fatalf("control calls = %v", bot.calls)
	}
	if len(bot.sessions) != 1 {
		t.Fatalf("sessions = %v", bot.sessions)
	}
}

func TestHTTPAdapterBotDownIsError(t *testing.T) {
	bot := &referenceBot{seeded: map[string]bool{}, sessions: map[string]string{}}
	srv := httptest.NewServer(bot.handler())
	defer srv.Close()
	root := writeHTTPProject(t, `transport:
  name: http
  settings:
    url: http://127.0.0.1:1/messages
fixtures_manager: http-control
fixtures_manager_settings:
  url: `+srv.URL+`/control
`, true)
	t.Chdir(root)
	if code := runHTTPProject(t, root); code != 2 {
		t.Fatalf("exit = %d, want 2 (infrastructure error, not a regression)", code)
	}
}

func TestStatelessManagerEndToEnd(t *testing.T) {
	bot := &referenceBot{seeded: map[string]bool{}, sessions: map[string]string{}}
	srv := httptest.NewServer(bot.handler())
	defer srv.Close()
	root := writeHTTPProject(t, `transport:
  name: http
  settings:
    url: `+srv.URL+`/messages
fixtures_manager: none
`, false)
	t.Chdir(root)
	if code := runHTTPProject(t, root); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures")); err == nil {
		t.Fatal("no fixtures dir expected")
	}
}
