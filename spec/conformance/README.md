# Conformance

Checks that an Obatala implementation behaves as [SPEC.md](../SPEC.md)
requires. The cases are in [expected.yaml](expected.yaml). They assert
structure (exit codes, outcomes, report shape), not wording, so any
implementation in any language can be checked against them.

## What you need

- `echo_bot.py`: the reference bot (Python 3, standard library). It speaks
  the synchronous [HTTP adapter protocol](../http-adapter.md) on port 8090
  and its behavior is set by `ECHO_MODE` (`echo`, `silent`, `slow`,
  `broken`).
- An LLM for the customer and judge. `obatala.yaml` here configures the Go
  engine; native implementations use their own config.
- `scenarios/` (valid cases) and `invalid/` (suites that must be rejected).

## Running a case

For each case in `expected.yaml`:

1. Start the bot: `ECHO_MODE=<bot> PORT=8090 python3 echo_bot.py`.
2. Run your implementation on `suite` with the case's `filter`, writing
   the JSON report.
3. Check: the exit code equals `exit`; each scenario's `outcome` matches
   `outcomes`; every `require` holds; the report validates against
   [`../schemas/report.schema.json`](../schemas/report.schema.json).

With the Go engine:

```sh
cd spec/conformance
ECHO_MODE=echo python3 echo_bot.py &
obatala run --suite scenarios --id echo-pass --report json    # expect exit 0
```

The `pass` and `unmet` cases depend on the judge LLM. With a capable
model they are unambiguous; if one flips, rerun before investigating.
