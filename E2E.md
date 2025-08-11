# End-to-End (E2E) plan for OpenAI Responses API in Crush

## Goals
- Deterministic E2E validation of agent ↔ Responses API ↔ tools ↔ UI state updates
- Cover reasoning (summary and encrypted), function calls (incl. parallel), text, and final UI correctness
- Dual-mode: offline against a mock server and live against real OpenAI for side-by-side comparison

## Scope (initial)
- Single simple tool (Bash) end-to-end
- Streaming path (primary); add non-stream later
- Assertions on logical chat/message state (not pixel rendering)

## Harness architecture
- Test file: `e2e/agent_responses_e2e_test.go`
- Mock server: `httptest.Server` implementing `/v1/responses` (stream + non-stream)
- Provider config: baseURL → mock; model CanReason=true
- Agent: AllowedTools=["bash"]. Inject a fake Bash tool controllable via channels per tool itemID
- Orchestrator: drives the mock SSE sequence and tool completions; takes snapshots at checkpoints

## Mock Responses server behavior

- Build SSE payloads using the OpenAI SDK (github.com/openai/openai-go/responses) types; avoid hand-rolled JSON where possible so shapes match prod.
- Keep streaming-only coverage for now; skip non-streaming path until we actually use it in prod.
- Streaming endpoint emits SSE events in canonical order:
  - `response.created` (optional later)
  - `response.reasoning_summary_text.delta` (optional)
  - `response.output_item.added` with `function_tool_call` (multiple for parallel)
  - `response.function_call_arguments.delta` (interleaved per itemID)
  - `response.function_call_arguments.done` (per itemID)
  - `response.output_text.delta` (assistant text)
  - `response.output_text.done`
  - `response.completed` including `output` list with:
    - `reasoning` items (id, encrypted_content, optional summary)
    - `function_tool_call` items (id, name, arguments)
- Non-stream (later): respond with a single JSON body mirroring the final state

## Fake Bash tool
- Implements `tools.BaseTool`
- Input channel per tool itemID to unblock execution deterministically
- Produces `ToolResult` with configurable content/metadata and errors to test UI error states

## UI probe & assertions
- Probe via `message.Service` and `agent` side-effects:
  - Per assistant message: `IsThinking()`, `Content().Text`, `ReasoningSummaryContent{ID, EncryptedContent, Summary, FinishedAt}`
  - Tool calls: presence by itemID, `Finished` flag, incremental `Input` via `AppendToolCallInput`
  - Finish reason on completion; absence of any pending tool calls or “waiting for tool response…” at final
- Snapshot serializer (`e2e/timeline.go`) produces a compact JSON timeline with:
  - timepoint label
  - messages: {role, content_excerpt, reasoning:{id,enc,summary,finished}, tool_calls:[{id,name,finished,input_excerpt}]}

## Checkpoints (initial scenario)
1) After `output_item.added(function_tool_call A)`
   - Assert pending tool A exists, `Finished=false`, `IsThinking()` false
2) After first `arguments.delta` for A
   - Assert tool A `Input` includes delta
3) After `arguments.done` for A
   - Assert EventToolUseStop reflected (`Finished=true`)
4) After fake tool unblocks and returns ToolResult
   - Assert ToolResult message created; mock observes `function_call_output` request
5) After `response.completed`
   - Assert final assistant has expected text
   - `ReasoningSummaryContent` finished, and if present, `ID` and `EncryptedContent` stored
   - No unfinished tool calls; no waiting UI state

## Parallel tool calls (second scenario)
- Mock emits two `function_tool_call` items (A, B)
- Interleave `arguments.delta` for A/B
- Ensure the UI tracks per-itemID; both end `Finished=true` and final state has no pending tools

## Live mode (record and compare)
- If `E2E_LIVE=1` and `OPENAI_API_KEY` set:
  - Use real OpenAI client; model env `E2E_MODEL` (default `gpt-4o-mini`)
  - Run same scenario but skip strict event sequencing (cannot control server); assert primarily on UI/logical state
  - Save live artifacts under timestamped per-test directories in `e2e/_artifacts/` (includes `logs/provider-wire.log`)
  - Produce a normalized, readable JSON trace from provider-wire.log (canonicalizer strips volatile fields, tolerates optional events)
  - Diff normalized live trace vs committed mock trace; allow optional events and small usage variance; programmer reviews semantic changes and updates mock if needed
  - Keep mocks committed to git; they are the fast dev loop. Live runs are the ground truth safety net.

