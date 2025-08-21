#!/usr/bin/env bash
# tmux_step.sh — atomic tmux REPL step helper
# Usage:
#   tmux_step.sh <session[:.pane]> <keys-to-send> [sleep_ms]
# Example:
#   tmux_step.sh crushdbg:.0 'p len(cfg.MCP)' 500
#   tmux_step.sh crushdbg:.0 'continue' 1000

set -euo pipefail
if [[ $# -lt 2 ]]; then
  echo "usage: $0 <session[:.pane]> <keys> [sleep_ms]" >&2
  exit 2
fi
PANE="$1"
KEYS="$2"
SLEEP_MS="${3:-400}"

TS=$(date +%s%3N)
BEFORE="scratch/tmux_${TS}_before.txt"
AFTER="scratch/tmux_${TS}_after.txt"
mkdir -p scratch

# snapshot before
if ! tmux capture-pane -pt "$PANE" > "$BEFORE" 2>/dev/null; then
  echo "error: tmux pane $PANE not found" >&2
  exit 3
fi

# send keys (single logical command, ENTER appended)
# Use bash -lc in pane if you need shell evaluation; here we trust REPL
# Send raw plus Enter
TMUX_SEND=(tmux send-keys -t "$PANE" "$KEYS" C-m)
"${TMUX_SEND[@]}"

# optional sleep
python3 - "$SLEEP_MS" <<'PY'
import sys, time
ms=int(sys.argv[1])
time.sleep(max(0, ms)/1000.0)
PY

# snapshot after
if ! tmux capture-pane -pt "$PANE" > "$AFTER" 2>/dev/null; then
  echo "error: tmux pane $PANE disappeared" >&2
  exit 4
fi

# show succinct diff (context 3)
# If no diff, emit a notice
if diff -u "$BEFORE" "$AFTER" > "${AFTER}.diff"; then
  echo ">>> sent: $KEYS"
  echo "(no visible change)"
else
  echo ">>> sent: $KEYS"
  echo "--- before"
  echo "+++ after"
  # Print with some context; trim huge outputs
  awk 'NR>1000{exit} {print}' "${AFTER}.diff"
fi
