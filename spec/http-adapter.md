# Obatala HTTP adapter protocol

Version 1. This protocol lets the `obatala` binary test a bot written in
any language. The bot exposes a message endpoint and, if it has test data
to seed, a small control API. No Go code is needed on the bot's side.

The key words MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119.

Two transports and two managers implement it:

| `obatala.yaml`                  | role                                                    |
|---------------------------------|---------------------------------------------------------|
| `transport.name: http`          | synchronous: the bot answers in the HTTP response       |
| `transport.name: webhook-outbox`| asynchronous: the bot answers later via a Redis stream  |
| `fixtures_manager: http-control`| seeds and removes test data through the control API     |
| `fixtures_manager: none`        | the bot has no test data; no control API is called      |

## Identifiers

- **session_id**: unique per scenario run. The bot MUST keep each session's
  conversation separate and MUST key conversation state by it (or by
  identity, which is also unique per run).
- **identity**: a synthetic user id such as `obatala-1a2b3c4d5e6f`, unique
  per scenario run. With `http-control` the bot is told to delete it after
  the run.
- **fixture**: the key of the seeded test data the scenario runs against,
  or absent when the scenario has none.
- **meta**: string map returned by the bot when the fixture was seeded,
  passed back on every message (for example a tenant token).

## Message endpoint (`transport: http`)

```yaml
transport:
  name: http
  settings:
    url: http://localhost:8080/obatala/messages   # required
    timeout: 120s                                   # per request, default 120s
    headers:                                        # optional
      Authorization: "Bearer ${BOT_TEST_TOKEN}"     # ${VAR} expands from env / .env
```

Request, one per customer message:

```http
POST <url>
Content-Type: application/json

{"session_id": "pricing-1", "identity": "obatala-1a2b3c4d5e6f",
 "fixture": "demo-store", "meta": {"tenant_token": "t-1"},
 "text": "How much are the plans?"}
```

`fixture` and `meta` are omitted when empty.

Response:

```http
200 OK
Content-Type: application/json

{"replies": [{"text": "Plans start at $10 per seat."},
             {"text": "Want the full price sheet?", "at": "2026-01-02T15:04:05Z"}]}
```

- The bot MUST answer 2xx with a JSON object holding `replies`, an array
  that MAY be empty.
- Each reply's `text` is one bot message. Replies with empty `text` are
  ignored. A turn that ends with no non-empty reply is an infrastructure
  error (outcome `error`): the bot MUST answer every customer message.
- `at` (RFC 3339) is optional. When absent the time the response arrived
  is used. Latency is measured from just before the request to `at`.
- Any non-2xx status, a timeout or an invalid body is an infrastructure
  error: the scenario's outcome is `error` (exit code 2), never `fail`.
  Response bodies are never copied into reports.

## Asynchronous replies (`transport: webhook-outbox`)

For bots that acknowledge a message and reply later (queues, workers).

```yaml
transport:
  name: webhook-outbox
  settings:
    url: http://localhost:8080/obatala/messages   # required
    redis_url: redis://localhost:6379/0           # or redis_url_env: NAME_OF_ENV_VAR
    stream_prefix: "obatala:outbox:"              # default
    timeout: 15s                                    # per POST, default 15s
    headers: {}                                     # as for http
```

- obatala POSTs the same request body as above. Any 2xx accepts the
  message; the response body is ignored.
- The bot MUST append each reply to the Redis stream
  `<stream_prefix><identity>`:

  ```
  XADD obatala:outbox:obatala-1a2b3c4d5e6f * text "Plans start at $10 per seat." id "r-17" at "2026-01-02T15:04:05Z"
  ```

  - `text`: the reply. An entry without `text` (for example a typing
    marker) counts as activity but is not shown.
  - `id` (optional): a stable reply id. A second entry with the same `id`
    is dropped, so retried XADDs are safe.
  - `at` (optional, RFC 3339): defaults to the stream entry's timestamp.
- obatala deletes the stream when the session opens and closes.
- A turn ends when `settle_window` (obatala.yaml, default 4s) passes with
  no new entry after the first reply, or when `reply_timeout` (default
  90s) passes with no reply at all. A turn with no reply is an
  infrastructure error (outcome `error`, exit code 2).

## Control API (`fixtures_manager: http-control`)

```yaml
fixtures_manager: http-control
fixtures_manager_settings:
  url: http://localhost:8080/obatala   # required; paths below are appended
  timeout: 30s                         # per request, default 30s
  headers:
    Authorization: "Bearer ${BOT_CONTROL_TOKEN}"
```

The control API MUST only be reachable in test deployments. obatala
refuses to run unless `/meta` reports test mode.

### `GET /meta`

```json
{"test_mode": true, "environment": "staging", "app_version": "1.4.2",
 "prompt_version": "2026-01-02", "bot_model": "gpt-x", "extra": {"region": "eu"}}
```

- `test_mode` MUST be `true`. obatala refuses to run otherwise, and also
  when `environment` is `production`.
- The other fields are optional and are copied into the report's `meta`.

### `PUT /fixtures/{key}`

- Body: the fixture file `fixtures/<key>.yaml` (or `.yml`/`.json`),
  converted to JSON. Its schema is the bot's own; obatala does not
  interpret it. Keys match `^[a-z0-9][a-z0-9-]{2,40}$` (the scenario
  schema's fixture key rule).
- The bot MUST create or replace the fixture (idempotent) and answer 2xx.
- Optional response body: `{"meta": {"name": "value"}}`. The meta is passed
  on every message of scenarios using this fixture.
- Each fixture referenced by the selected scenarios is seeded once per run,
  before any scenario starts.

### `DELETE /identities/{identity}` and `DELETE /fixtures/{key}`

- Called after the run, whatever the outcome: identities first, then
  fixtures.
- The bot SHOULD delete everything it stored for that identity or fixture.
- 2xx or 404 count as success. Any other status makes the run's exit code
  2, because test data may have been left behind.

### `GET /fixtures/{key}/system-prompt` (optional)

```json
{"system_prompt": "You are the support assistant for ..."}
```

Used only by `obatala reconcile`, which proposes edits to the bot's system
prompt for failed scenarios. Answer 404 if not supported.

## No test data (`fixtures_manager: none`)

Scenarios omit `fixture`; obatala makes no control calls and still gives
each scenario its own `session_id` and `identity`.

## Security notes

- Never put secrets in `obatala.yaml`. Use `${VAR}` in headers, or
  `redis_url_env`, and keep values in the environment or a `.env` file
  beside the config (environment variables win).
- obatala never echoes response bodies into errors or reports.
