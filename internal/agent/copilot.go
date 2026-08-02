package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

// copilotAgent spawns the GitHub Copilot CLI for each invocation. Copilot
// runs non-interactively with `copilot -p <prompt> --output-format json`,
// emitting JSONL events on stdout. The lifecycle is codex/pi-shaped: one
// process per Run, no managed server.
type copilotAgent struct {
	bin       string
	extraArgs []string
	// disableProjectSettings is the resolved, trusted-only opt-out. When true,
	// buildArgs suppresses copilot's project-level custom-instruction autoload.
	disableProjectSettings bool
}

func (a *copilotAgent) Name() string { return "copilot" }

func (a *copilotAgent) ReportsAgentAttempts() bool { return true }

// NeutralizesGateInstructions reports whether copilot is currently launched
// with the target repo's custom-instruction autoload disabled. It is
// meaningful only under the opt-out (disableProjectSettings): the gate only
// consults it when the repo opted out. The effective knob is
// `--no-custom-instructions`, which disables AGENTS.md and related-file
// autoload at startup.
//
// Empirical evidence (copilot 1.0.71 binary, real firstmate AGENTS.md plus a
// CLAUDE.md symlink, fresh --session-id per arm, prompt "Who are you? Answer
// in one short sentence. Do not use any tools."):
//   - Control (no flag): "I'm your first mate, captain: the single point of
//     contact who delegates and supervises all software work across your
//     projects." Input 49.4k tokens.
//   - Treatment (--no-custom-instructions): "I'm GitHub Copilot CLI, a
//     terminal-based coding assistant powered by Claude Opus 5." Input 30.8k
//     tokens.
//
// The ~18.6k token drop matches AGENTS.md not being loaded.
//
// Unlike codex/claude there is no operator-defeatable per-invocation override
// path to detect from agent_args_override: this adapter prepends extraArgs and
// appends managed flags, so the enforced trailing `--no-custom-instructions`
// stays authoritative for this invocation. The accepted limit is unchanged
// across all adapters: this blocks autoload, not file reads. An unsandboxed
// gate agent can still read AGENTS.md/CLAUDE.md via tools.
func (a *copilotAgent) NeutralizesGateInstructions() bool {
	return a.disableProjectSettings
}

func (a *copilotAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return runWithRetry(ctx, "copilot", opts, claudeMaxRetries, classifyTransient, nil, func() (*Result, error) {
		return a.runOnce(ctx, opts)
	})
}

func (a *copilotAgent) Close() error { return nil }

func (a *copilotAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	prompt := buildCopilotPrompt(opts.Prompt, opts.JSONSchema)
	args := a.buildArgs(prompt)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = opts.CWD
	cmd.Stdin = nil
	cmd.Env = gitSafeEnv(opts.CWD)
	shellenv.ConfigureShellCommand(cmd)

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	started, err := startNativeAgentCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("copilot start: %w", err)
	}
	defer started.closePipes()
	pid := started.pid()
	emitAgentStarted(opts, "copilot", pid)

	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(started.stderr)
	}()

	var usage TokenUsage
	var messages []string
	var copilotErr string
	exitCode := 0
	var completion copilotCompletion
	if err := parseCopilotEvents(ctx, started.stdout, opts.OnChunk, &usage, &messages, &copilotErr, &exitCode, &completion); err != nil {
		err = started.waitAfterParseError(err)
		stderrWG.Wait()
		retErr := fmt.Errorf("copilot parse events: %w", err)
		emitAgentExited(opts, "copilot", pid, retErr)
		return nil, retErr
	}

	waitErr := started.wait()
	stderrWG.Wait()

	detail := copilotErrorDetail(copilotErr, string(stderrBuf))
	if waitErr != nil {
		if detail != "" {
			retErr := fmt.Errorf("copilot exited: %w: %s", waitErr, detail)
			emitAgentExited(opts, "copilot", pid, retErr)
			return nil, retErr
		}
		retErr := fmt.Errorf("copilot exited: %w", waitErr)
		emitAgentExited(opts, "copilot", pid, retErr)
		return nil, retErr
	}
	if exitCode != 0 {
		if detail != "" {
			retErr := fmt.Errorf("copilot reported exit code %d: %s", exitCode, detail)
			emitAgentExited(opts, "copilot", pid, retErr)
			return nil, retErr
		}
		retErr := fmt.Errorf("copilot reported exit code %d", exitCode)
		emitAgentExited(opts, "copilot", pid, retErr)
		return nil, retErr
	}

	res, err := finalizeCopilotResult(completion, messages, opts.JSONSchema, usage)
	emitAgentExited(opts, "copilot", pid, err)
	return res, err
}

