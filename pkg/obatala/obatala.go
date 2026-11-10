// Package obatala is the one-call integration surface for regression
// testing a conversational bot: import your bot's adapter package (which
// registers its transport and fixture manager), write scenario YAML, and
// call Run from a go test:
//
//	func TestRegression(t *testing.T) { obatala.Run(t) }
//
// The same Execute function powers the obatala CLI, so the go-test
// harness and the command behave identically.
package obatala

import (
	"context"
	"testing"
	"time"

	"github.com/frii-ai/obatala/pkg/obatala/report"
)

// Run executes the regression suite against the bot under test and fails
// t when setup breaks, any scenario fails, or any scenario errors.
// Defaults: config obatala.yaml, suite scenarios/, fixtures fixtures/ —
// override with Config/Suite/Fixtures options.
func Run(t testing.TB, opts ...Option) {
	t.Helper()
	o := Options{
		Log:    t.Logf,
		ErrLog: t.Logf,
	}
	for _, opt := range opts {
		opt(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout())
	defer cancel()
	summary, code, err := Execute(ctx, o)
	if err != nil {
		t.Errorf("obatala: %v", err)
	}
	if code != 0 {
		pass, fail, errs := report.Tally(summary)
		t.Fatalf("obatala: %d pass, %d fail, %d error", pass, fail, errs)
	}
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}
