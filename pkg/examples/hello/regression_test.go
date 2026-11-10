package main

import (
	"os"
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala"
)

// TestHello is the five-line harness: Run executes the suite in this
// directory (obatala.yaml, scenarios/, fixtures/) with every role on a
// real LLM — the bot, the customer simulator and the judge. It skips
// without an API key so offline runs and the default CI test job stay
// green; the e2e job sets OLLAMA_API_KEY and runs it live.
func TestHello(t *testing.T) {
	if os.Getenv("OLLAMA_API_KEY") == "" {
		t.Skip("hello example is live: set OLLAMA_API_KEY (see pkg/examples/hello/obatala.yaml) to run it")
	}
	obatala.Run(t)
}