## Files & structure
- `e2e/mock_openai_responses.go` — helpers to emit SSE chunks per Responses spec
- `e2e/fake_bash_tool.go` — controllable tool implementation
- `e2e/timeline.go` — snapshot and diff utilities
- `e2e/agent_responses_e2e_test.go` — wired tests and scenarios
- `e2e/_golden/*.json` — expected logical timelines
- `e2e/_artifacts/*.json` — live captured timelines

## Execution
- Offline (mock): `go test ./e2e -run TestAgentResponsesScenarioBasic -v`
- Live compare: `E2E_LIVE=1 OPENAI_API_KEY=$KEY go test ./e2e -run TestAgentResponsesScenarioBasic -v`
- Update golden: `E2E_LIVE=1 E2E_UPDATE_GOLDEN=1 OPENAI_API_KEY=$KEY go test ./e2e -run TestAgentResponsesScenarioBasic -v`

## Future extensions

### Steppable mock server design (control-by-checkpoint)
- Goal: deterministically step the streaming server through checkpoints so tests can assert UI state between events.
- Model: a scenario is a sequence of checkpoints, each emitting a small batch of SSE events and flushing.
- API (test side):
  - s := NewSteppableMock(scenario)
  - ts := httptest.NewServer(s)
  - s.Next(ctx) // advance one checkpoint
  - s.NextN(ctx, n) // advance n checkpoints
  - s.AwaitToolOutput(ctx, toolID) // block until client posts function_call_output for toolID (then unlock next phase)
  - s.Done() // signal no more steps; close stream if active
- Scenario DSL: steps composed from primitives (Created, ReasoningDelta(text), AddTool(id,name), ArgsDelta(id,delta), ArgsDone(id), OutputTextDelta(text), OutputTextDone(), Completed(output,usage)). Parallel calls: steps can contain multiple emissions.
- Server impl: SSE writer goroutine per connection blocks on stepCh; when stepping, it writes the step’s events and flushes; if client disconnects, writer exits and drains.
- Safety: each Next has a timeout; server enforces single active connection; test must create agent after server.
- Tool phase: upon function_call_output POST, set sawFunctionCallOutput and allow subsequent Completed steps; tests can gate on s.AwaitToolOutput.
- Error injection: steps can include ResponseError to test UI error paths.
- Usage: tests interleave assertions and s.Next():
  1) Start agent → assert spinner; s.Next() (Created) → spinner still; s.Next() (AddTool+Args) → spinner off, tool pending; unblock tool; s.Next() (Text deltas) → assert; s.Next() (Completed) → final assertions.

- Streaming state edge cases to add:
  - Arguments delta arrives before output_item.added (ensure UI starts tool-args state upon first delta)
  - Done arrives without prior delta (still start/stop appropriately)
  - Multiple parallel tool calls with interleaved deltas; ensure per-item tracking and no leakage
  - Tool-exec-failed path (simulate tool error)
  - Timeout while arguments still streaming vs while tool executing
  - Ensure no pending tool calls at completion; emit forced stop if server omits .done

- Non-streaming path parity
- Additional incomplete reasons (max tokens, content_filter) and finish mapping checks
- Reasoning-only completions (encrypted_content present, no summary)
- Image inputs/outputs
- Bedrock/OpenRouter passthrough compatibility shims

---

## Target scalable step-driven scenario test infrastructure (design)

### Core principles
- One test function owns the entire scenario as ordered steps (Act → Eventually Assert), making the timeline explicit and readable.
- A strict global kill switch aborts the entire test package after 30s wall time; each step has a shorter budget (e.g. 2–5s). No step may outlive the global deadline.
- Tests assert on stable logical state (messages, tool calls, reasoning) rather than network or pixel output.
- Mock and live paths share the same test code by using an Orchestrator interface; live orchestrator is a no-op for Act, mock emits deterministic SSE.
- Snapshots are captured after each step for triage, and goldens are used for mock assertions.

### Building blocks
- ScenarioStep: Name, Act(ctx), Assert(t, ctx)
- ScenarioCtx: carries t, global deadline, per-step budget, services (agent, sessions, messages), sessionID, orchestrator, artifacts dir.
- Orchestrator interface:
  - Advance() — generic gate to move to the next checkpoint
  - EmitTextDelta(text)
  - EmitCompleted(content)
  - EmitAddTool(id, name), EmitArgsDelta(id, delta), EmitArgsDone(id) — for tool scenarios
  - Close()
