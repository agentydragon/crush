# OpenAI Responses/LLM integration — prioritized TODOs

## P0 — High priority (safety, recoverability, test ground truth)

- Provider preflight & safe fallback
  - [ ] Before sending to provider, validate conversation consistency (e.g., assistant tool_calls without corresponding tool results) and apply a sane fallback.
  - [ ] Scope at an abstraction that covers multiple providers; add provider-specific patches where needed (OpenAI Chat and Responses).
  - [ ] Always surface a clear error (logs/UI) and continue via fallback (synthesize missing tool results, or restart the step), never hard-dead-end the user.

- Crash/resume handling
  - [ ] Detect orphaned tool_calls on session restore (assistant message with tool_calls but missing tool results), and recover sanely.
  - [ ] Chat Completions: synthesize missing tool messages from persisted tool results, or restart the step with a new request; prompt if needed.
  - [ ] Responses: ensure pending function_tool_call items are resolved or canceled before proceeding.
  - [ ] Add tests reproducing the 400 invalid_request_error and verify recovery logic.

- E2E ground-truth and mock alignment
  - [x] Stable e2e harness (mock + live) that writes artifacts to `e2e/_artifacts/` in a fully sandboxed per-test env (HOME, XDG_*, DataDirectory).
  - [x] Simple deterministic scenario with a single bash call; streaming-only; artifacts captured.
  - [x] Use provider wire log as live blueprint; assert `logs/provider-wire.log` exists under per-test artifact dir.
  - [ ] Auto-align mock event sequences with the latest provider-wire.log captured in the per-test sandbox; provide a small comparator/diff report.
  - [ ] Stop naming JSONs as basic.* and rely on test-name/timestamped paths only.

## P1 — Medium priority (UX switches, core flows, coverage)

- Explicit API selection switch
  - [x] Config switch (`generation_api: responses|chat`) chooses OpenAI API path independent of `CanReason`.
  - [x] Provider selection is gated on this switch; `CanReason` used only for reasoning options.
  - [ ] TUI config switch to toggle Responses vs Chat at runtime (writes to config), including discoverability and status display.

- TUI: Reasoning effort selector
  - [ ] Add selector to Models dialog (low/medium/high/none) and persist per model type; show only where supported.

- UI state fidelity for streaming/tooling
  - [ ] Separate states: thinking-in-progress, tool-args-streaming, tool-executing, tool-exec-failed, timeout/canceled.
  - [ ] Fix copy and transitions (no “waiting for tool response” while args are still streaming).
  - [ ] Acceptance: clear transitions on EventToolUseStart/Delta/Stop vs actual tool execution; no pending tool calls at final.

- Core scenarios
  - [ ] Parallel tool calls (A/B) with interleaved deltas/done; verify per-item tracking; no leakage at completion.
  - [ ] Tool execution failures/timeouts; assert UI reflects failure/timeout and final session has no lingering tool calls.

- Retry/backoff & observability
  - [ ] Rate-limit retry correctness and logs; record retry metrics and forced tool-stop warnings for observability.

- Comparator and CI integration
  - [ ] Optional tolerant comparator vs goldens (ignore timestamps/whitespace) or hand assertions.
  - [ ] Wire e2e target into CI (mock-only ok; live opt-in via secret).

## P2 — Lower priority (longer-tail)

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
