# Crush Distributed Architecture — Design Draft

Status: Draft
Owner: mpokorny

## Goals

- Decouple UI, agent runners, and state so runners continue while UIs come and go (or the laptop is off).
- Support multiple runner types/environments: local raw, local sandboxed, Docker/Kube online/offline, Jupyter-kernel MCP, etc.
- Preserve current single-process UX (local mode) as a first-class option.
- Introduce a durable control plane with a simple, reliable event stream for UI(s) and runners.
- Keep the agent/tools programming model stable; add environment selection and policy via predicates and a sandbox policy spec.

## Components

1) Control Plane (crushd)
- Owns persistence and exposes APIs (HTTP+JSON, SSE for events; optional gRPC later).
- Scheduler/leases for runs; permission broker; metrics/heartbeats.
- Backing store: Postgres (preferred). For dev/local: SQLite behind a single crushd.

2) Runners (crush-runner)
- Stateless executors. Lease work from crushd, stream deltas/events, write messages/files, run tools.
- Configure one or more execution environments per runner (local/raw, local/sandboxed, container, kube pod, jupyter MCP, …).

3) UI Clients (TUI/CLI/Web)
- Pure clients. CRUD sessions, stream events, control runs (start/cancel/summarize), respond to permission prompts.
- Fallback to Local Mode (in-proc services and pubsub) when no remote configured.

## Data Model (additions)

Existing tables remain (sessions, messages, files). New tables (Postgres syntax):

```sql
-- Runs represent a unit of agent work attached to a session
create table runs (
  id uuid primary key,
  session_id uuid not null references sessions(id) on delete cascade,
  status text not null, -- PENDING | LEASING | RUNNING | WAITING_INPUT | CANCELED | COMPLETED | ERROR
  requested_by text,    -- user or system actor
  model text,           -- effective model ID/type
  provider text,
  environment_id uuid,  -- chosen execution environment
  summary boolean default false, -- if this run is a summarization/compact
  cancel_requested boolean default false,
  error text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz,
  runner_id uuid,       -- runner that leased it
  last_heartbeat timestamptz,
  effective_policy jsonb -- approved/enforced policy (see PolicySpec)
);
create index runs_session_id_idx on runs(session_id);
create index runs_status_idx on runs(status);

-- Permission requests centralized
create table permission_requests (
  id uuid primary key,
  session_id uuid not null references sessions(id) on delete cascade,
  run_id uuid references runs(id) on delete cascade,
  tool_call_id text,
  tool_name text not null,
  action text not null,
  description text,
  params jsonb,
  path text,
  status text not null, -- PENDING | GRANTED | GRANTED_PERSISTENT | DENIED
  requested_policy jsonb, -- PolicySpec proposed by tool/agent or control plane
  approved_policy jsonb,  -- PolicySpec after approval (typically requested_policy or a UI-tweaked variant)
  created_at timestamptz not null default now(),
  decided_at timestamptz
);
create index permission_requests_session_idx on permission_requests(session_id);
create index permission_requests_status_idx on permission_requests(status);

-- Execution environments registry (no global "trust" enum; use labels + defaults)
create table execution_environments (
  id uuid primary key,
  name text not null,         -- e.g., 'local-sandbox', 'docker:proj-x'
  type text not null,         -- LOCAL_RAW | LOCAL_SANDBOX | DOCKER | KUBE | JUPYTER_MCP
  online boolean not null,    -- true if network allowed by default
  capabilities jsonb,         -- declared tool set, limits, e.g. {"tools":["bash","edit"],"cpus":2}
  labels jsonb,               -- arbitrary tags (e.g., ["sandboxed","offline-default","no-root"]) 
  default_policy jsonb,       -- PolicySpec applied by default in this environment
  config jsonb,               -- type-specific config (image, namespace, kernel spec, mounts, etc)
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

-- Runners register themselves
create table runners (
  id uuid primary key,
  name text,
  version text,
  last_seen timestamptz not null default now(),
  environments jsonb,     -- subset/instances this runner can host
  metadata jsonb
);
```

