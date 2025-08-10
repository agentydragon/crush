# Predicate-based Permission Rules (Proposal)

Status: Draft
Owner: you + Crush team

## Problem and goals

Crush currently prompts the user for tool permissions at runtime with optional session-persisted grants and an allowlist (permission/permission.go:137-141,175-203). This covers simple cases but becomes noisy and coarse when the user wants: (a) narrowly scoped, time-limited approvals; (b) reusable approvals that encode intent; (c) consistent policy across tools, files, and MCP servers.

Goals
- Expressive, code-first policy: user-defined predicates decide allow/deny/punt for each permission request.
- Time-scoped rules with expiration (e.g., “24h for edits under this project”).
- Fine granularity (tool, action, path, args, session metadata, MCP server/tool name) with shared helpers for safe parsing.
- Low friction UI: approve once → optionally mint a rule the user can inspect and edit.
- Backward compatible with existing prompts, allowlist, and YOLO.

Non-goals (initial)
- Auto-learning policies without explicit user action
- Network/OS sandboxing beyond existing command blockers
- Cross-machine rule sync

## Key concepts

- Predicate: executable function that evaluates a concrete permission request and returns a decision: Allow | Deny | Punt. Predicates run on every tool request; expiration (expiresAt) attaches to the rule’s metadata, not to an individual decision.
- Rule: a persisted predicate with metadata (id, title, description, createdAt, expiresAt, language, source_path/source_text, enabled).
- Shared library: host-provided helpers for common parsing and safety checks (e.g., parseShellPipeline, isSafeCommand, pathWithin, mcpToolSelector).
- Decision flow: a chain that consults (1) SkipRequests, (2) allowlist, (3) active rules, (4) session-persistent grants, (5) interactive prompt.

## Desired UX

When a tool requests permission and `on_unpermitted="prompt"`, the prompt panel offers:
- `Allow this one call`
- `Deny this one call`
- `Ask the LLM to request a longer permission` (delegates to Permissions Manager; the agent may propose one or more rules; user reviews source and approves or provides feedback)

Rule management:
- Rules pane: list active/expired rules with title, scope summary, expiresAt, pinned; enable/disable; extend; delete; open source; activate/deactivate pinned rules on demand. Janitor cleans only non-pinned expirations; expired pinned remain visible for reactivation.
- Decision explainability: for each allowed/denied request, show the matching rule or why it fell through to prompt.
- Permissions Manager (LLM-facing tool):
  - get_current_permissions returns:
    ```json
    {
      "mode": "prompt" | "soft_fail",
      "allowlist": ["bash", "write", "bash:execute"],
      "project_rules": [
        {"id":"...","title":"...","enabled":true,"pinned":false,"expired":false,"scope":"project","summary":"..."}
      ],
      "global_rules": [
        {"id":"...","title":"...","enabled":true,"pinned":true,"expired":false,"scope":"global","summary":"..."}
      ]
    }
    ```
  - request_permissions accepts:
    ```json
    [
      {
        "reactivate_rule_id": "rule-id-123", // mutually exclusive with source_code
        "description": "...",
        "source_code": "...", // Lua predicate
        "duration": "24h", // optional; sets rule expiry
        "scope": "project" | "global",
        "pinned": false,
        "labels": ["network", "read-only"],
        "scope_hint": "**/foo/*.py"
      }
    ]
    ```
    Returns:
    ```json
    {
      "granted": [{"id": "rule-id-123"}],
      "rejected": [{"id": "rule-id-456"}],
      "user_feedback": "not comfy with all edits; try config/*.yaml only"
    }
    ```
    Pending rules are not LLM-visible beyond this interaction.

## Rule evaluation pipeline

Given CreatePermissionRequest (permission/permission.go:18-26,165-173) and a normalized RequestContext:
1) SkipRequests? If yes → Allow
2) Allowlist? If tool or tool:action matches → Allow
3) Active rules (not expired): evaluate; precedence `Deny > Allow > Punt`
4) Session-persistent grants cache match? → Allow
5) Otherwise: if `on_unpermitted="prompt"`, show dialog; if `on_unpermitted="soft_fail"`, return structured “permission not granted; use Permissions Manager” to the LLM (not user-visible).