- Eventually helper: polls a condition until success or until the earlier of per-step budget or global deadline; sleeps 10–20ms between polls.
- Kill switch: TestMain sets a time.AfterFunc(30s) that exits(2) on expiry, affecting mock and live runs equally.
- Snapshotting: after each step, serialize a compact timeline to artifacts and compare to goldens in mock mode (update via env flag).

### Mock server semantics
- Streaming endpoint is step-driven: a goroutine blocks on a channel of Step batches; each Step writes its events and flushes.
- Handling function_call_output: HTTP POSTs to /responses still go through the same handler; when the body contains function_call_output, record it and continue streaming. Do not early-return or short-circuit the SSE lifecycle.
- Safety/constraints: single active connection enforced; if client disconnects, writer exits; steps with WaitUntil allow gating on observed request bodies, signals, or sleeps.

### Live mode
- Same ScenarioStep code; Act calls are no-ops. Assertions wait for state to appear.
- Provider wire logs are collected under the per-test data dir. A normalizer produces a canonical JSON trace; diffs against a committed baseline inform mock updates.

### Test authoring pattern
- Write the scenario as a small array of steps. Each step:
  1) Performs Act via Orchestrator methods (mock: emit SSE; live: no-op)
  2) Asserts via Eventually on Messages/List() or Agent events
  3) Calls Snapshot(label) to leave breadcrumbs in artifacts
- Keep steps small, reflecting a single conceptual milestone.

### Worked example (tool-less, streaming)
```go
func TestScenario_ToolLess_Mock(t *testing.T) {
    ctx := NewScenarioCtx(t, WithGlobalDeadline(30*s), WithPerStep(2*s), WithArtifactsDir(...))
    orch := NewMockOrchestrator(NewSteppableMock()) // live: NewLiveOrchestrator()
    ctx.Orchestrator = orch
    ctx.Agent, ctx.Sessions, ctx.Messages = setupServices(...)
    ctx.SessionID = createSession(...)
    go ctx.Agent.Run(runCtx, ctx.SessionID, "Say ok")

    steps := []ScenarioStep{
        {
            Name: "assistant created",
            Act:  func(ctx *ScenarioCtx) { orch.Advance() },
            Assert: func(t *testing.T, ctx *ScenarioCtx) {
                ctx.Eventually("assistant exists", func() bool {
                    msgs, _ := ctx.Messages.List(context.Background(), ctx.SessionID)
                    return len(msgs) >= 2 && msgs[len(msgs)-1].Role == message.Assistant
                })
            },
        },
        {
            Name: "text delta + completion",
            Act: func(ctx *ScenarioCtx) {
                orch.EmitTextDelta("ok")
                orch.EmitCompleted("ok")
            },
            Assert: func(t *testing.T, ctx *ScenarioCtx) {
                ctx.Eventually("assistant finished with ok", func() bool {
                    msgs, _ := ctx.Messages.List(context.Background(), ctx.SessionID)
                    last := msgs[len(msgs)-1]
                    return last.IsFinished() && last.Content().Text == "ok"
                })
            },
        },
    }
    RunSteps(ctx, steps...)
}
```

### Properties we want to guarantee
- Determinism: Mock emits exactly the intended event sequence with flushes between steps.
- Isolation: Tests do not touch user home; all paths are sandboxed and data-dir is under per-test artifacts.
- Observability: Step-labeled snapshots in artifacts; live wire logs captured and normalized.
- Time-bounded: Hard kill at 30s for the whole package; each step has a tight budget.
- Extensibility: Add tool, parallel, error, timeout, and non-streaming scenarios without changing the harness shape.

### TODO (implementation plan)
- Harness
  - ScenarioCtx, ScenarioStep, RunSteps, Eventually helpers
  - Orchestrators: mock (emits SSE), live (no-op)
  - Artifact snapshot helper (reuse timeline.go)
- Mock server
  - Step channel, WaitUntil conditions (sleep, signal, request-body-contains)
  - Correct function_call_output handling (record, do not terminate SSE)
  - Error injection support
- Tests
  - Convert existing E2E to step-driven tool-less scenario (done first)
  - Add parallel tool-calls scenario and edge cases (delta-before-added, done-without-delta)
  - Add non-streaming parity scenario
  - Golden compare + E2E_UPDATE_GOLDEN flag
- Live mode
  - Wire-log normalizer and diff vs baseline
  - Nightly job for live runs with summaries
- Tooling
  - Per-test artifacts dir plumbing via config.Options.DataDirectory
  - Global TestMain 30s kill switch (already enforced)
