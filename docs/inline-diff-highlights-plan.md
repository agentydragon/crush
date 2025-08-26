# Inline diff highlights — implementation state (external-only)

This document captures the end-to-end state of the inline word/substring highlighting feature in the diff view, including design decisions, UX behavior, configuration, test coverage, limitations, and follow‑ups.

## Scope and approach

- External-only word diff: We rely exclusively on an external provider (git) for word‑level changes.
- No local heuristics: When external word‑diff is unavailable, we fall back to whole‑line +/- rendering.
- Source of truth: `git diff --word-diff=porcelain` (invoked per line pair for simplicity and stability).

## Implementation summary

- Per-line external adapter (done)
  - API: `diff.PerLineWordPieces(before, after) -> []Piece{Equal|Insert|Delete}` via `git --word-diff=porcelain -U0`.
  - Parses porcelain output into minimal piece streams (equal/ins/del) for a single logical line.

- DiffView integration (done)
  - Optional provider: `InlineProvider(before, after) -> ([]InlinePiece, bool)`.
  - Unified view:
    - Single-line replace (- followed by +) collapses to a single neutral row with inline red/green spans.
    - If -/+ are identical after newline-trim, collapse to Equal.
    - Multi-line change runs are grouped: all Delete rows first, then all Insert rows.
  - Split view:
    - For single-line changes present on both sides, apply red spans on the left (deletions), green on the right (insertions).
    - For multi-line runs, do not cross‑pair deletes with inserts; left shows dense sequence of deletions, right shows own insert rows later (prevents left-side numbering gaps).

- ANSI/layout handling (done)
  - Assemble spans first, then apply ANSI-aware Cut/Truncate with xOffset/width.
  - Strip CR/LF from inline content to prevent row breaks before rendering.
  - Equal inline pieces render as plain text (inherit line background) to avoid nested background artifacts.

- Config wiring (done)
  - Enabled when:
    - `options.diff.external_command` contains `--word-diff=porcelain`, and
    - `options.diff.parse_mode` is `git_word_porcelain` (or `auto`).

## UX behavior (concise)

- Unified
  - Equal lines: unchanged.
  - Single-line replacements: a single neutral row with inline red/green spans.
  - Identical -/+ pairs: collapsed to a single Equal row.
  - Multi-line runs: contiguous Deletes first, then Inserts.
  - Line numbers advance appropriately for collapsed and grouped rows.

- Split
  - Equal/insert/delete rows as usual.
  - For single-line changes present on both sides: red spans on left, green on right.
  - For change runs: left deletions do not skip; right inserts render as separate rows afterwards.

## Test coverage (goldens)

- BasicOps (Add/Delete/Edit) — Unified and Split.
- Boundary cases — no trailing newline before/after, start/end of files.
- Word-diff partial inline (Replace/Insert/Delete middle word) — Unified and Split.
- Grouped multi-line run with inline single-line replacement — Unified.
- Stress case — many small edits within a single line (inline spans correctness).
- Width/offset/y‑offset sweeps and narrow/large layouts (regression coverage).
- Goldens disable syntax highlighting for determinism.

## Configuration

- Example (global):
  ```json
  {
    "options": {
      "diff": {
        "external_command": "git diff --no-index --histogram --minimal --word-diff=porcelain -U3 -- a {old} -- b {new}",
        "parse_mode": "git_word_porcelain",
        "ignore_indent_changes": true
      }
    }
  }
  ```
- Tests that hit external git require `CRUSH_TEST_ENABLE_WORDDIFF=1` (for golden generation/verification only).

## Known limitations

- External dependency: Requires `git` available on PATH; porcelain format is the only supported source.
- Per-line process cost: One external invocation per changed line pair (acceptable for typical diffs; see Follow‑ups for caching).
- Very large lines: Inline disabled by planned thresholds to avoid noise/perf issues (see Follow‑ups).
- Styling interactions: We sanitize newlines and avoid re-styling equal spans, but extreme ANSI nesting could still be heavy for very long lines.
- Non‑ASCII/graphemes: We use ANSI grapheme-aware cut/truncate; rare edge cases may still appear with complex scripts.
- No whole‑file porcelain mapping: We don’t attempt to stitch full‑file porcelain into the view; simpler, per‑line calls are used instead.
- Cross‑platform: Behavior depends on `git` porcelain consistency; Windows/Cygwin/MSYS environments may differ.

## Follow‑ups / future work

- Inline thresholds (planned)
  - Disable inline for high change ratios (e.g., >X%) or piece counts (e.g., >N) per line.
  - Add explicit golden tests that exercise threshold boundaries.

- Memoization (planned)
  - Cache per‑line adapter results keyed by `(before, after)` content hash in view scope.
  - Consider global short‑lived memoization across rerenders.

- UX polish (optional)
  - Additional cues (underline/bold) configurable for inline pieces.
  - UI toggles (enable/disable inline) and per‑view override of thresholds.

- Whole‑file porcelain path (future)
  - Single external invocation for the entire file pair; parse to hunks/line pieces directly.
  - Trade‑off: more complex mapping logic, better performance at scale.

- Telemetry / diagnostics (optional)
  - Counters for external calls, timeouts, truncation events, and threshold hit rates.

## Troubleshooting

- No inline spans appear:
  - Verify config has `--word-diff=porcelain` and `parse_mode: git_word_porcelain`.
  - Ensure `git` is on PATH.
- Unexpected extra line breaks:
  - CR/LF in content were observed previously; we now strip them. If seen again, gather the specific file and create a test.
- Interleaved -/+ in unified:
  - Multi-line runs should group (- then +). If not, capture the minimal before/after that reproduces.

## Status snapshot

- Core inline provider + renderer integration: done.
- Grouping/pairing adjustments in unified/split: done.
- CR/LF sanitization and equal-span styling fix: done.
- Goldens updated; stress and grouping tests present.
- Remaining: thresholds + memoization; optional aesthetic/UX improvements; optional whole‑file path.
