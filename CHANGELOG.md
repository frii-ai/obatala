# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project
uses [semantic versioning](https://semver.org/). Before 1.0, minor
versions may break the Go API.

## [Unreleased]

## [0.1.0] - 2026-11-12

First public release.

- Engine: LLM customer simulator and judge, YAML scenarios with success
  and failure criteria, turn and latency budgets, concurrent runner,
  exit codes 0/1/2.
- Reports: console, JSON (`report_version` 3), JUnit, Markdown and
  self-contained HTML.
- LLM providers: OpenAI-compatible and Anthropic.
- Transports: `inproc`, `httpwebhook`, `outbox` (Redis streams), and the
  HTTP adapter transports `http` and `webhook-outbox`.
- Fixture managers: `http-control` and `none`.
- CLI as a library (`cli.Main`) and the `obatala` binary with
  `run`, `list`, `validate`, `create` and `reconcile`.
- Specification (`spec_version` 1), JSON Schemas and a conformance suite.
- Examples: `hello` (Go, in-process) and `http-bot` (Python over HTTP).

[Unreleased]: https://github.com/frii-ai/obatala/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/frii-ai/obatala/releases/tag/v0.1.0
