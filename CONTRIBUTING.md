# Contributing

Issues and pull requests are welcome.

## Development

You need Go (the version in `go.mod`) and
[golangci-lint](https://golangci-lint.run/) v2.

```sh
make all     # build, vet, gofmt check, lint, test
```

`go test ./...` runs offline. Tests that call a real LLM skip unless
their key is set; for example `OLLAMA_API_KEY=... go test ./pkg/examples/hello`.

## Changes to behaviour

[spec/SPEC.md](spec/SPEC.md) describes what any implementation must do,
and the JSON Schemas in `spec/schemas` are checked against the engine by
tests. A change to scenario files, the verdict tool, exit codes or the
report shape must update the spec and schemas in the same pull request.
A breaking change to the report bumps `report_version`.

Run the [conformance suite](spec/conformance) when you touch the engine
loop, budgets or reports.

## Pull requests

- Keep each pull request to one change, with tests.
- Add a line to `CHANGELOG.md` under "Unreleased".
- Never commit API keys or `.env` files.
