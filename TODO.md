# OpenAI Responses/LLM integration — prioritized TODOs

## P0 — High priority (common, high-impact; unblock confident iteration)

- UI state fidelity for streaming/tooling
  - [ ] Separate states: thinking-in-progress, tool-args-streaming (model streaming function_call arguments), tool-executing, tool-exec-failed, timeout/canceled.
  - [ ] Current issue: UI shows “waiting for tool response” while arguments are still streaming (tool not running yet). Fix copy and state transitions.
  - [ ] Acceptance: clear transitions on EventToolUseStart/Delta/Stop vs actual tool execution start; no blended/lying states; no pending unanswered tool calls at final.

- E2E check stack foundation (enable fast, reliable dev)
  - [x] Restore stable e2e harness (mock + live) that always writes artifacts to `e2e/_artifacts/` and runs in a fully sandboxed per-test env (HOME, XDG_*, DataDirectory).
  - [ ] Refactor mock SSE to use OpenAI SDK response types (no hand-rolled JSON) for event payloads: output_item.added (function_call), function_call_arguments.delta/done, output_text.delta/done, response.completed (+optional response.created, reasoning deltas).
  - [x] Simple, deterministic scenario with explicit prompt and a single bash call; streaming-only; artifacts captured.
  - [ ] Add basic assertions on final logical chat state (no pending tool calls, sane finish reason, content present) to guard regressions.
  - [x] Use provider wire log as live blueprint; assert `logs/provider-wire.log` exists under per-test artifact dir.

- Explicit API selection switch
  - [x] Config switch (`generation_api: responses|chat`) chooses which OpenAI API path to use, independent of `CanReason`.
  - [x] Gate provider selection on this switch; keep `CanReason` for reasoning options only.
  - [ ] Add TUI config switch to toggle Responses vs Chat at runtime (writes to config), including discoverability and status display.

- Error/termination correctness
  - [ ] Handle `response.error` explicitly in streaming, propagate as EventError, and ensure channel closure.
  - [x] On completion, force-stop any started tool items that didn’t explicitly emit `.done` so UI final state is consistent.

## P1 — Medium priority (frequent enough, valuable coverage)

- Parallel tool calls scenario
  - [ ] Two function_tool_call items (A, B) with interleaved deltas/done; verify per-item tracking; no leakage at completion.

- Tool execution failures/timeouts
  - [ ] Simulate tool errors and timeouts; assert UI reflects failure/timeout state and final session has no lingering tool calls.

- Retry/backoff & observability
  - [ ] Basic rate-limit retry correctness and logs; record retry metrics and forced tool-stop warnings for observability.

- Comparator and CI integration
  - [ ] Optional tolerant comparator vs goldens (ignore timestamps/whitespace); otherwise hand assertions are acceptable initially.
  - [ ] Wire e2e target into CI to catch regressions (mock-only is fine; live can be opt-in via secret).

## P2 — Lower priority (less common/longer-tail)

- Non-streaming parity
  - [ ] Only if/when used in prod; otherwise defer. Persist encrypted reasoning and reuse as reasoning items later at the service layer.

- Additional output item types
  - [ ] Consider `response.output_image.*` and future outputs to avoid dropping UI signals.

- Future high-level e2e/eval (regression tests)
  - [ ] “Set up sandbox, put agent into a state, run commands against real API, check final UI state is sane”, optionally “agent solved a simple task correctly.”
  - [ ] Run sparingly in CI (nightly or gated); primarily for regression/evaluation rather than fast dev loop.

## Notes

- Live runs must use OPENAI_API_KEY from env; streaming-only coverage for now.
- Prefer SDK-generated payloads for mocks to minimize drift.
- Keep initial scenarios simple and explicit to maximize reliability.

## New TODOs

- [ ] TUI: Add Reasoning Effort selector to Models dialog (low/medium/high/none) and persist per model type; show only for providers/models that support reasoning.
- [ ] E2E: Auto-align mock event sequences with the latest provider-wire.log captured in the per-test sandbox; provide a small comparator/diff report; stop naming JSONs as basic.* and rely on test-name/timestamped paths only.
- [ ] Crash/resume handling: Detect orphaned tool_calls on session restore (assistant message with tool_calls but missing tool results), and recover sanely.
  - For Chat Completions: either synthesize missing tool messages from persisted tool results, or restart the step with a new request; prompt the user if needed.
  - For Responses: ensure pending function_tool_call items are resolved or canceled before proceeding.
  - Add tests reproducing the 400 invalid_request_error and verify recovery logic.
- [ ] Provider preflight & safe fallback: before sending to provider, validate conversation consistency (e.g., assistant tool_calls without corresponding tool results) and apply a sane fallback.
  - Scope at an abstraction that covers multiple providers; add provider-specific patches where needed (OpenAI Chat and Responses).
  - Always surface a clear error (logs/UI) and continue via fallback (synthesize missing tool results, or restart the step), never hard-dead-end the user.
