# Obatala engine

Obatala regression- and latency-tests LLM-driven bots. It simulates a user
chatting with your bot over a pluggable transport, judges the conversation
against natural-language criteria, and reports pass/fail/error plus latency
percentiles and token usage. A bot plugs in through three seams — a
**Transport** (how messages travel), a **Manager** (how test state is
seeded and torn down), and **LLM providers** (the simulated user and the
judge) — after which every test is a YAML file.

This package tree is bot-agnostic. Bot-specific code (schemas, control
planes, transports) lives in the adapter that registers implementations
into the registries below; nothing here imports an adapter.

## Quick start

Five lines in a `go test` (see `pkg/examples/hello` for a complete
runnable example):

```go
package regression_test

import (
    "testing"

    _ "example.com/mybot/adapter" // registers transport + manager
    "github.com/frii-ai/obatala/pkg/obatala"
)

func TestRegression(t *testing.T) {
    obatala.Run(t) // fails the test on any regression; exit-code style via Execute
}
```

Or from a CLI (this is what `cmd/obatala` does):

```go
summary, code, err := obatala.Execute(ctx, obatala.Options{
    ConfigPath: "obatala.yaml",
    SuiteDir:   "scenarios",
    Reports:    []string{"json", "junit"},
})
```

`Run(t)` is `Execute` wrapped for testing.TB: it logs the console report
and calls `t.Fatal` on infrastructure errors, `t.Error` on regressions.

Exit codes (also returned by `Execute`): **0** all pass, **1** any fail or
fatal budget violation, **2** any error — setup, preflight, teardown or
transport failures never masquerade as regressions.

## Architecture

```
Options ─▶ config.Load ─▶ prepare.GetManager ─▶ Manager.Preflight
                │                                     │ RunMeta
                ▼                                     ▼
        scenariodef.Load ──┐                  Manager.LoadFixtures
                            ▼                          │ FixtureSet
                    filter (tag/id)                    ▼
                            │            validate fixture references
                            └──────────────┬───────────┘
                                           ▼
                    buildTasks: Manager.Seed (per fixture key)
                                Manager.NewIdentity (per scenario)
                                           ▼
                    runner.Runner (bounded worker pool)
                        └─▶ engine.Run per task
                              transport.Open/Send/Receive/Close
                              customer LLM drives the user side
                              judge LLM returns finish_test verdict
                                           ▼
                    budgets → RunSummary → reporters (json/junit/md)
                                           ▼
                    Manager.Teardown (per-run ids, then fixture keys)
```

Key properties:

- **One orchestration path.** `Run` and the CLI both call `Execute`; there
  is no second code path to drift.
- **Registries, not imports.** Transports, managers, LLM providers and
  reporters self-register (`init` or explicit `Register`). The engine
  looks them up by the names in `obatala.yaml`.
- **Teardown always runs** — pass, fail or error — so backends are left
  clean (per-run identities first, then suite-scoped fixtures). A teardown
  failure escalates an otherwise-passing run to exit 2.
- **Latencies are runner-clock only** (send → first reply), never
  provider-reported.
- **The user simulator cannot leak roles**: customer messages are hardened
  against empty replies, phantom tool calls, and URL fabrication (see
  `engine/harden.go`); `allow_urls: true` in a scenario relaxes the URL
  guard deliberately.

## Configuration (`obatala.yaml`)

```yaml
transport:
  name: mybot                   # transport registry name
  settings:                     # opaque, passed to the transport factory
    url: http://localhost:8080/test/messages
fixtures_manager: mybot         # manager registry name (optional if one is registered)
bot_description: >-             # optional context for `create` and `reconcile`
  Support assistant for a project-management SaaS.

customer:                       # the simulated user's LLM
  provider: openai              # registry name: openai | anthropic | …
  model: gemma4:31b
  base_url: https://ollama.com/v1/   # any OpenAI-compatible endpoint
  api_key_env: OLLAMA_API_KEY        # secret lives in env (or .env beside the config)

judge:                          # verdict LLM; defaults to the customer block
  provider: openai
  model: gemma4:31b

concurrency: 4                  # parallel scenarios
reply_timeout: 120s             # per-turn wait for bot replies
settle_window: 4s               # extra wait to collect trailing replies
retry_attempts: 5               # LLM retries on degenerate responses (empty, malformed tool call, customer URL)
transcript_turns: 3             # head/tail excerpt size in reports
budget_violations: fatal        # or: warn
```

A `.env` file beside the config is loaded automatically (real environment
variables always win). Secrets never live in the YAML — only the *name* of
the env var (`api_key_env`) does.

## Scenario schema (`scenarios/**/*.yaml`)