Notes
- We continue to persist message parts as typed JSON in messages.parts (unchanged), with frequent updates (assistant streaming) as today.
- Files remain versioned; in distributed mode, versions represent runner-side workspace writebacks (see Workspaces).

## API Sketch (HTTP JSON, SSE)

Base path: `/api/v1`

Sessions/Messages/Files (CRUD)
- GET /sessions
- POST /sessions {title}
- GET /sessions/{id}
- GET /sessions/{id}/messages
- GET /messages/{id}
- GET /sessions/{id}/files
- GET /files/{id}

Runs
- POST /runs
  - body: `{ session_id, content, attachments?, summarize?:bool, environment_pref?: {id? | type? | labels?: [string], online?:bool}, model_override?, requested_policy? }`
  - returns run object
- GET /runs?session_id=&status=
- GET /runs/{id}
- POST /runs/{id}/cancel

Permissions
- GET /permission_requests?session_id=&status=
- POST /permission_requests/{id}/approve { persistent?:bool, policy_override?: PolicySpec }  // UI may adjust; predicates do not.
- POST /permission_requests/{id}/deny

Runners (control plane side)
- POST /runners/register { name, version, environments }
- POST /runners/{runner_id}/lease { max?: int }
  - returns array of leasable runs (usually 0 or 1) with optimistic-locked status -> LEASING
- POST /runners/{runner_id}/heartbeat { run_id }
- POST /runners/{runner_id}/update { run_id, status, error?, effective_policy? }

Execution Environments
- GET /environments
- POST /environments (admin)

Events (SSE)
- GET /events?topics=sessions,messages,files,runs,permissions,lsp
  - Event envelope:
  ```json
  {
    "topic": "messages",  
    "type": "created|updated|deleted|run_status|perm_pending|perm_decided|lsp_state|...",
    "id": "<uuid>",
    "ts": "2025-08-11T00:00:00Z",
    "payload": { ...typed... }
  }
  ```

Authentication
- Initial pass: bearer token or mTLS; later: per-user scopes, per-runner scopes.

## Run State Machine

```
PENDING -> LEASING -> RUNNING -> (WAITING_INPUT -> RUNNING)* -> COMPLETED
                                   |                                |
                                   |                                v
                                   +------> CANCELED / ERROR <-------
```

- Lease: control plane flips PENDING->LEASING with runner_id (optimistic lock). Runner confirms and flips to RUNNING + started_at.
- WAITING_INPUT: set when permission request is persisted with status=PENDING; resume RUNNING on GRANTED/GRANTED_PERSISTENT; DENIED -> ERROR.
- Heartbeats: runner updates last_heartbeat on RUNNING. Control plane reclaims on timeout (LEASING/RUNNING with stale heartbeat -> reset to PENDING, or ERROR if not safe to resume).

## Permission Flow (centralized)

- Runner calls tools; policy engine evaluates requested_policy + context via predicates to decide allow/deny/punt.
- If punt → prompt UI; if deny → return structured denial; if allow → persist approved_policy (== requested_policy unless UI adjusted/defaults applied) and continue.
- Predicates return only decision (and optional reason); they do not mutate policy.

## Execution Environments & Policy (no global trust enum)

- Environments advertise capabilities, labels, defaults. Labels (e.g., sandboxed, offline-default, no-root, gpu) and default_policy describe what’s typical in that environment; they are not a linear trust scale.
- The same tool in different environments can be subject to different default policies. Predicates can further constrain via decisions (deny if policy insufficient), but do not edit policies.

Tool naming & selection
- Expose explicit tool variants to the model where useful (e.g., `mcp__sandboxed_local_bash`, `mcp__raw_local_bash`) and bind generic tools (e.g., `bash`) to the current environment’s executor.
- System prompt advises the agent to prefer sandboxed paths; raw local can be disabled entirely by policy.

## Predicate-based permissions alignment

