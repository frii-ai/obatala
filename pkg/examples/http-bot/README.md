# http-bot: test a bot written in any language

`bot.py` is a small support bot in Python (standard library only). It
speaks Obatala's [HTTP adapter protocol](../../../spec/http-adapter.md), so
the `obatala` binary can test it with no Go code on the bot's side:

| file                        | what                                                        |
|-----------------------------|-------------------------------------------------------------|
| `bot.py`                    | the bot: a message endpoint plus the test-mode control API  |
| `obatala.yaml`              | transport `http`, manager `http-control`, the customer LLM  |
| `fixtures/demo-store.yaml`  | test data in the bot's own schema, seeded before the suite  |
| `scenarios/asks-pricing.yaml` | the test: persona, strategy, pass/fail criteria           |

The bot is rule-based so it runs without a model. Replace `reply()` with
your own LLM call and keep the endpoints.

## Run it

```sh
cd pkg/examples/http-bot
python3 bot.py &                     # listens on 127.0.0.1:8080
echo 'OLLAMA_API_KEY=...' > .env     # the customer simulator and judge are LLMs
go run github.com/frii-ai/obatala/cmd/obatala validate
go run github.com/frii-ai/obatala/cmd/obatala run --report json,html
```

Expect `PASS asks-pricing` and exit code 0. Open `reports/report.html` to
read the conversation.

Point `customer` in `obatala.yaml` at any OpenAI-compatible endpoint, or
use `provider: anthropic`.
