package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// capturedCopilotReviewStream is the copilot event stream recorded from the
// review step of run 01KZ0JPJYSREYZTD6YPPZKFCJM, which failed the gate with
// `copilot output parse: invalid character 'T' looking for beginning of value`.
// The fixture lives next to the adapter regression it primarily pins.
const capturedCopilotReviewStream = "../../agent/testdata/copilot_review_task_complete.jsonl"

// TestCopilotReviewContract_CapturedStreamProducesFindings is the end-to-end
// proof for the review gate: the real copilot adapter, driven over the exact
// captured output of the failing run and handed the real reviewFindingsSchema,
// must produce findings that deserialize into the pipeline's Findings contract
// exactly as reviewStep does.
func TestCopilotReviewContract_CapturedStreamProducesFindings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake copilot binary uses a POSIX shell script")
	}

	bin := writeFakeCopilotFromFixture(t, capturedCopilotReviewStream)

	ag, err := agent.New(types.AgentName("copilot"), bin, nil)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	result, err := ag.Run(context.Background(), agent.RunOpts{
		Prompt:     "review the branch",
		CWD:        t.TempDir(),
		JSONSchema: reviewFindingsSchema,
	})
	if err != nil {
		t.Fatalf("copilot review run failed at the output contract: %v", err)
	}
	if result.Output == nil {
		t.Fatal("review produced no structured output")
	}

	// Mirror reviewStep's parse exactly.
	var findings Findings
	if err := json.Unmarshal(result.Output, &findings); err != nil {
		t.Fatalf("structured review output does not satisfy the Findings contract: %v", err)
	}
	if len(findings.Items) != 8 {
		t.Fatalf("findings = %d, want the 8 captured review findings", len(findings.Items))
	}
	for i, f := range findings.Items {
		if f.Severity == "" {
			t.Errorf("finding %d has no severity", i)
		}
		if f.Description == "" {
			t.Errorf("finding %d has no description", i)
		}
		if f.Action == "" {
			t.Errorf("finding %d has no action", i)
		}
	}
	// The captured review reported blocking work, so the gate must park.
	if !hasBlockingFindings(findings.Items) {
		t.Error("captured review findings should be blocking")
	}
}

// writeFakeCopilotFromFixture writes a stub copilot binary that replays the
// JSONL fixture on stdout and exits 0.
func writeFakeCopilotFromFixture(t *testing.T, fixture string) string {
	t.Helper()

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	script := []string{"#!/bin/sh"}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		script = append(script, "printf '%s\\n' '"+strings.ReplaceAll(line, "'", `'\''`)+"'")
	}
	script = append(script, "exit 0")

	bin := filepath.Join(t.TempDir(), "copilot")
	if err := os.WriteFile(bin, []byte(strings.Join(script, "\n")+"\n"), 0o755); err != nil {
		t.Fatalf("write fake copilot: %v", err)
	}
	return bin
}
