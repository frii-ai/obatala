// Command obatala runs regression and latency tests against an LLM
// chatbot: run, list, validate, reconcile, create. It registers the
// language-neutral HTTP adapter (transports "http" and "webhook-outbox",
// fixture managers "http-control" and "none"), so a bot written in any
// language can be tested without Go code. See spec/http-adapter.md.
//
// To test a Go bot in-process, or with your own transport or manager,
// write a small main that blank-imports your adapter and calls cli.Main
// (see pkg/examples/hello).
package main

import (
	"os"

	"github.com/frii-ai/obatala/pkg/obatala/cli"
	_ "github.com/frii-ai/obatala/pkg/obatala/prepare/httpcontrol"
	_ "github.com/frii-ai/obatala/pkg/obatala/prepare/stateless"
	_ "github.com/frii-ai/obatala/pkg/obatala/transport/httpsync"
	_ "github.com/frii-ai/obatala/pkg/obatala/transport/webhookoutbox"
)

func main() { os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr, cli.Config{})) }