- RequestContext includes environment metadata: `{ env: { id, type, labels, online, default_policy } }` and the `requested_policy`.
- Rules (Starlark or Lua) return: `{ decision: "allow"|"deny"|"punt", reason?: string }`.
- If a rule wants “allow only if offline + write-only to /workspace”, it encodes this as denial when the requested_policy lacks those constraints.
- Precedence remains: Skip/YOLO → Allowlist → Predicates (deny > allow > punt) → Session-grants → Prompt.
- Storage: rules live local in Local Mode; crushd can ingest/export rule sets for distributed mode.

### Starlark-first predicate runtime

- Starlark for clarity/determinism; Lua may be added later with the same RequestContext surface.
- Host provides helpers: pathWithin, parseShellPipeline, policy.has_constraints(requested_policy, wanted), env.has_label(), network.is_readonly_get(requested_policy), etc.
- No policy mutation from predicates.

## Execution Policy & Sandbox DSL

Purpose: the caller declares `requested_policy`; rules approve or deny based on whether it satisfies organizational constraints.

PolicySpec (JSON shape, transport-agnostic)
```jsonc
{
  "fs": {
    "read_only": ["/usr", "/lib"],
    "write": ["/workspace", "/tmp"],
    "block": ["/etc", "/home"],
    "follow_symlinks": false
  },
  "process": {
    "cwd": "/workspace",
    "env_allow": ["HTTP_PROXY", "HTTPS_PROXY"],
    "ulimits": {"cpu_seconds": 300, "rss_mb": 2048, "nofile": 4096}
  },
  "network": {
    "mode": "offline" | "proxy" | "direct",
    "allow_domains": ["example.com", "*.acme.io"],
    "allow_methods": ["GET", "HEAD"],
    "proxy_url": "http://proxy.internal:8080"
  },
  "caps": {
    "seccomp": "default-deny", 
    "drop": ["NET_RAW"],
    "namespaces": ["mount", "pid", "uts", "user", "net"],
    "no_new_privs": true
  },
  "timeouts": {"wall_seconds": 600}
}
```

Evaluation & Approval
- Tool call may include `requested_policy` (agent should propose appropriate constraints); if omitted, control plane can inject environment `default_policy`.
- Predicates evaluate the request; if allowed → `approved_policy` is the `requested_policy` (or UI-tweaked); if denied → caller/agent must resubmit with stricter policy.
- Runner enforces `effective_policy` per environment:
  - LOCAL_SANDBOX: nsjail/bwrap/landlock/seatbelt + iptables/utun; rlimits for CPU/mem; HTTP proxy envs when `network.mode=proxy`.
  - DOCKER: `--network=none` or egress policy + `--memory`, `--pids-limit`, seccomp profile, RO/WO bind mounts.
  - KUBE: Pod spec with ResourceLimits, SecurityContext (drop caps, seccomp), NetworkPolicy/egress, mounted volumes, injected proxy sidecar if needed.
  - JUPYTER_MCP: enforce via MCP server policy and kernel env; optionally tunnel via proxy tool.

UI/UX
- Permission dialog shows requested_policy; user can tighten; predicates do not auto-edit it.
- Rules can be minted to encode common constraints ("allow sandboxed docker bash with offline net and write limited to /workspace").

## Workspaces & Source Control

- Each run operates inside a workspace (path inside LOCAL_* or mounted volume in DOCKER/KUBE).
- Workspace can be bound to a git repo; runner clones repo at start (image/init hook) and uses per-agent credentials issued by a central git server.
- Runner’s edit/write tools modify workspace files; results persist in DB history (files table) for audit/preview.
- Optional: runner pushes branches/PRs to central git; UI links to PR.
- Hooks (setup scripts): image init scripts or per-environment setup hooks to provision deps, credentials, kernel MCP, etc.

### Git-sync workflow (optional, local-like UX)

- Dirty snapshot representation
  - Client computes a tree snapshot (e.g., via `git ls-files -m -o --exclude-standard` and content hash) and pushes as a synthetic commit to a server-side "dirty" ref (e.g., `refs/crush/dirty/<device>/<session>`).
  - Crushd exposes `/git/snapshots` to upload file blobs or a pack; server generates a commit with a special marker.
