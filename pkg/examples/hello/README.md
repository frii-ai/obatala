# hello — a complete Obatala bot in one package

Every role in this example is a **real AI call**:

| role        | who plays it                                            |
|-------------|---------------------------------------------------------|
| the bot     | an LLM behind the `inproc` transport                    |
| the user    | Obatala's LLM customer simulator                        |
| the judge   | Obatala's LLM judge grading against scenario criteria   |

The transport and the in-memory fixture manager self-register from
`init`, so there is no backend to run — only an LLM API key. This is
the smallest useful demonstration of what Obatala does: *your bot,
an AI user, a verdict*.

## Run it

The default config targets Ollama's OpenAI-compatible API
(`gemma4:31b`); swap `base_url`/`model`/`api_key_env` in `obatala.yaml`
for OpenAI, Anthropic, or any local endpoint.

```sh
cd pkg/examples/hello
echo 'OLLAMA_API_KEY=...' > .env   # or export it; .env beside obatala.yaml is loaded automatically
go run . validate
go run . run --report json,html
```

Expect `PASS  asks-pricing`, turn latencies, and token totals; exit
code 0 on pass, 1 on regression, 2 on infra error.

## Test it

```sh
OLLAMA_API_KEY=... go test .
```

Without the key the test skips, so `go test ./...` stays green offline.
The CI `e2e` job sets the key and runs it live.

## What's here

- `bot.go` — the LLM bot as an `inproc` handler: `Reply` generates the
  answer; per-session history keyed by session id, created and dropped by
  the `OnOpen`/`OnClose` hooks.
- `main.go` — the whole CLI in one call to `cli.Main`; your own bot's
  main looks the same plus a blank import of its adapter.
- `manager.go` — in-memory fixture manager (the seam a real adapter
  uses for a backend's data store).
- `obatala.yaml` — engine config: bot settings under
  `transport.settings`, customer/judge providers.
- `scenarios/asks-pricing.yaml` — the test itself: strategy + pass/fail
  criteria, no code.
- `fixtures/demo.yaml` — fixture data loaded through the manager.

See `pkg/obatala/README.md` for the engine documentation.
