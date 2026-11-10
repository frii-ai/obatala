# Obatala specification

`spec_version: 1`

This document defines what any Obatala implementation must do so that
scenario files, reports and CI wiring work the same everywhere. The Go
engine in this repository is the reference implementation. A native
implementation in another language (for example one written with the
Obatala skill) conforms when it meets every MUST below and passes the
checks in [`conformance/`](conformance/).

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.
Machine-readable schemas live in [`schemas/`](schemas/). The HTTP protocol
for bots tested by the Go binary is [http-adapter.md](http-adapter.md).

## 1. Core loop

For each scenario, an implementation:

1. gives the scenario a fresh session (a unique session id and a unique
   synthetic identity) and seeds its fixture if it has one;
2. asks the **customer** LLM for a first message;
3. repeats `max_turns` times: send the customer message to the bot,
   collect the bot's replies for that turn, then either ask the customer
   LLM for the next message or, after the last turn, ask the **judge** LLM
   for a verdict;
4. maps the verdict and budgets to an outcome;
5. after all scenarios, tears down what it created and writes reports.

The bot is the system under test. The customer and the judge are LLMs the
implementation calls; they MAY be the same model.

## 2. Scenario files

A suite is a directory. Every `*.yaml` or `*.yml` file under it,
recursively, is one scenario. The schema is
[`schemas/scenario.schema.json`](schemas/scenario.schema.json).

| field | type | rule |
|---|---|---|
| `id` | string | REQUIRED. `^[a-z0-9][a-z0-9-]{1,63}$`. Unique across the suite. |
| `name` | string | Human title. |
| `tags` | string[] | Used by tag filters. |
| `fixture` | string | `^[a-z0-9][a-z0-9-]{2,40}$`. See §3. |
| `customer.description` | string | REQUIRED, non-empty. The persona. |
| `customer.strategy` | string | REQUIRED, non-empty. What the customer tries. |
| `customer.max_turns` | integer ≥ 0 | Customer messages to send. |
| `customer.allow_urls` | boolean | Default false. See §5. |
| `criteria.success` | string[] | REQUIRED, at least one, none blank. |
| `criteria.failure` | string[] | Optional. |
| `budgets.max_turns` | integer ≥ 0 | Optional cap on turns. |
| `budgets.turn_latency_p95_ms` | integer ≥ 0 | Optional latency budget. |
| `repeat`, `pass_threshold` | integer | Reserved. If `repeat` > 0, `pass_threshold` MUST be > 0. |

- Implementations MUST reject a suite with a schema violation, a duplicate
  `id`, or no scenario files, before any scenario runs (exit code 2).
- Unknown fields SHOULD be ignored.
- The **group** of a scenario is its file's directory relative to the
  suite root, with `/` separators, or empty at the root. It is derived,
  never read from YAML, and used only for reporting.
- Scenarios run in `id` order when run one at a time.
- **Effective turns**: start with `customer.max_turns`. If
  `budgets.max_turns` is positive and smaller (or `customer.max_turns` is
  0), use it. If the result is 0, use 10.

## 3. Fixtures

A fixture is named test data the bot needs (a store, an account, a
document). Its file format belongs to the bot; implementations only need
its key.

- If the suite's fixture set is non-empty, every selected scenario MUST
  name an existing key in `fixture`.
- If the fixture set is empty, scenarios MUST NOT set `fixture`.
- Violations are setup errors (exit code 2) found before any scenario
  runs.
- Each fixture referenced by the selected scenarios is seeded once per
  run, before scenarios start. Seeding can return string **meta** that is
  passed to the bot with every message of scenarios using that fixture.
- Teardown runs after every run, whatever the outcomes: per-scenario
  identities first, then fixtures. A teardown failure turns the run's exit
  code into 2.
- Before seeding, implementations SHOULD run a **preflight** that refuses
  to touch a backend that is not in test mode.

## 4. Turns and replies

- A turn is one customer message plus every bot reply collected for it.
- For bots that reply asynchronously, collection waits up to the **reply
  timeout** (default 90s) for the first reply, then keeps collecting until
  the **settle window** (default 4s) passes with no new reply.