Notes
- Expiration: rule is ignored after expiresAt; background sweeper prunes.
- Determinism: rules should be fast, side-effect free, and deterministic; host enforces max eval time.

## Predicate API (language-agnostic)

Signature (conceptual):
- Input: { session: {id, title, nonInteractive}, tool: {name, action}, path, params, mcp: {server, tool}?, timeNow }
- Return: { decision: "allow" | "deny" | "punt", reason?: string }  // rule metadata carries expiresAt

Helpers provided by host:
- parseShellPipeline(cmd) → {executables: [], writesToPaths: [], networkOps: {domains: [], methods: []}}
- isSafeCommand(cmd)
- pathWithin(path, base)
- isWithinWorkingDir(path)
- isReadOnlyBash(cmd)
- `mcpToolSelector({server, tool})` → string key like `mcp:github:search`
- duration(n, unit) → timestamp (used at rule creation time; predicates do not return expiry)
Note: working-dir/safe-command logic should live in the shared predicate library surface (can be migrated out of Go over time).

## Example rules

Lua (via gopher-lua) examples

1) Allow edits under current project for 24h
```lua
function predicate(ctx)
  if ctx.tool.name == "edit" and ctx.tool.action == "write" then
    if host.pathWithin(ctx.path, host.env.workingDir) then
      return { decision = "allow", reason = "project edit window" }
    end
  end
  return { decision = "punt" }
end
```

2) Allow bash GET-only curl/wget to foo.com for 1h
```lua
function predicate(ctx)
  if ctx.tool.name == "bash" and ctx.tool.action == "execute" then
    local info = host.parseShellPipeline(ctx.params.command)
    if #info.networkOps.domains == 1 and info.networkOps.domains[1] == "foo.com" then
      if info.networkOps.methodsOnly and info.networkOps.methodsOnly["GET"] or info.networkOps.methodsOnly["HEAD"] then
        return { decision = "allow", reason = "allow read-only pulls from foo.com" }
      end
    end
  end
  return { decision = "punt" }
end
```

3) Deny destructive bash (rm -rf) always
```lua
function predicate(ctx)
  if ctx.tool.name == "bash" then
    local info = host.parseShellPipeline(ctx.params.command)
    for i, exe in ipairs(info.executables) do
      if exe.name == "rm" and exe.hasFlag("-rf") then
        return { decision = "deny", reason = "destructive rm" }
      end
    end
  end
  return { decision = "punt" }
end
```

4) MCP-scope: allow calling `mcp:github:search` only for repos under org "acme"
```lua
function predicate(ctx)
  if ctx.mcp and host.mcpToolSelector(ctx.mcp) == `mcp:github:search` then
    if ctx.params.repo and string.match(ctx.params.repo, "^acme/[
-_%w%.]+$") then
      return { decision = "allow" }
    else
      return { decision = "deny", reason = "only acme/* repos" }
    end
  end
  return { decision = "punt" }
end
```

Python/Starlark variants are similar; see Implementation choices.

## Rule metadata and Pin

Fields (conceptual)
- id, title, description, language, source, createdAt, updatedAt
- scope: "project" or "global" (storage location)
- expiresAt: wall-clock expiry for the rule; once reached, the rule stops evaluating until extended or reactivated
- enabled: participates in evaluation when true
- pinned: when true, janitor will NOT delete on expiry; rule stays visible (e.g., under "Expired (Pinned)") for quick reactivation. Useful for reusable "modes" you turn on/off.

Decision-time behavior
- Pin does not alter precedence (`Deny > Allow > Punt`)
- Expired+pinned: ignored by engine but preserved for reactivation
- Expired+not pinned: ignored by engine and eligible for cleanup

UX flows
- Add rule: via Permissions Manager request → user reviews source → Approve → saved (enabled) with chosen scope/expiry; optional “Pin” toggle in the dialog
- Remove rule: Rules panel → select → Delete (soft delete optional) or Disable (keeps source but stops evaluation)
- Pin/unpin: Rules panel → toggle Pin; pinned rules are protected from janitor cleanup and show under Expired (Pinned) when lapsed

