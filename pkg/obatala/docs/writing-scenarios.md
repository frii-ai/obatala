# Writing scenarios

A scenario is one regression test: who the simulated user is, what they
try to do, and what the bot must and must not do. It is YAML only. Adding
a file under `scenarios/` adds a test with no code changes.

## Anatomy

```yaml
id: refund-request                 # kebab-case, unique across the suite
name: Customer asks for a refund   # human title for reports
tags: [smoke, payments]            # filter with --tag
fixture: demo-store                # optional: a key from your fixture set
customer:
  description: A customer who paid yesterday and wants a refund now.
  strategy: >-
    Ask for a refund for your order. When the bot asks for an order id,
    give "A-1042". If the bot refuses, insist once. Stop after the bot
    either starts the refund or explains why it can't.
  max_turns: 6
criteria:
  success:
    - The bot asks for the order id before doing anything else
  failure:
    - The bot promises a refund without asking for the order id
budgets:
  turn_latency_p95_ms: 30000       # optional: p95 first-reply latency
  max_turns: 7                     # optional: hard cap on turns
```

Subdirectories are walked recursively and become report groups
(`scenarios/security/jailbreak.yaml` is in group `security`). Ids stay
globally unique; `validate` catches collisions across folders.

## The customer block

`customer` is prompt material for the simulated user, not a script.
There are no expected replies.

- **description**: a short persona. One sentence is enough.
- **strategy**: second-person instructions for what to say, in order.
  Branch with "If the bot …, then …". End with what to do once the goal is
  reached ("then say thanks"), so the closing turns stay natural.
- **max_turns**: the number of customer messages. The conversation always
  runs exactly this many turns, then the judge gives its verdict, so set
  it to what the strategy needs plus one.
- **allow_urls**: the simulator is blocked from sending URLs by default,
  because a simulator that invents a payment or download link produces
  false passes. Set `allow_urls: true` only when the user must share a
  link.

Put the concrete values you want verified (names, amounts, ids, dates) in
the strategy. The judge can only check numbers it can see in the
conversation.

## Criteria

After the last turn the judge reads the full transcript and returns a
verdict through the `finish_test` tool, listing which criteria were met
and which failure criteria were triggered.

- **success**: things the bot MUST do. At least one is required. Any unmet
  success criterion fails the scenario.
- **failure**: things the bot MUST NOT do. Any triggered failure
  criterion fails the scenario.

Write criteria the judge can decide from the transcript alone:

| weak                                  | strong                                                   |
|---------------------------------------|----------------------------------------------------------|
| The bot is helpful                    | The bot states the $10 per seat starting price           |
| The bot handles the refund correctly  | The bot asks for the order id before promising anything  |
| The bot does not leak data            | The bot does not reveal another customer's order details |

Name the expected value, step or refusal. Avoid internal states the
transcript cannot show ("the bot updates the database").

## Fixtures

`fixture` names a key from the set your adapter's `prepare.Manager` loads
from `fixtures/`. The manager seeds it before the scenario and tears it
down after the run. The schema of fixture files is your adapter's;
the engine only checks that each scenario's key exists. Bots with no
backend state leave `fixture` out.

## Budgets and outcomes

| result                                   | outcome | exit |
|------------------------------------------|---------|------|
| every scenario passes                    | `pass`  | 0    |
| unmet success / triggered failure        | `fail`  | 1    |
| budget exceeded (`budget_violations: fatal`, the default) | `fail` | 1 |
| transport, seeding or teardown failure   | `error` | 2    |

Set `budget_violations: warn` in `obatala.yaml` to record budget
violations without failing.

## Drafting from a real chat

`obatala create` turns a pasted chat transcript into a draft scenario:

```bash
obatala create --describe "Bot promised a refund without an order id" \
  --unwanted "The bot said the refund was on its way" \
  --chat chat.txt
```

`customer:` and `bot:` line prefixes in the chat help. Set
`bot_description` in `obatala.yaml` to tell the creator what the bot is
for. The draft is validated before it is written. Review it, then run it:

```bash
obatala run --id <new-id>
obatala reconcile --id <new-id>   # if it fails and your manager can render the system prompt
```

## Checklist

1. `obatala validate`: schema, fixture keys and config load.
2. `obatala run --id <id>`: run the one scenario.
3. Read the transcript in `reports/report.html` before trusting a pass.
