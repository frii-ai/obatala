# Obatala

Regression and latency tests for LLM chatbots.

Obatala has an LLM play a user who chats with your bot. After the
conversation, a second LLM judges the transcript against criteria you
write in plain language. Each test is a YAML file:

```yaml
id: asks-pricing
fixture: demo-store
customer:
  description: A team lead comparing tools for a team of five.
  strategy: Ask what plans cost for five seats, then what is included. Then say thanks.
  max_turns: 4
criteria:
  success:
    - The bot states the $10 per seat starting price
  failure:
    - The bot invents a discount
budgets:
  turn_latency_p95_ms: 30000
```

A run prints pass, fail or error for each scenario, latency percentiles
and token usage. It can also write JSON, JUnit, Markdown and a
self-contained HTML report. The exit code is made for CI:

| code | meaning |
|---|---|
| 0 | every scenario passed |
| 1 | the bot regressed: a criterion was missed or a budget was exceeded |
| 2 | infrastructure error: transport, LLM, seeding or teardown failed |

Infrastructure problems are never reported as regressions.

## Two ways to use it

**Any language, over HTTP.** Your bot exposes a message endpoint and,
if it has test data, a small control API
([spec/http-adapter.md](spec/http-adapter.md)). The `obatala` binary
does the rest. [pkg/examples/http-bot](pkg/examples/http-bot) is a
Python bot tested this way.

```sh
go install github.com/frii-ai/obatala/cmd/obatala@latest   # or download a release binary
obatala validate
obatala run --report json,html
```

**From Go.** Plug your bot into three seams and run the suite from
`go test` or your own CLI:

- a **Transport** carries messages to the bot and replies back
  (in-process, HTTP webhook and Redis outbox building blocks included);
- a **Manager** seeds test data before the run and tears it down after;
- **LLM providers** play the customer and the judge (OpenAI-compatible
  and Anthropic built in).

```go
func TestRegression(t *testing.T) {
    obatala.Run(t)
}
```

[pkg/examples/hello](pkg/examples/hello) is a complete Go example.

## Configuration

`obatala.yaml` names the transport, the fixture manager and the LLMs:

```yaml
transport:
  name: http
  settings:
    url: http://127.0.0.1:8080/obatala/messages
fixtures_manager: http-control
fixtures_manager_settings:
  url: http://127.0.0.1:8080/obatala
customer:
  provider: openai              # any OpenAI-compatible endpoint, or anthropic
  model: gpt-4.1-mini
  api_key_env: OPENAI_API_KEY   # the key itself lives in the environment or .env
concurrency: 4
budget_violations: fatal        # or warn
```

A `.env` file beside the config is loaded automatically; real
environment variables win.

## Commands

| command | does |
|---|---|
| `obatala run` | run the suite (`--tag`, `--id`, `--report json,junit,md,html`) |
| `obatala list` | list scenarios |
| `obatala validate` | check scenarios, fixtures and config without calling anything |
| `obatala create` | draft a scenario from a real chat transcript |
| `obatala reconcile` | suggest system-prompt edits for a failing scenario |

## Documentation

- [Writing scenarios](pkg/obatala/docs/writing-scenarios.md)
- [Engine overview and seams](pkg/obatala/README.md)
- [Writing your own adapter](pkg/obatala/docs/write-your-own-adapter.md),
  [adding a transport](pkg/obatala/docs/how-to-add-a-transport.md),
  [adding an LLM provider](pkg/obatala/docs/how-to-add-a-provider.md)
- [Specification](spec/SPEC.md): what any Obatala implementation must
  do, with JSON Schemas and a [conformance suite](spec/conformance)

## License

MIT. The customer and judge prompts are adapted from
[langwatch/scenario-go](https://github.com/langwatch/scenario-go) (MIT).
