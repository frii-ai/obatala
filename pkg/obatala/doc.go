// Package obatala is the reusable core of the Obatala test runner: it
// regression-tests and latency-tests LLM-driven bots by simulating a user
// against a backend over a pluggable Transport, judging outcomes with
// natural-language criteria, and reporting pass/fail/error plus latency.
//
// The five-line entrypoint is Run(t) from a go test; Execute is the same
// orchestration for CLIs. Bot adapters plug in via the transport, prepare
// (Manager) and llm registries — typically from init — so this package
// tree never imports bot-specific code. See README.md for the full guide.
package obatala
