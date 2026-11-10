// Command hello is a complete minimal Obatala bot where every role is a
// real AI call: an LLM-powered in-process bot, an LLM customer simulator
// and an LLM judge (Ollama/gemma4 by default — see obatala.yaml). The
// fixture manager is in-memory, so no backend is needed, only an API key:
//
//	cd pkg/examples/hello && go run . run
//
// The transport and manager factories self-register from init, mirroring
// how a real adapter plugs into the engine; main is the whole CLI (run,
// list, validate, reconcile, create) via cli.Main.
package main

import (
	"os"

	"github.com/frii-ai/obatala/pkg/obatala/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr, cli.Config{
		Name:    "hello",
		Tagline: "Obatala's minimal example bot",
	}))
}
