package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/langwatch/scenario-go"

	"github.com/frii-ai/obatala/pkg/obatala/llm"
)

const verdictToolName = "finish_test"

var toolChoiceRequired = "required"

const agentGreeting = "Hello, how can I help you today?"

// System prompt template and finish_test schema vendored from
// github.com/langwatch/scenario-go v1.0.0 (MIT), extended with a dual-LLM
// seam: the customer simulator and the judge are independent completions.
var testingAgentTemplate = template.Must(template.New("testing_agent").Parse(`
<role>
You are pretending to be a user, you are testing an AI Agent (shown as the user role) based on a scenario.
Approach this naturally, as a human user would, with very short inputs, few words, all lowercase, imperative, not periods, like when they google or talk to chatgpt.
</role>

<goal>
Your goal (assistant) is to interact with the Agent Under Test (user) as if you were a human user to see if it can complete the scenario successfully.
</goal>

<scenario>
{{.Description}}
</scenario>

<strategy>
{{.Strategy}}
</strategy>

<success_criteria>
{{.SuccessCriteriaJSON}}
</success_criteria>

<failure_criteria>
{{.FailureCriteriaJSON}}
</failure_criteria>

<execution_flow>
1. Generate the first message to start the scenario
2. After the Agent Under Test (user) responds, generate the next message to send to the Agent Under Test, keep repeating step 2 until the criteria match
3. If the test should end, use the finish_test tool to determine if success or failure criteria have been met
</execution_flow>

<rules>
1. Test should end immediately if a failure criteria is triggered
2. Test should continue until all success criteria have been met
3. DO NOT make any judgment calls that are not explicitly listed in the success or failure criteria, withhold judgement if necessary
4. DO NOT carry over any requests yourself, YOU ARE NOT the assistant today, wait for the user to do it
</rules>
`))

// customerAgentTemplate is the judge template minus the finish_test tool
// instructions. The customer completion advertises no tools, so any mention
// of a tool tempts models (observed with gemma4) to emit a phantom tool call
// with empty content, which harden then rejects as degenerate.
var customerAgentTemplate = template.Must(template.New("customer_agent").Parse(`
<role>
You are pretending to be a user, you are testing an AI Agent (shown as the user role) based on a scenario.
Approach this naturally, as a human user would, with very short inputs, few words, all lowercase, imperative, not periods, like when they google or talk to chatgpt.
</role>

<goal>
Your goal (assistant) is to interact with the Agent Under Test (user) as if you were a human user to see if it can complete the scenario successfully.
</goal>

<scenario>
{{.Description}}
</scenario>

<strategy>
{{.Strategy}}
</strategy>

<success_criteria>
{{.SuccessCriteriaJSON}}
</success_criteria>

<failure_criteria>
{{.FailureCriteriaJSON}}
</failure_criteria>

<execution_flow>
1. Generate the first message to start the scenario
2. After the Agent Under Test (user) responds, generate the next message to send to the Agent Under Test, keep repeating step 2
</execution_flow>

<rules>
1. You must always reply with the next message text — never an empty reply, never a test summary, never an evaluation. You cannot end the conversation; you only decide what the user says next.
2. DO NOT make any judgment calls that are not explicitly listed in the success or failure criteria, withhold judgement if necessary.
3. DO NOT carry over any requests yourself, YOU ARE NOT the assistant today, wait for the user to do it.
4. If the scenario goals are achieved, the user may say a short closing like "ok, thanks!" — keep it natural.
5. You are the user. NEVER write or invent links of any kind — no payment, login, tracking, download or order links — the agent provides those and you receive them. Never invent URLs.
</rules>
`))

const verdictFinishMessage = `
System:

<finish_test>
This is the last message, conversation has reached the maximum number of turns, give your final verdict,
if you don't have enough information to make a verdict, say inconclusive with max turns reached.
</finish_test>`

func verdictTool() llm.Tool {
	return llm.Tool{
		Name:        verdictToolName,
		Description: "Complete the test with a final verdict",
		Strict:      true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []string{"success", "failure", "inconclusive"},
					"description": "The final verdict of the test",
				},
				"reasoning": map[string]any{
					"type":        "string",
					"description": "Explanation of why this verdict was reached",
				},
				"details": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"met_criteria": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of success criteria that have been met",
						},
						"unmet_criteria": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of success criteria that have not been met",
						},
						"triggered_failures": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of failure criteria that have been triggered",
						},
					},
					"required":             []string{"met_criteria", "unmet_criteria", "triggered_failures"},
					"additionalProperties": false,
					"description":          "Detailed information about criteria evaluation",
				},
			},
			"required":             []string{"verdict", "reasoning", "details"},
			"additionalProperties": false,
		},
	}
}

