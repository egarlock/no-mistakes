package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capturedReviewSchema mirrors the pipeline's reviewFindingsSchema (see
// internal/pipeline/steps/common.go). The adapter package cannot import the
// steps package, so the review contract's required keys are restated here; the
// end-to-end proof against the real schema lives in
// internal/pipeline/steps/copilot_review_contract_test.go.
var capturedReviewSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"findings": {"type": "array"},
		"risk_level": {"type": "string", "enum": ["low", "medium", "high"]},
		"risk_rationale": {"type": "string"},
		"risk_scope": {"type": "string", "enum": ["source-or-external", "pipeline-owned-delivery"]}
	},
	"required": ["findings", "risk_level", "risk_rationale", "risk_scope"]
}`)

// capturedReviewStream returns the copilot event stream recorded from the
// review step of run 01KZ0JPJYSREYZTD6YPPZKFCJM, which failed with
// `copilot output parse: invalid character 'T' looking for beginning of value`.
// Every content-bearing assistant.message and the terminal
// session.task_complete event are reproduced verbatim from that session's
// events.jsonl.
func capturedReviewStream(t *testing.T) []string {
	t.Helper()
	return readJSONLFixture(t, filepath.Join("testdata", "copilot_review_task_complete.jsonl"))
}

func readJSONLFixture(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("fixture %s is empty", path)
	}
	return lines
}

func parseCapturedStream(t *testing.T, lines []string) ([]string, copilotCompletion) {
	t.Helper()
	var usage TokenUsage
	var messages []string
	var copilotErr string
	exitCode := 0
	var completion copilotCompletion

	if err := parseCopilotEvents(
		context.Background(),
		strings.NewReader(strings.Join(lines, "\n")+"\n"),
		nil, &usage, &messages, &copilotErr, &exitCode, &completion,
	); err != nil {
		t.Fatalf("parseCopilotEvents: %v", err)
	}
	return messages, completion
}

// TestParseCopilotEvents_CapturesTaskCompleteSummary pins the root cause of the
// review-gate failure: copilot delivers its final non-interactive response
// through the terminal session.task_complete event, not through the trailing
// assistant.message, which is mid-turn progress narration.
func TestParseCopilotEvents_CapturesTaskCompleteSummary(t *testing.T) {
	messages, completion := parseCapturedStream(t, capturedReviewStream(t))

	if !completion.Reported {
		t.Fatal("session.task_complete was not captured")
	}
	if !completion.Success {
		t.Error("captured run reported success=false, want true")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(completion.Summary), &payload); err != nil {
		t.Fatalf("task_complete summary is not JSON: %v", err)
	}
	for _, key := range []string{"findings", "risk_level", "risk_rationale", "risk_scope"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("task_complete summary missing required key %q", key)
		}
	}

	// The captured last assistant message is the prose that produced the
	// reported `invalid character 'T'` failure.
	last := messages[len(messages)-1]
	if !strings.HasPrefix(last, "The review has confirmed") {
		t.Fatalf("last assistant message = %q, want the captured prose narration", last)
	}
}

// TestFinalizeCopilotResult_CapturedReviewRunSatisfiesContract is the exact
// regression for the reported failure: the captured stream must now produce a
// schema-valid result instead of a parse error.
func TestFinalizeCopilotResult_CapturedReviewRunSatisfiesContract(t *testing.T) {
	messages, completion := parseCapturedStream(t, capturedReviewStream(t))

	// Disconfirming control: no assistant message satisfies the contract, so
	// the pre-fix message-only path could not have succeeded on this run.
	for i, msg := range messages {
		if _, err := finalizeTextResult("copilot", msg, capturedReviewSchema, TokenUsage{}); err == nil {
			t.Fatalf("assistant message %d unexpectedly satisfied the review contract", i)
		}
	}

	result, err := finalizeCopilotResult(completion, messages, capturedReviewSchema, TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	if len(result.Output) == 0 {
		t.Fatal("result.Output is empty, want the structured review findings")
	}
	var payload struct {
		Findings []struct {
			Severity    string `json:"severity"`
			Action      string `json:"action"`
			Description string `json:"description"`
		} `json:"findings"`
		RiskLevel string `json:"risk_level"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal structured output: %v", err)
	}
	if len(payload.Findings) != 8 {
		t.Fatalf("findings = %d, want the 8 captured review findings", len(payload.Findings))
	}
	if payload.RiskLevel == "" {
		t.Error("risk_level is empty")
	}
	for i, f := range payload.Findings {
		if f.Severity == "" || f.Action == "" || f.Description == "" {
			t.Errorf("finding %d is malformed: %+v", i, f)
		}
	}
}