- A turn with no bot reply is an infrastructure error.
- Each turn records: the customer text, the bot texts, the time the
  message was sent, the time of the first and last reply, and the reply
  count. **First-reply latency** is first reply minus sent time.

## 5. The customer

- The customer LLM is prompted with the scenario's description, strategy
  and criteria. The reference prompt is in Appendix A.
- Temperature SHOULD be 0.
- The conversation passed to the customer is: the system prompt, a fixed
  assistant greeting `Hello, how can I help you today?`, then the
  dialogue so far. Customer messages have role `user` and bot replies have
  role `assistant` (the bot sits in the assistant seat).
- **URL guard**: unless `customer.allow_urls` is true, a customer message
  matching `https?://\S+` MUST be rejected and regenerated. The customer
  invents links the bot never sent (payment links, for example), which
  produces false passes.
- Empty customer messages MUST be regenerated.
- Regeneration is retried up to `retry_attempts` (config, default 5) with
  backoff. Exhausting retries is an infrastructure error.

## 6. The judge and the verdict

After the last turn the judge LLM receives the reference judge prompt
(Appendix A), the same greeting and dialogue, and a final user message
(Appendix B). It MUST be required to call exactly one tool,
`finish_test`, whose arguments follow
[`schemas/verdict.schema.json`](schemas/verdict.schema.json):

```json
{
  "verdict": "success | failure | inconclusive",
  "reasoning": "why",
  "details": {
    "met_criteria": ["..."],
    "unmet_criteria": ["..."],
    "triggered_failures": ["..."]
  }
}
```

- A missing call, more than one call, a different tool, or arguments
  without a string `verdict`, a string `reasoning` and an object
  `details` are degenerate responses: retry as in §5, then
  infrastructure error. A `verdict` outside the enum is an
  infrastructure error.
- Tools MUST NOT be offered to the customer LLM, and a tool call from the
  customer is degenerate.
- `success` maps to outcome `pass`. `failure` and `inconclusive` map to
  `fail`.

## 7. Budgets

- `budgets.turn_latency_p95_ms`: the p95 of the scenario's first-reply
  latencies is compared to the budget. If it is higher, a violation
  message is recorded in `budget_violations`.
- With `budget_violations: fatal` (the default) a violation turns a `pass`
  into `fail`. With `warn` it is recorded only.

## 8. Outcomes and exit codes

| outcome | meaning |
|---|---|
| `pass` | the judge said success and no fatal budget violation |
| `fail` | the bot misbehaved: failure or inconclusive verdict, or fatal budget violation |
| `error` | infrastructure: transport, LLM, open/close session failures |

Infrastructure problems MUST NEVER be reported as `fail`.

Process exit code:

| code | when |
|---|---|
| 0 | every scenario passed |
| 1 | at least one `fail` and no `error` |
| 2 | any `error`, or a setup, preflight, seeding or teardown failure |

## 9. Report

Implementations MUST be able to write a JSON report that validates
against [`schemas/report.schema.json`](schemas/report.schema.json)
(`report_version` 3). Durations are integer nanoseconds (`*_ns`);
timestamps are RFC 3339.

- `meta`: preflight metadata (environment, versions, bot model,
  `test_mode`) plus free-form string `extra`.
- `latency`: p50/p95/max of first- and last-reply latency across all
  turns, and the turn count. Per-scenario `latency` is the same over that
  scenario's turns.
- Per scenario: `id`, `outcome`, `turns_used`, `duration_ns`, `latency`
  are always present. The rest (judge reasoning and criteria lists,
  `budget_violations`, `error`, `turns` timings, `token_usage`, the full
  `transcript` as `customer: …` / `bot: …` lines, and a head/tail excerpt)
  are present when they have content.
- `report_version` changes only on breaking changes. Consumers MUST key
  on it; additive fields do not bump it.

A console summary SHOULD always be printed. Other formats (JUnit,
Markdown, HTML) are optional.

## 10. Configuration

Implementations SHOULD read `obatala.yaml` with at least: the transport
and its settings, the fixtures manager and its settings, `customer` and
`judge` LLM settings (`judge` defaults to `customer`), `concurrency`,
`reply_timeout`, `settle_window`, `retry_attempts` and
`budget_violations`. Secrets MUST NOT be stored in the config: name an
environment variable instead (`api_key_env`, or `${VAR}` in header
values), and SHOULD load a `.env` file beside the config, with real
environment variables taking precedence.

