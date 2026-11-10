package engine

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// TestVerdictToolMatchesSpec keeps spec/schemas/verdict.schema.json and
// the finish_test tool the judge is offered identical.
func TestVerdictToolMatchesSpec(t *testing.T) {
	data, err := os.ReadFile("../../../spec/schemas/verdict.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"$schema", "$id", "title", "description"} {
		delete(spec, k)
	}
	// Round-trip the tool's Go literal through JSON so types compare equal.
	b, err := json.Marshal(verdictTool().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	var tool map[string]any
	if err := json.Unmarshal(b, &tool); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec, tool) {
		got, _ := json.MarshalIndent(tool, "", "  ")
		t.Fatalf("verdict.schema.json drifted from verdictTool():\n%s", got)
	}
	if name := verdictTool().Name; name != "finish_test" {
		t.Fatalf("tool name = %q", name)
	}
}