// copilotCompletion is the payload of the terminal `session.task_complete`
// event: copilot's designated final response for a non-interactive `-p` run.
// The CLI ends such a run by invoking its built-in `task_complete` tool, whose
// summary argument carries the answer; the trailing assistant.message is
// mid-turn progress narration.
type copilotCompletion struct {
	Summary string
	Success bool
	// Reported distinguishes "copilot emitted no task_complete event" from
	// "copilot reported an empty summary".
	Reported bool
}

// finalizeCopilotResult converts a completed copilot run into a structured
// Result.
//
// Copilot has no equivalent of claude's --json-schema or codex's
// --output-schema, so the contract is inlined in the prompt and the answer is
// recovered from the event stream. The authoritative source is the terminal
// session.task_complete event: in `-p` mode the CLI finishes by calling its
// built-in task_complete tool, and that tool's summary is the final response.
// Reading the last assistant.message instead is what broke every review step
// with `copilot output parse: invalid character 'T'`, because copilot narrates
// progress between tool calls and that narration is usually the trailing
// message. Across all 55 recorded no-mistakes copilot invocations,
// task_complete was present every time, while the trailing message satisfied
// the requested schema only rarely.
//
// Assistant messages remain a fallback, newest-first: copilot is
// non-deterministic about honoring the contract and sometimes emits the schema
// JSON in an earlier message and closes with a prose summary. Every candidate,
// including the task_complete summary, is validated against the schema by
// finalizeTextResult; nothing is accepted unvalidated and no success-shaped
// result is synthesized. When no candidate satisfies the contract the error
// reports the designated final response, so the failure names copilot's actual
// output rather than an intermediate message.
func finalizeCopilotResult(completion copilotCompletion, messages []string, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if len(schema) == 0 {
		return finalizeTextResult("copilot", copilotFinalText(completion, messages), schema, usage)
	}

	if completion.Reported && strings.TrimSpace(completion.Summary) != "" {
		if result, err := finalizeTextResult("copilot", completion.Summary, schema, usage); err == nil {
			return result, nil
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if result, err := finalizeTextResult("copilot", messages[i], schema, usage); err == nil {
			return result, nil
		}
	}

	_, err := finalizeTextResult("copilot", copilotFinalText(completion, messages), schema, usage)
	if err != nil && completion.Reported && !completion.Success {
		return nil, fmt.Errorf("%w (copilot task_complete reported success=false)", err)
	}
	return nil, err
}

// copilotFinalText returns copilot's final response: the task_complete summary
// when the CLI reported one, otherwise the last assistant message, which is the
// only source available on builds that emit no task_complete event.
func copilotFinalText(completion copilotCompletion, messages []string) string {
	if completion.Reported && strings.TrimSpace(completion.Summary) != "" {
		return completion.Summary
	}
	if len(messages) > 0 {
		return messages[len(messages)-1]
	}
	return ""
}

func copilotErrorDetail(copilotErr, stderr string) string {
	detail := strings.TrimSpace(copilotErr)
	stderr = strings.TrimSpace(stderr)
	if detail != "" && stderr != "" {
		return detail + "; " + stderr
	}
	if detail != "" {
		return detail
	}
	return stderr
}

// buildArgs constructs the copilot CLI arguments. User-supplied extraArgs
// (from agent_args_override) are inserted ahead of the managed flags so user
// choices (e.g. --model, --effort) win over no-mistakes' defaults. If the user
// supplied their own permission flag, the default --allow-all-tools is not
// added; --no-ask-user is always added so the agent never blocks waiting for
// interactive input.
func (a *copilotAgent) buildArgs(prompt string) []string {
	args := make([]string, 0, len(a.extraArgs)+8)
	args = append(args, a.extraArgs...)
	args = append(args,
		"-p", prompt,
		"--output-format", "json",
		"--no-color",
	)
	if a.disableProjectSettings {
		args = append(args, "--no-custom-instructions")
	}
	if !copilotUserSetAskUser(a.extraArgs) {
		args = append(args, "--no-ask-user")
	}
	if !copilotUserSetPermissionMode(a.extraArgs) {
		args = append(args, "--allow-all-tools")
	}
	return args
}

// copilotUserSetPermissionMode reports whether extraArgs already grant tool
// auto-approval, in which case buildArgs skips its default --allow-all-tools.
// Only a blanket approval flag (--allow-all-tools, --allow-all, --yolo) or an
// explicit allowlist (--allow-tool) counts. Flags that merely restrict the tool
// set or filesystem paths (--available-tools, --excluded-tools, --deny-tool,
// --allow-all-paths) do not grant approval, so the non-interactive -p run still
// needs the default --allow-all-tools to avoid blocking on approval prompts.
func copilotUserSetPermissionMode(extraArgs []string) bool {
	for _, arg := range extraArgs {
		switch {
		case arg == "--allow-all-tools",
			arg == "--allow-all",
			arg == "--yolo",
			arg == "--allow-tool":
			return true
		case strings.HasPrefix(arg, "--allow-tool="):
			return true
		}
	}
	return false
}

// copilotUserSetAskUser reports whether extraArgs already control the ask_user
// tool, in which case buildArgs skips its default --no-ask-user.
func copilotUserSetAskUser(extraArgs []string) bool {
	for _, arg := range extraArgs {
		if arg == "--no-ask-user" {
			return true
		}
	}
	return false
}

// buildCopilotPrompt appends a JSON-output contract to the user prompt when a
// schema is provided. The Copilot CLI has no equivalent of codex's
// --output-schema flag, so we inline the schema in the prompt the same way pi
// and rovodev do, then parse the final text with finalizeTextResult.
func buildCopilotPrompt(prompt string, schema json.RawMessage) string {
	if len(schema) == 0 {
		return prompt
	}
	pretty, err := json.MarshalIndent(json.RawMessage(schema), "", "  ")
	if err != nil {
		pretty = []byte(schema)
	}
	return prompt + "\n\n## no-mistakes final output contract\n\n" +
		"When the task is complete, your final assistant response must be only valid JSON matching this JSON Schema. " +
		"Do not wrap it in Markdown fences. Do not include prose before or after the JSON object.\n\n" +
		string(pretty)
}

// copilotEvent is the top-level JSONL event from the copilot CLI.
type copilotEvent struct {
	Type     string            `json:"type"`
	Data     *copilotEventData `json:"data,omitempty"`
	ExitCode *int              `json:"exitCode,omitempty"`
}

type copilotEventData struct {
	// assistant.message_delta
	DeltaContent string `json:"deltaContent,omitempty"`
	// assistant.message
	Content      string `json:"content,omitempty"`
	OutputTokens int    `json:"outputTokens,omitempty"`
	// error / abort events
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	// session.task_complete
	Summary string `json:"summary,omitempty"`
	Success *bool  `json:"success,omitempty"`
}

// parseCopilotEvents reads JSONL from the reader and dispatches events. It
// streams assistant.message_delta content to onChunk, appends each non-empty
// assistant.message text to messages (oldest first), accumulates output tokens,
// captures the terminal session.task_complete payload (copilot's designated
// final response), and records the terminal result event's exit code.
func parseCopilotEvents(
	ctx context.Context,
	r io.Reader,
	onChunk func(string),
	usage *TokenUsage,
	messages *[]string,
	copilotErr *string,
	exitCode *int,
	completion *copilotCompletion,
) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), claudeScannerMaxTokenSize)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event copilotEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue // skip malformed lines
		}

		switch event.Type {
		case "assistant.message_delta":
			if event.Data != nil && event.Data.DeltaContent != "" && onChunk != nil {
				onChunk(event.Data.DeltaContent)
			}

		case "assistant.message":
			if event.Data == nil {
				continue
			}
			usage.Add(TokenUsage{OutputTokens: event.Data.OutputTokens, Reported: true})
			if event.Data.Content != "" && messages != nil {
				*messages = append(*messages, event.Data.Content)
			}

		case "error", "assistant.abort":
			if event.Data != nil && copilotErr != nil {
				if msg := firstNonEmpty(event.Data.Message, event.Data.Error); msg != "" {
					*copilotErr = msg
				}
			}

		case "session.task_complete":
			if event.Data == nil || completion == nil {
				continue
			}
			*completion = copilotCompletion{
				Summary:  event.Data.Summary,
				Success:  event.Data.Success == nil || *event.Data.Success,
				Reported: true,
			}

		case "result":
			if event.ExitCode != nil && exitCode != nil {
				*exitCode = *event.ExitCode
			}
		}
	}

	return scanner.Err()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