LLM flows
- Reactivate vs. add new: agent prefers reactivation when a pinned rule roughly matches intended scope; call request_permissions with {reactivate_rule_id} to open renewal dialog
- If no suitable pinned rule, agent proposes a new rule; on approval, it’s added as usual
- get_current_permissions includes pinned and expired flags so the agent can decide between reactivation and proposing new

## Storage and lifecycle

- Location: ~/.crush/policies/ (global) and <project>/.crush/policies (project), each rule as a file (e.g., rule-<id>.lua) + rule.json metadata.
- Creation: via Permissions Manager tool (agent-driven) or API; prompt mode can delegate to agent (“Ask the LLM to request…”).
- Editing: open in editor via UI; hot-reload rules on save; syntax errors surface in UI.
- Expiration: metadata.expiresAt honored; background janitor disables expired rules; pinned rules are exempt from cleanup and remain visible for reactivation.
- Enable/disable per rule without deletion; pin/unpin for on-demand modes.

## UI affordances

Prompt dialog (prompt mode)
- Options: Allow this one call, Deny this one call, Ask the LLM to request a longer permission
- The third option hands control to the agent and opens the Permissions Manager dialog seeded by the agent’s request; user reviews source code before enabling

Rules panel
- Table: Title | Scope summary | Decision | Expires | Enabled | Language
- Actions: Enable/disable, Extend duration, Open source, Delete, Test against sample requests
- Detail drawer: full source, parse of scope, last N hits

Decision explainers
- Small “Why allowed/denied?” link on permission notifications showing: matching rule id/title, expiresAt, reason

## Backward compatibility and precedence

Order (first-match wins):
1) SkipRequests (YOLO) (permission/permission.go:125-129,215-221)
2) Allowlist (tool or tool:action) (permission/permission.go:137-141)
3) Predicate rules (within rules: Deny > Allow > Punt)
4) Session-persistent grants (permission/permission.go:75-93)
5) Prompt or soft-fail (as configured)

Config:
```jsonc
{
  "permissions": {
    "on_unpermitted": "prompt" // or "soft_fail"
  }
}
```

This preserves today’s semantics while adding a richer tier.

## Implementation choices (execution engine)

Safety/ergonomics options:
1) Lua (gopher-lua). Imperative and stateful; small runtime. Sandbox by default (no FS/net); expose only host helpers and a small scoped KV (rule/session/global).
2) Starlark (deterministic, no IO). Safer but less familiar for imperative state.
3) CEL (expression language). Small but limited for complex policies.
4) Python (CPython). Familiar but heavy and hard to sandbox.

Recommendation: start with Lua; add Starlark/Python adapters later as optional backends.

Host API surface (Go)
- type Decision { Allow, Deny, Punt }
- type Rule struct { ID, Title, Lang, Source, Scope, ExpiresAt, Enabled, Pinned, CreatedAt, UpdatedAt }
- RuleStore: List, Get, Add, Update, Remove, LoadActive()
- Engine: Evaluate(ctx, request) → (Decision, matchedRule?) with maxDuration per eval
- permission.Service: new hook EvaluatePredicates(request) before session cache (permission/permission.go:125-151) and after allowlist/skip
- UI: extend permission dialogs and add Rules TUI components

Parsing helpers
- Reuse/extend safe.go for read-only bash determination
- Add shell AST/lightweight parser (we already block commands in bash.go:275-304; reuse parse knowledge)

MCP integration
- Request context includes mcp server/tool names (agent/mcp-tools.go:90-160) so rules can match mcp:<server>:<tool> and params.

## Template predicates in shared library

Purpose: give LLM and users a concise, DRY vocabulary for common scopes and actions while keeping predicates readable and auditable.

Library surface (Lua examples)
- glob_scoped_edit(pattern, opts?)
  - Allows edit/write for files matching glob pattern (project-root relative)
  - opts: {exclude?: [globs], allow_create?: bool, within?: path}
- path_prefix_edit(dir, opts?)
  - Allows edit/write under dir
- bash_allow_readonly_http(hosts, opts?)
  - Allows bash commands that resolve to GET/HEAD network calls to listed hosts
- mcp_allow(server, tool, args_pred?)
  - Allows a specific MCP tool with optional args predicate

Example usage
```lua
function predicate(ctx)
  return host.templates.glob_scoped_edit("**/foo/*.py")
end
```

