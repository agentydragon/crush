# Proposal: External Minimal Diff Engine

## Summary

Produce high‑quality minimal diffs (with word‑level highlights and smart local hunk detection) by delegating to an external diff program (default: Git) and parsing its output into our existing diff model. Keep the mechanism simple and transparent: a single configurable shell command is invoked with {old} and {new} placeholders. If the external tool is unavailable or fails, we fall back to the current internal line‑diff.

No UI configuration is needed. We also do not optimize for gigantic files; our usage is bounded by model context.

## Goals

- Minimal, stable diffs for typical code edits (avoid large hunks for small local changes).
- Intraline/word‑level highlighting for modified lines.
- Transparent and configurable external tool invocation.
- Safe fallback to existing internal diff when external diff is unavailable.

## Non‑goals

- UI knobs. The engine choice is configured once, not in the UI.
- Special handling for very large files.
- Implementing semantic diff (language‑aware) in the initial cut.

## Approach

1) External diff as the primary engine
- Default to Git, which has robust algorithms and stable flags for minimal diffs and word‑level highlighting.
- Command (default):
  - `git diff --no-index --histogram --minimal -U3 -- a {old} -- b {new}`
  - Rationale:
    - `--no-index`: compare paths outside a repo.
    - `--histogram --minimal`: produce smaller, more stable hunks in the presence of local edits.
    - `-U3`: show 3 lines of context; yields a standard unified diff for minimal integration.

2) Transparent configuration
- A single string in configuration controls the command template, with placeholders:
  - `{old}`: path to the temp file with the "before" contents.
  - `{new}`: path to the temp file with the "after" contents.
- Suggested config shape (example):
  - JSON path: `options.diff.external_command`
  - Defaults to the Git command above if not set or if Git is not installed.
- Examples:
  - Git (default):
    - `git diff --no-index --histogram --minimal -U3 -- a {old} -- b {new}`
  - Git with word-level (porcelain) parsing (set parse_mode):
    - `git diff --no-index --histogram --minimal --word-diff=porcelain -U3 -- a {old} -- b {new}`
  - Difftastic (pretty textual output; no structured spans):
    - `difft --display=inline --background=none --color=never {old} {new}`
  - GNU diff with minimal mode (line‑only):
    - `diff -u --minimal {old} {new}`

3) Fallback to internal diff
- If the external command is empty, missing, times out, or exits non‑zero without producing usable output, we use the current internal diff (go‑udiff) with the same API surface.
- Word‑level spans are only available when the external output supports them (Git porcelain); otherwise, the diff remains line‑only.

4) Integration points
- internal/diff:
  - Add a new runner that:
    - Writes `beforeContent` and `afterContent` to short‑lived temp files.
    - Expands the configured command template with `{old}` and `{new}`.
    - Executes the command with a short timeout.
    - Parses stdout into a structured diff model (Hunks, Lines, Word spans when available).
  - Keep the existing `GenerateDiff` function and make it call the external runner first, then fall back to the current udiff path.
- Parsing
  - For Git `--word-diff=porcelain`:
    - Parse hunk headers and +/-/context lines as usual (from unified segment of output, when present) OR
    - When porcelain segments are provided directly, recover line boundaries and map `add/remove` word markers to per‑line spans.
    - We only need enough to reconstruct: hunks with line numbers, line kinds (context/add/del), and word spans for add/del lines.
  - For other external tools: treat output as plain text unified diff (no word spans), or use it directly for text rendering when structured mapping isn’t feasible.

5) Security and reliability
- Use explicit temp files with restrictive permissions and predictable lifetimes.
- Quote/escape paths safely when constructing the command.
- Respect a short execution timeout (e.g., 1–2s) and small output size to avoid hangs.
- Log the exact command when debug logging is enabled.

## Configuration

- Default (no config):
  - Use Git command above if `git` is available on PATH.
  - If not available, fall back to internal line‑diff.
- Opt‑in custom command:
  - `options.diff.external_command = "difft --display=inline --background=none --color=never {old} {new}"`

## Data model

- Keep the existing return from `GenerateDiff` (unified text + counts) for callers.
- Internally create a structured model:
  - FileDiff {Hunks[]}
  - Hunk {OldStart, OldLines, NewStart, NewLines, Lines[]}
  - Line {Kind: context|add|del, Text, Spans?}
  - Span {StartColumn, EndColumn, Kind: add|del}
- Renderers:
  - Unified text (for logging, patches, downstream tools).
  - TUI renderer uses Line.Spans for intraline highlights (no UI options needed).

## Testing

- Golden tests for tricky edits:
  - Single‑word changes within long lines produce word spans.
  - Localized edits in duplicated blocks yield small hunks (histogram/minimal).
  - Insert/delete adjacency: correct pairing and intraline mapping.
- Fallback tests:
  - External tool missing → internal diff.
  - External tool returns non‑zero → internal diff.
- Cross‑platform sanity: paths and cmdline quoting on macOS/Linux/Windows.

## Rollout

- Land the external runner and parser behind the default command.
- Keep internal diff as fallback to ensure robustness.
- Document the `CRUSH_DIFF_CMD` environment variable and the `options.diff.external_command` config.

## Future work (optional)

- Add support for difftastic as a structured source if/when it provides machine‑readable spans.
- Add a language‑aware mode (opt‑in) using a semantic diff engine.
- Move detection and rename awareness (text‑only scope by default).