## Appendix A: reference prompts

The Go engine uses these prompts, adapted from langwatch/scenario-go
v1.0.0 (MIT). `{{.Description}}`, `{{.Strategy}}`,
`{{.SuccessCriteriaJSON}}` and `{{.FailureCriteriaJSON}}` are the
scenario's fields, with criteria as indented JSON arrays. Native
implementations SHOULD use them verbatim so results are comparable.

### Customer

```text
<role>
You are pretending to be a user, you are testing an AI Agent (shown as the user role) based on a scenario.
Approach this naturally, as a human user would, with very short inputs, few words, all lowercase, imperative, not periods, like when they google or talk to chatgpt.
</role>

<goal>
Your goal (assistant) is to interact with the Agent Under Test (user) as if you were a human user to see if it can complete the scenario successfully.
</goal>

<scenario>
{{.Description}}
</scenario>

<strategy>
{{.Strategy}}
</strategy>

<success_criteria>
{{.SuccessCriteriaJSON}}
</success_criteria>

<failure_criteria>
{{.FailureCriteriaJSON}}
</failure_criteria>

<execution_flow>
1. Generate the first message to start the scenario
2. After the Agent Under Test (user) responds, generate the next message to send to the Agent Under Test, keep repeating step 2
</execution_flow>

<rules>
1. You must always reply with the next message text — never an empty reply, never a test summary, never an evaluation. You cannot end the conversation; you only decide what the user says next.
2. DO NOT make any judgment calls that are not explicitly listed in the success or failure criteria, withhold judgement if necessary.
3. DO NOT carry over any requests yourself, YOU ARE NOT the assistant today, wait for the user to do it.
4. If the scenario goals are achieved, the user may say a short closing like "ok, thanks!" — keep it natural.
5. You are the user. NEVER write or invent links of any kind — no payment, login, tracking, download or order links — the agent provides those and you receive them. Never invent URLs.
</rules>
```

### Judge

```text
<role>
You are pretending to be a user, you are testing an AI Agent (shown as the user role) based on a scenario.
Approach this naturally, as a human user would, with very short inputs, few words, all lowercase, imperative, not periods, like when they google or talk to chatgpt.
</role>

<goal>
Your goal (assistant) is to interact with the Agent Under Test (user) as if you were a human user to see if it can complete the scenario successfully.
</goal>

<scenario>
{{.Description}}
</scenario>

<strategy>
{{.Strategy}}
</strategy>

<success_criteria>
{{.SuccessCriteriaJSON}}
</success_criteria>

<failure_criteria>
{{.FailureCriteriaJSON}}
</failure_criteria>

<execution_flow>
1. Generate the first message to start the scenario
2. After the Agent Under Test (user) responds, generate the next message to send to the Agent Under Test, keep repeating step 2 until the criteria match
3. If the test should end, use the finish_test tool to determine if success or failure criteria have been met
</execution_flow>

<rules>
1. Test should end immediately if a failure criteria is triggered
2. Test should continue until all success criteria have been met
3. DO NOT make any judgment calls that are not explicitly listed in the success or failure criteria, withhold judgement if necessary
4. DO NOT carry over any requests yourself, YOU ARE NOT the assistant today, wait for the user to do it
</rules>
```

The `finish_test` tool is offered with description `Complete the test
with a final verdict` and the parameters in `schemas/verdict.schema.json`.

## Appendix B: final judge message

Appended as a `user` message after the dialogue:

```text

System:

<finish_test>
This is the last message, conversation has reached the maximum number of turns, give your final verdict,
if you don't have enough information to make a verdict, say inconclusive with max turns reached.
</finish_test>
```

## Appendix C: provider quirks

Some OpenAI-compatible relays reject a conversation with no `user`
message. When the conversation has none, rewrite the first `assistant`
message (the greeting) to role `user`. Implementations SHOULD also strip
tools from requests that do not require a tool, because some reasoning
models emit premature verdicts when a tool is merely offered.
