# Write your own adapter

An adapter is everything bot-specific, isolated so the generic library
(`pkg/obatala`) never imports it. Yours can live in `internal/<bot>/` (same
repo as the bot) or in its own module.
Consumers get the whole system with one blank import plus YAML — see
[how-to-add-a-scenario.md](how-to-add-a-scenario.md).

An adapter is three pieces:

## 1. A transport (how messages move)

Implement `transport.Transport` and register it —
[how-to-add-a-transport.md](how-to-add-a-transport.md). In-process is
first-class: if your bot exposes a "handle one inbound message" function,
`Send` calls it and `Receive` returns its output. Nothing forces a
webhook/outbox architecture on the runner.

## 2. A fixture manager (how state is prepared and cleaned)

`prepare.Manager` is the control-plane seam — loading and seeding test
data before scenarios and wiping everything after, regardless of outcome:

```go
type FixtureSet interface {
    Keys() []string // fixture keys the engine can reference
}

type Manager interface {
    // Preflight: verify the backend is safe to test (test mode on,
    // environment not production, version compatible) and return the
    // run metadata stamped into every report (use RunMeta.Extra for
    // adapter-specific fields).
    Preflight(ctx context.Context) (report.RunMeta, error)

    // LoadFixtures: parse the fixture directory in YOUR schema.
    // Bots without backend state may return an empty set; scenarios
    // then omit the fixture key.
    LoadFixtures(dir string) (prepare.FixtureSet, error)

    // Seed: create (or reuse) the fixture identified by key; return
    // the per-session meta your transport needs (e.g. webhook
    // credentials). Each referenced key is seeded once per run.
    Seed(ctx context.Context, key string, fx prepare.FixtureSet) (map[string]string, error)

    // NewIdentity: a fresh synthetic actor id per scenario run. The
    // engine puts it in the session and hands it back in Teardown.
    // Implement your backend's identity format here (or return a UUID).
    NewIdentity() (string, error)

    // Teardown: remove per-run identities, then suite-scoped fixtures.
    // Best-effort: attempt every id/key; join failures into one error.
    Teardown(ctx context.Context, perRunIDs []string, perSuiteKeys []string) error
}
```

Register it with your transport:

```go
func init() {
    _ = transport.Register("myservice", newTransport)
    _ = prepare.RegisterManager("myservice", newManager)
}

func newManager(cfg config.Config) (prepare.Manager, error) { /* build API client */ }
```

The fixture schema is **yours**: `LoadFixtures` parses
`fixtures/*.yaml` however your backend needs (products, accounts,
documents, …) and wraps the result in a `FixtureSet`. The engine only
ever asks for keys, validates scenario references against them, and calls
`Seed` per key. Validate domain rules (units, required fields, sane
ranges) in `LoadFixtures` so `obatala validate` catches mistakes before
anything is seeded.

## 3. Scenario + fixture YAML (the content)

`scenarios/**/*.yaml` (id, optional `fixture` key reference, customer
persona/strategy, max_turns, criteria, optional latency budgets) and
`fixtures/*.yaml` in your adapter's schema. Adding either never requires
Go — that's the point of the adapter split.

## Error taxonomy (get this right)

| failure                                   | outcome | exit |
|-------------------------------------------|---------|------|
| judge: bot violated a failure criterion   | `fail`  | 1    |
| judge: success criteria unmet             | `fail`  | 1    |
| transport non-2xx, Redis down, timeout    | `error` | 2    |
| preflight refused (test mode off, etc.)   | setup   | 2    |
| seeding failed                            | setup   | 2    |
| teardown refused/failed                   | `error` | 2    |

**Infra never masquerades as a regression.** Transport and manager
failures must return errors, never fake bot replies or judge verdicts.

## Checklist for a new bot

1. `internal/<bot>/transport` — channel adapter. Start from
   `transport/inproc` (same Go program) or `transport/httpwebhook` +
   `outbox` (webhook in, Redis stream out); see
   [how-to-add-a-transport.md](how-to-add-a-transport.md).
2. `internal/<bot>/manager` — `prepare.Manager` over your control/test API
   (fixture schema included).
3. `internal/<bot>/<bot>.go` — `init()` registering both; ignore
   `transport.ErrDuplicate`/`prepare.ErrDuplicateManager` so repeated
   imports are safe.
4. Fixtures + scenarios (YAML, your fixture schema).
5. One 5-line `regression_test.go` (blank import + `obatala.Run(t)`).
6. Optional: a CLI — a `main` that blank-imports your adapter and calls
   `cli.Main` (see `pkg/examples/hello/main.go`). `go test` is a
   first-class runner too.

Reference implementation: `pkg/examples/hello` (live AI: an LLM bot on
the `inproc` transport and an in-memory manager — needs an API key).
