package scenariodef_test

import (
	"testing"

	"github.com/frii-ai/obatala/pkg/obatala/scenariodef"
	"gopkg.in/yaml.v3"
)

const sample = `id: haggle-socks-and-pay
name: Customer haggles for socks and pays
tags: [haggle, payment, smoke]
fixture: shoes-and-socks
customer:
  description: Budget-conscious buyer who wants 3 pairs of socks
  strategy: Ask for socks, haggle once, accept the counter-offer, then pay
  max_turns: 10
criteria:
  success:
    - The bot sends a payment link the customer can pay with
    - The amount reflects the price agreed after haggling
  failure:
    - The bot offers products the shop does not sell
    - The bot sends a payment link before a price is agreed
budgets:
  turn_latency_p95_ms: 8000
  max_turns: 10
`

func TestDecodePlanExample(t *testing.T) {
	var s scenariodef.Scenario
	if err := yaml.Unmarshal([]byte(sample), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.ID != "haggle-socks-and-pay" {
		t.Fatalf("id = %q", s.ID)
	}
	if s.Fixture != "shoes-and-socks" {
		t.Fatalf("fixture = %q", s.Fixture)
	}
	if len(s.Tags) != 3 || s.Tags[0] != "haggle" {
		t.Fatalf("tags = %v", s.Tags)
	}
	if s.Customer.MaxTurns != 10 || s.Customer.Strategy == "" {
		t.Fatalf("customer = %+v", s.Customer)
	}
	if len(s.Criteria.Success) != 2 || len(s.Criteria.Failure) != 2 {
		t.Fatalf("criteria = %+v", s.Criteria)
	}
	if s.Budgets.TurnLatencyP95MS != 8000 || s.Budgets.MaxTurns != 10 {
		t.Fatalf("budgets = %+v", s.Budgets)
	}
}

func TestOptionalFieldsAbsent(t *testing.T) {
	var s scenariodef.Scenario
	if err := yaml.Unmarshal([]byte("id: minimal\n"), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.ID != "minimal" || s.Repeat != 0 || s.PassThreshold != 0 {
		t.Fatalf("scenario = %+v", s)
	}
}