// TestCopilotAgent_RunCapturedReviewStream drives the real adapter over the
// captured stream through a fake copilot binary, proving the run no longer
// fails at the parser boundary.
func TestCopilotAgent_RunCapturedReviewStream(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeCopilot(t, dir, capturedReviewStream(t), 0)

	ca := &copilotAgent{bin: bin}
	result, err := ca.Run(context.Background(), RunOpts{
		Prompt:     "review the branch",
		CWD:        t.TempDir(),
		JSONSchema: capturedReviewSchema,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var payload struct {
		Findings []json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(payload.Findings) != 8 {
		t.Fatalf("findings = %d, want 8", len(payload.Findings))
	}
}

// TestFinalizeCopilotResult_TaskCompleteWinsOverEarlierMessage pins precedence:
// the designated final response is authoritative over an earlier message that
// also happens to parse.
func TestFinalizeCopilotResult_TaskCompleteWinsOverEarlierMessage(t *testing.T) {
	completion := copilotCompletion{Summary: `{"value":"final"}`, Success: true, Reported: true}
	messages := []string{`{"value":"stale"}`, "now finishing up"}

	result, err := finalizeCopilotResult(completion, messages, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Value != "final" {
		t.Fatalf("value = %q, want the task_complete summary to win", payload.Value)
	}
}

// TestFinalizeCopilotResult_FallsBackToMessagesWhenSummaryIsProse preserves the
// existing recovery path: copilot sometimes emits the contract JSON in an
// earlier message and closes with a prose task_complete summary.
func TestFinalizeCopilotResult_FallsBackToMessagesWhenSummaryIsProse(t *testing.T) {
	completion := copilotCompletion{Summary: "All four fixes are applied.", Success: true, Reported: true}
	messages := []string{`{"value":"from message"}`, "wrapping up"}

	result, err := finalizeCopilotResult(completion, messages, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Value != "from message" {
		t.Fatalf("value = %q, want the parsing assistant message", payload.Value)
	}
}

// TestFinalizeCopilotResult_MalformedSummaryIsRejected proves the fix adds no
// success-shaped fallback: a task_complete summary that is not valid JSON, with
// no parsing message, is an error rather than a fabricated empty result.
func TestFinalizeCopilotResult_MalformedSummaryIsRejected(t *testing.T) {
	completion := copilotCompletion{Summary: `{"findings": [`, Success: true, Reported: true}

	result, err := finalizeCopilotResult(completion, []string{"working on it"}, capturedReviewSchema, TokenUsage{})
	if err == nil {
		t.Fatalf("expected an error for a malformed summary, got %+v", result)
	}
	if result != nil {
		t.Fatalf("expected nil result on parse failure, got %+v", result)
	}
	if !strings.Contains(err.Error(), "copilot output parse") {
		t.Fatalf("error = %v, want a copilot output parse error", err)
	}
	// The failure must report the designated final response, not the narration.
	if !strings.Contains(err.Error(), `findings`) {
		t.Fatalf("error = %v, want the task_complete summary in the snippet", err)
	}
	if strings.Contains(err.Error(), "working on it") {
		t.Fatalf("error = %v, want the summary rather than the assistant narration", err)
	}
}

// TestFinalizeCopilotResult_SchemaViolatingSummaryIsRejected proves valid JSON
// that violates the contract is still refused: findings must never be accepted
// malformed.
func TestFinalizeCopilotResult_SchemaViolatingSummaryIsRejected(t *testing.T) {
	completion := copilotCompletion{Summary: `{"findings":[],"risk_level":"catastrophic"}`, Success: true, Reported: true}

	if _, err := finalizeCopilotResult(completion, nil, capturedReviewSchema, TokenUsage{}); err == nil {
		t.Fatal("expected an error for a schema-violating summary")
	}
}

// TestFinalizeCopilotResult_UnsuccessfulTaskCompleteIsSurfaced keeps a failed
// completion diagnosable when the contract is not satisfied.
func TestFinalizeCopilotResult_UnsuccessfulTaskCompleteIsSurfaced(t *testing.T) {
	completion := copilotCompletion{Summary: "I could not finish the review.", Success: false, Reported: true}

	_, err := finalizeCopilotResult(completion, nil, capturedReviewSchema, TokenUsage{})
	if err == nil {
		t.Fatal("expected an error when the contract is unmet")
	}
	if !strings.Contains(err.Error(), "task_complete reported success=false") {
		t.Fatalf("error = %v, want the unsuccessful completion surfaced", err)
	}
}

// TestFinalizeCopilotResult_NoTaskCompletePreservesMessageBehavior pins
// backward compatibility with copilot builds that emit no task_complete event.
func TestFinalizeCopilotResult_NoTaskCompletePreservesMessageBehavior(t *testing.T) {
	messages := []string{`{"value":"from message"}`, "trailing prose"}

	result, err := finalizeCopilotResult(copilotCompletion{}, messages, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(result.Output, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Value != "from message" {
		t.Fatalf("value = %q, want the parsing assistant message", payload.Value)
	}
}

// TestFinalizeCopilotResult_NoSchemaUsesTaskCompleteSummary applies the same
// root-cause fix to schema-free calls: the final response is the completion
// summary, not the trailing narration.
func TestFinalizeCopilotResult_NoSchemaUsesTaskCompleteSummary(t *testing.T) {
	completion := copilotCompletion{Summary: "the real answer", Success: true, Reported: true}

	result, err := finalizeCopilotResult(completion, []string{"still working"}, nil, TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	if result.Text != "the real answer" {
		t.Fatalf("text = %q, want the task_complete summary", result.Text)
	}
}

// TestFinalizeCopilotResult_NoSchemaFallsBackToLastMessage keeps schema-free
// behavior unchanged when copilot emits no completion event.
func TestFinalizeCopilotResult_NoSchemaFallsBackToLastMessage(t *testing.T) {
	result, err := finalizeCopilotResult(copilotCompletion{}, []string{"first", "last"}, nil, TokenUsage{})
	if err != nil {
		t.Fatalf("finalizeCopilotResult: %v", err)
	}
	if result.Text != "last" {
		t.Fatalf("text = %q, want the final assistant message", result.Text)
	}
}