- Agent branch
  - Each run uses `refs/heads/crush/run/<id>` branching from the latest known base (either the repo default or the dirty snapshot base).
  - Runner applies edits as commits on that branch (per edit or batched), with commit messages referencing tool call IDs.
- Merge/rebase
  - If the user pushes new local commits (or a new dirty snapshot) while the run is executing, control plane injects a system message: "Upstream moved; rebase required" and can auto-run `git rebase --autostash` inside the runner; conflicts result in WAITING_INPUT with conflict summaries.
- Completion strategies
  - Immediate: every edit is a commit; the UI shows live branch diff.
  - Batched: one commit per tool step or per phase; final squash or PR.
  - Finalization: runner opens a PR/MR to a target branch; link is emitted to UI.
- APIs
  - POST `/git/snapshots` with a tar/pack of changed files → returns commit SHA.
  - POST `/runs/{id}/workspace/rebase` to instruct runner to rebase onto a new base SHA.
  - GET `/runs/{id}/diff` returns unified diff between base and tip for UI previews.

Trade-offs
- Avoids writing directly into the user's local FS; preserves a "local-edit-like" UX via near-live commits.
- Enables conflict detection and controlled resolution within the runner environment.

## LSP in distributed mode (optional)

- Sidecar LSP per workspace (container/pod), diagnostics forwarded as events (lsp_state/diagnostics_changed).
- For MVP: disable LSP in distributed; keep local LSP in Local Mode.

## Event Types (payload sketches)

- messages.created/updated/deleted → `{ id, session_id, role, parts, model, provider, created_at, updated_at }`
- runs.status → `{ id, session_id, status, runner_id, cancel_requested, error, started_at, finished_at, last_heartbeat, effective_policy }`
- permissions.pending → `{ id, session_id, run_id, tool_name, action, description, params, path, requested_policy }`
- permissions.decided → `{ id, status, approved_policy }`
- sessions.created/updated/deleted → existing shapes
- files.created (version) → `{ id, session_id, path, version, created_at }`
- lsp.state/diagnostics → `{ name, state, diagnostic_count }`

## Client/Service abstraction

- Introduce RemoteService interfaces mirroring current Services (Sessions, Messages, History, Permissions, Agent, LSP).
- Two impls:
  - LocalService (existing code/base, in-proc Broker)
  - RemoteService (HTTP client + SSE subscriber)
- TUI switches based on `CRUSH_REMOTE_URL` (or config file): if set → Remote; else Local.

## Scheduling & Heartbeats

- LeaseNextRun: atomic update on runs where status=PENDING and not cancel_requested.
- Runner heartbeat interval (e.g., 2–5s). Control plane reclaims if `now - last_heartbeat > reclaim_timeout`.
- Max concurrency per runner; optional run priority.

## Security

- Secrets: control plane stores per-agent git creds and environment secrets; deliver to runner via short-lived tokens or K8s secrets mounted to pods.
- Auth: bearer tokens for UI/runner; mTLS optional.
- Logging: never log secrets; scrub params.

## Backwards compatibility / Local Mode

- Keep current app behavior; if no remote is configured, use LocalService and in-proc pubsub.
- The Agent tool surface remains stable; environment preferences collapse to the local environment.

## Migration Plan (Phased)

Phase 0 (MVP lifecycle inside current app)
- Add runs state machine in-process using existing DB, expose in UI (“Running/Waiting/Done”).
- Centralize permission requests in the DB (optional stub table behind current broker) while preserving current UX.

Phase 1 (crushd + RemoteService)
- Implement crushd with HTTP+SSE APIs over existing schema (SQLite acceptable for dev). Port Services to RemoteService.
- Add CRUSH_REMOTE_URL toggle in TUI.

Phase 2 (Runner binary)
- Implement crush-runner: register, lease, heartbeat, execute agents with streaming writes to crushd.
- Map tools to configured environments; enforce policy.
- Implement cancel/waiting-input handling.

Phase 3 (Kubernetes)
- Package crushd (StatefulSet + Postgres) and runners (Deployment/Autoscaled). Add optional LSP sidecars and Jupyter MCP sidecars.
- Secrets distribution and per-agent git credentials.

## Configuration (examples)