Suites nest by directory; ids must be unique across the whole suite.
Authoring guide: [docs/writing-scenarios.md](docs/writing-scenarios.md).

```yaml
id: pricing-enquiry            # kebab-case, unique
name: Ask about pricing
tags: [smoke, pricing]
fixture: demo-store             # key in the adapter's fixture set (optional iff the set is empty)
customer:
  description: A team lead comparing tools for a team of five.
  strategy: Ask what plans cost for five seats, then ask what is included. Stop once both are answered.
  max_turns: 8
  allow_urls: false             # default; true relaxes the no-URL guard
criteria:
  success:                      # the judge checks these after the last turn
    - The bot states the per-seat starting price
  failure:                      # meeting any of these fails the scenario
    - The bot invents a discount
budgets:
  max_turns: 9
  turn_latency_p95_ms: 30000    # violations recorded; fatal unless budget_violations: warn
```

## The fixture seam

Fixtures are per-suite test data in **your adapter's schema** — products,
accounts, documents, whatever your backend needs to seed. The engine never
parses them; it only sees keys:

```go
type FixtureSet interface { Keys() []string }

type Manager interface {
    Preflight(ctx) (report.RunMeta, error)            // safety check + report metadata
    LoadFixtures(dir string) (FixtureSet, error)      // parse dir into your schema
    Seed(ctx, key string, fx FixtureSet) (map[string]string, error) // create it; return session meta
    NewIdentity() (string, error)                     // fresh synthetic actor id per scenario
    Teardown(ctx, perRunIDs []string, perSuiteKeys []string) error  // after any outcome
}
```

Rules the engine enforces: scenarios must reference an existing key when
the fixture set is non-empty, and must not reference one when it is empty.
Each referenced key is seeded once per run; the session meta returned by
`Seed` lands in `transport.Session.Meta` (e.g. webhook credentials).
`NewIdentity` output goes to `Session.Identity` and comes back through
`Teardown` — implement your backend's identity format there, or return a
UUID.

Register it under a name (or as the sole manager):

```go
func init() { _ = prepare.RegisterManager("mybot", newManager) }
```

## Transport seam

```go
type Transport interface {
    Open(ctx, Session) error                        // reserve state for this run
    Send(ctx, Session, text string) (time.Time, error)
    Receive(ctx, Session, ReceiveOpts) ([]Message, error) // 1+ replies
    Close(ctx, Session) error                       // release; always called
}
```

`Session` carries `ID` (unique per run), `Identity`, `FixtureKey`, and the
seed `Meta`. Register with `transport.Register("name", factory)`; the
factory receives `transport.settings` from the YAML unchanged.

Ready-made building blocks: `transport/inproc` (bot in the same Go
program), `transport/httpwebhook` (JSON POST sender) and `outbox` (Redis
stream reply reader). `pkg/obatala/fakes` ships a scriptable transport and
completion for offline tests. See [docs/how-to-add-a-transport.md](docs/how-to-add-a-transport.md).

## Reports

The console summary always prints. Extra reporters (`json`, `junit`, `md`,
`html`) write under `reports/`. `html` is a single self-contained
`reports/report.html` — open it in a browser or publish it as a CI
artifact; it needs no server and makes no external requests. The JSON
schema carries `report_version` (current: 3); consumers should key on it.
`meta` holds preflight metadata (environment, versions, model) plus
`extra` for adapter-defined fields.

## Packages

| Package | Role |
|---|---|
| `pkg/obatala` | `Run`/`Execute`/`Validate`/`ExitCode` — the orchestration |
| `…/engine` | one scenario: customer turns, judge verdict, hardening |
| `…/runner` | worker pool, budgets, result building |
| `…/scenariodef` | YAML schema, loading, validation, filters |
| `…/prepare` | Manager/FixtureSet registries (the adapter seam) |
| `…/transport` | Transport interface + registry |
| `…/transport/inproc` | in-process transport over a handler func |
| `…/transport/httpwebhook` | JSON POST sender for webhook-driven bots |
| `…/outbox` | Redis stream reply reader (settle window, dedupe) |
| `…/cli` | the `run`/`list`/`validate`/`reconcile`/`create` CLI as a library |
| `…/llm` | Completion interface, openai/anthropic providers, registry |
| `…/report` | RunSummary, reporters, tally |
| `…/creator`, `…/reconcile` | `create` (draft scenarios from chats), `reconcile` (prompt-edit suggestions) |
| `…/condition`, `…/metrics` | outcome types, latency aggregation |
| `…/config` | `obatala.yaml` + `.env` loading |
| `…/fakes` | offline transport/LLM fakes |
| `pkg/examples/hello` | complete minimal bot you can `go run` |