Composed usage
```lua
function predicate(ctx)
  local d = host.templates.glob_scoped_edit("config/**/*.yaml")
  if d.decision ~= "punt" then return d end
  return host.templates.bash_allow_readonly_http({"foo.com"})
end
```

Under the hood
- Templates compile to ordinary allow/deny logic using helpers (glob match, pathWithin, parseShellPipeline)
- They return {decision, reason} (rule carries expiresAt).
- Keep templates minimal and composable; encourage readable policy code.

## Security considerations

- Sandboxing: no filesystem or network from predicate code; expose only safe host helpers.
- Timeouts: per-eval deadline (e.g., 50ms) to avoid UI stalls.
- Resource bounds: memory caps per VM
- Code injection: show source before enabling; signed provenance if LLM generated; visually label “LLM-suggested”.
- Auditability: log rule matches/decisions (local only).

## Testing strategy

- Golden tests for engine precedence and expiration
- Fuzz tests for shell parser helpers
- UI integration tests for prompt → create rule → auto-allow
- Backward-compat tests for existing allowlist/skip/session cache

## Rough plan and estimate

Phase 1 (core, 5–8 days)
- RuleStore + Engine + Evaluate hook in permission.Service
- Starlark runtime + host helpers + unit tests
- Basic CLI/TUI: add “Add time-scoped rule…” with freeform title, duration; rule list panel (read-only)

Phase 2 (UX + LLM assist, 4–6 days)
- Editable rule source in TUI; enable/disable; extend duration; decision explainers
- LLM template to generate initial predicates from a request + chosen scope
- MCP-specific matching convenience helpers

Phase 3 (parsing + Lua adapter, optional 3–6 days)
- Shell parse helpers robust enough for wget/curl/sed/pipe cases
- Optional Lua engine; language selection per rule

Feasibility: High. Core is contained and integrates cleanly ahead of the current prompt. Risk concentrated in the predicate sandbox and shell parsing; both manageable if we start with Starlark and a conservative helper surface.

## Example flows

### A) Prompt mode: one-off approval then upgrade via LLM
1) Tool tries: edit file foo/bar.go
2) Engine: not YOLO, not allowlist, no matching predicate; session grant miss → prompt shown
3) User selects “Allow this one call”; tool proceeds; no rule added
4) Later, tool tries another edit; prompt shown again; user selects “Ask the LLM to request a longer permission”
5) Agent calls Permissions Manager.request_permissions with a proposed rule (24h, project-scoped). UI shows source; user edits and approves
6) Rule is stored (project scope, expiresAt set). Next matching edits auto-allow until expiry

### B) Soft-fail: agent-led preflight permissions
1) Agent plans to run bash curl to foo.com; tool returns soft-fail “permission not granted; use Permissions Manager” (not user-visible)
2) Agent aggregates likely needs (edit ./src, curl GET foo.com) and calls request_permissions with two rules
3) UI shows both rule sources with durations; user approves curl but rejects edit with feedback (“only config/*.yaml, 2h”)
4) Agent adapts, resubmits a narrower edit rule; user approves
5) Agent proceeds; rules expire later; pinned none → janitor cleans them up

### C) MCP scoping with pinned global rule
1) Agent wants mcp:github:search; soft-fail triggers
2) Agent requests a global rule “allow mcp:github:search for org acme/*” pinned=true, expires in 7d
3) User approves after reviewing source and reviewer summary; rule lives in ~/.crush/policies/global and is pinned
4) Future sessions reuse the rule until expiry; after expiry it remains visible (pinned) for reactivation
5) Reactivation UX: Rules panel → Expired (Pinned) → select rule → “Reactivate” sets a new expiry (suggested same as original) and enables it. LLM can also call request_permissions with {reactivate_rule_id: ...} to trigger the same dialog.

### D) Deny precedence
1) Two predicates: (a) Allow edit under project; (b) Deny editing vendor/**
2) Tool requests edit vendor/x.go; engine evaluates → Deny matches → request denied despite allow rule

## Appendices

- Current permission choke point: permission.Service.Request (permission/permission.go:125-203)
- Bash policy surface: safe vs. execute approval and command blocking (internal/llm/tools/bash.go:355-388,44-115)
- MCP tool permission wrapper: agent/mcp-tools.go:138-160
