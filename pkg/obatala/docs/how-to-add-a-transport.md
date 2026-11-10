# How to add a transport

A transport is how Obatala delivers a simulated customer message to your
bot and collects the bot's replies. If your bot already has a test hook —
a webhook you can hit, a queue you can read, a function you can call —
a transport is a thin adapter over it.

## The contract

```go
package transport

type Session struct {
    ID         string            // unique per scenario run
    Identity   string            // synthetic actor id from Manager.NewIdentity
    FixtureKey string            // the scenario's fixture key ("" if none)
    Meta       map[string]string // seeded manager data (e.g. webhook tokens)
}

type Message struct {
    Text string
    At   time.Time
    Raw  json.RawMessage // optional raw payload for diagnostics
}

type ReceiveOpts struct {
    Timeout      time.Duration // give up after this long
    SettleWindow time.Duration // wait this extra long to collect multi-part replies
}

type Transport interface {
    Open(ctx context.Context, s Session) error
    Send(ctx context.Context, s Session, text string) (sentAt time.Time, err error)
    Receive(ctx context.Context, s Session, o ReceiveOpts) ([]Message, error)
    Close(ctx context.Context, s Session) error
}
```

Per turn the engine calls `Send` (the customer's message) then `Receive`
(the bot's replies). Return **all** replies collected within the settle
window; `At` drives the latency metric (first vs last reply).

## Rules

- **Errors are infrastructure, not regressions.** Anything that fails in
  `Send`/`Receive` (non-2xx, Redis down, timeout) becomes outcome `error`
  (exit 2) — never a judge `fail`. Don't swallow errors; don't retry
  inside the transport (the runner owns retries/timeouts).
- **One session = one conversation.** Parallel scenarios share a Transport
  instance; isolate state by `Session.ID`/`Session.Meta`. A reply for
  session A must never surface in session B.
- **`Open`/`Close`** prepare and release per-session resources (e.g.
  subscribe/unsubscribe an outbox key). `Close` always runs, even after
  failures.
- **Respect `ctx`** — no unbounded blocking.

## Patterns

Three building blocks ship in `pkg/`; use them before writing your own.

1. **In-process** (same Go program as the bot) — `pkg/obatala/transport/inproc`.
   `inproc.New(handler)` calls your handler on `Send` and returns its
   replies on `Receive` with no waiting. `inproc.NewAsync(handler)` gives
   the handler a `reply` callback for bots that answer later; `Receive`
   then waits for the first reply and the settle window. `OnOpen`/`OnClose`
   options hook per-session setup and cleanup.

   ```go
   transport.Register("mybot", func(map[string]any) (transport.Transport, error) {
       return inproc.New(func(ctx context.Context, s transport.Session, text string) ([]string, error) {
           return bot.Reply(ctx, s.ID, text)
       }), nil
   })
   ```
2. **Webhook + outbox** — `pkg/obatala/transport/httpwebhook` sends,
   `pkg/obatala/outbox` reads. `httpwebhook.Sender` POSTs a JSON body you
   build (`BuildBody`, or `Post` with a ready body) and turns non-2xx into
   errors (optional `StatusHint`, never the response body). It does not
   retry. `outbox.Reader` XREADs a per-session Redis stream (`StreamKey`)
   with the settle-window rule, deduplicates via your `Decode` hook, and
   clears the stream on `Open`/`Close` (unless `KeepOnOpen`/`KeepOnClose`).
   Your transport keeps only the payload mapping and calls both from its
   own `Open`/`Send`/`Receive`/`Close`. Only `outbox` imports go-redis.
3. **Polling**: `Send` enqueues work; `Receive` polls an API until
   replies appear or the timeout expires.

## Register it

```go
func init() {
    _ = transport.Register("myservice", func(settings map[string]any) (transport.Transport, error) {
        baseURL, _ := settings["backend_url"].(string) // filled from obatala.yaml
        return New(baseURL), nil
    })
}
```

`obatala.yaml` selects it and passes extra keys via `transport.settings`
(opaque to the engine — your factory decides):

```yaml
transport:
  name: myservice
  settings:
    backend_url: https://staging.example   # read by YOUR factory
    timeout: 30s
```

## Test it

Use `pkg/obatala/fakes.NewTransport(replies...)` (scripted reply batches,
injectable delay/send error) — see `pkg/obatala/execute_test.go` and
`pkg/obatala/cli/cli_test.go` for end-to-end examples with fakes. The
tests of `outbox` and `httpwebhook` show miniredis/httptest setups.