type testingAgentTemplateParams struct {
	Description         string
	Strategy            string
	SuccessCriteriaJSON string
	FailureCriteriaJSON string
}

// obatalaTestingAgent implements scenario.TestingAgent with independent
// completions for the simulated customer and the judge.
type obatalaTestingAgent struct {
	customer llm.Completion
	judge    llm.Completion
}

func newTestingAgent(customer, judge llm.Completion) *obatalaTestingAgent {
	return &obatalaTestingAgent{customer: customer, judge: judge}
}

func (t *obatalaTestingAgent) GenerateNextMessage(
	ctx context.Context,
	description string,
	strategy string,
	successCriteria []string,
	failureCriteria []string,
	conversation []scenario.Message,
	_ bool,
	lastMessage bool,
) (*string, *scenario.Result, error) {
	successJSON, err := json.MarshalIndent(successCriteria, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("engine: marshal success criteria: %w", err)
	}
	failureJSON, err := json.MarshalIndent(failureCriteria, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("engine: marshal failure criteria: %w", err)
	}

	tpl := customerAgentTemplate
	if lastMessage {
		tpl = testingAgentTemplate
	}
	var system bytes.Buffer
	if err := tpl.Execute(&system, testingAgentTemplateParams{
		Description:         description,
		Strategy:            strategy,
		SuccessCriteriaJSON: string(successJSON),
		FailureCriteriaJSON: string(failureJSON),
	}); err != nil {
		return nil, nil, fmt.Errorf("engine: render testing agent template: %w", err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: system.String()},
		{Role: llm.RoleAssistant, Content: agentGreeting},
	}
	for _, m := range conversation {
		messages = append(messages, llm.Message{Role: string(m.Role), Content: m.Content})
	}
	if lastMessage {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: verdictFinishMessage})
	}

	temperature := 0.0

	if lastMessage {
		resp, err := t.judge.Complete(ctx, llm.Request{
			Messages:    messages,
			Temperature: &temperature,
			Tools:       []llm.Tool{verdictTool()},
			ToolChoice:  &toolChoiceRequired,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("engine: judge completion: %w", err)
		}
		result, err := parseVerdict(resp)
		if err != nil {
			return nil, nil, err
		}
		return nil, result, nil
	}

	resp, err := t.customer.Complete(ctx, llm.Request{
		Messages:    messages,
		Temperature: &temperature,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("engine: customer completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, nil, fmt.Errorf("engine: customer completion returned no choices")
	}
	content := resp.Choices[0].Content
	if content == "" {
		return nil, nil, fmt.Errorf("engine: customer completion returned empty content")
	}
	return &content, nil, nil
}

func parseVerdict(resp llm.Response) (*scenario.Result, error) {
	if len(resp.Choices) == 0 || len(resp.Choices[0].ToolCalls) == 0 {
		return nil, fmt.Errorf("engine: judge returned no verdict tool call")
	}
	tc := resp.Choices[0].ToolCalls[0]
	if tc.Name != verdictToolName {
		return nil, fmt.Errorf("engine: judge called unexpected tool %q", tc.Name)
	}

	verdict, _ := tc.Arguments["verdict"].(string)
	reasoning, _ := tc.Arguments["reasoning"].(string)
	details, _ := tc.Arguments["details"].(map[string]any)
	met := stringSlice(details, "met_criteria")
	unmet := stringSlice(details, "unmet_criteria")
	triggered := stringSlice(details, "triggered_failures")

	switch verdict {
	case "success":
		return scenario.NewSuccessPartialResult(nil, reasoning, met), nil
	case "failure":
		return scenario.NewFailurePartialResult(nil, reasoning, met, unmet, triggered), nil
	case "inconclusive":
		return scenario.NewInconclusivePartialResult(nil, reasoning, met, unmet, triggered), nil
	default:
		return nil, fmt.Errorf("engine: judge returned unknown verdict %q", verdict)
	}
}

func stringSlice(details map[string]any, key string) []string {
	raw, ok := details[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