```yaml
remote:
  url: https://crushd.example.com
  token: $CRUSHD_TOKEN

runner:
  name: my-laptop
  environments:
    - name: local-sandbox
      type: LOCAL_SANDBOX
      labels: [sandboxed, offline-default]
      online: false
      capabilities:
        tools: [bash, edit, write, view, glob, grep]
        cpus: 4
      default_policy:
        fs: { write: ["$WORKSPACE", "/tmp"], read_only: ["/usr", "/lib"], block: ["/etc"] }
        network: { mode: offline }
    - name: docker-dev
      type: DOCKER
      labels: [sandboxed]
      online: true
      config:
        image: ghcr.io/org/dev:latest
        mounts: [{ host: /repos/acme, guest: /workspace }]
        setup_hooks: [ ./scripts/runner-setup.sh ]
      capabilities:
        tools: [bash, edit, write, view, glob, grep, diagnostics]
      default_policy:
        caps: { seccomp: default-deny, drop: [NET_RAW], no_new_privs: true }

agent:
  environment_pref:
    labels: [sandboxed]
    online: false
  tools:
    prefer: [mcp__sandboxed_local_bash]
    avoid: [mcp__raw_local_bash]
```

## OpenAPI-ish endpoint examples

POST /runs
```json
{
  "session_id": "a4b1...",
  "content": "Add CI workflow and bump golangci-lint",
  "environment_pref": {"labels": ["sandboxed"], "online": true},
  "model_override": "gpt-4o-mini",
  "requested_policy": {"network": {"mode": "proxy", "allow_domains": ["api.github.com"], "allow_methods": ["GET"]}}
}
```

SSE /events (example event)
```json
{
  "topic": "runs",
  "type": "run_status",
  "id": "f12c...",
  "ts": "2025-08-11T12:00:00Z",
  "payload": {
    "id": "f12c...",
    "session_id": "a4b1...",
    "status": "WAITING_INPUT",
    "runner_id": "r-9e7...",
    "effective_policy": {"network": {"mode": "offline"}}
  }
}
```

## Implementation Notes

- Starlark-first for predicates (decision + reason only); optional Lua backend with the same RequestContext later.
- Start with RemoteService for Messages/Sessions/Files/Permissions/Agent that mirrors current interfaces; replace pubsub wire with SSE.
- In Agent provider loop, runners persist deltas the same way we do locally; event throughput scales with DB row updates.
- Tool layer gets an Environment abstraction that provides execution adapters (local sandbox/raw, docker, kube, MCP) and policy enforcement.
- PolicySpec parser/normalizer lives in control plane; environment adapters translate to enforcement knobs (nsjail/bwrap/landlock/seatbelt, docker flags, k8s securityContext/networkPolicy, proxy envs).

## Acceptance Criteria

- UI can connect to crushd, list sessions, stream live message deltas for runs executing on remote runners.
- Starting 5 runs, quitting UI, reconnecting later shows accurate statuses: X running, Y waiting for input, Z done.
- Permission prompts persist and unblock runs after approval.
- Approved policies are enforced (offline/readonly, fs write scopes, time/mem limits); raw local without policy is denied by default.
- Runner can operate in Docker or Kube with offline mode enforced; optional Jupyter MCP exposed as tool provider.

## Risks & Mitigations

- Streaming reliability → SSE with reconnect + resume (last-event-id); store events with monotonically increasing ids.
- Workspace drift/conflicts → isolate per run branch; PR flow; or single-writer per session.
- Secret management → avoid logging; short-lived tokens; K8s secrets and per-agent credentials.
- Cross-platform sandbox differences → start with Linux-first (nsjail/bwrap) and Docker/K8s; macOS seatbelt adapter as follow-up.

## Open Questions

- Resume semantics for partially streamed messages (DB updates are safe, but event replay needs an event store or catch-up query).
- Multi-run per session policy (allow vs serialize?).
- LSP scope/scale in cluster; do we centralize diagnostics or per-run sidecar only?
- Should PolicySpec support compositional graph operators (all_of/any_of) vs. simple merge semantics only?
