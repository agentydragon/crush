#!/usr/bin/env bash
set -euo pipefail
DB=${1:?"usage: check_ids2.sh <db_path> [session_title_substr]"}
SESSION_TITLE_SUBSTR=${2:-"Architecture review and improvement suggestions"}
if [[ ! -f "$DB" ]]; then
  echo "DB not found at $DB" >&2
  exit 2
fi
# 1) Find session id
SID=$(sqlite3 "$DB" "SELECT id FROM sessions WHERE title LIKE '%' || replace('$SESSION_TITLE_SUBSTR','\'','''') || '%' ORDER BY created_at DESC LIMIT 1;")
if [[ -z "$SID" ]]; then
  echo "No session found matching title substring: $SESSION_TITLE_SUBSTR" >&2
  exit 3
fi
# 2) Extract assistant tool call parts (JSON) -> IDs
ASSISTANT_PARTS=$(sqlite3 -cmd ".param init" -cmd ".param set sid $SID" "$DB" "SELECT parts FROM messages WHERE session_id = $sid AND role='assistant' ORDER BY created_at;")
# 3) Extract tool results parts (JSON) -> tool_call_id
TOOL_PARTS=$(sqlite3 -cmd ".param init" -cmd ".param set sid $SID" "$DB" "SELECT parts FROM messages WHERE session_id = $sid AND role='tool' ORDER BY created_at;")
ASS_IDS=$(printf "%s\n" "$ASSISTANT_PARTS" | jq -r 'try fromjson // empty | map(select(type=="object" and has("id") and has("name"))) | .[] | [.id, .name] | @tsv' || true)
TR_IDS=$(printf "%s\n" "$TOOL_PARTS" | jq -r 'try fromjson // empty | map(select(type=="object" and has("tool_call_id"))) | .[] | [.tool_call_id, (.name // "")] | @tsv' || true)
ASS_ONLY=$(awk -F"\t" '{print $1}' <<<"$ASS_IDS" | sort -u)
TR_ONLY=$(awk -F"\t" '{print $1}' <<<"$TR_IDS" | sort -u)
ONLY_IN_ASS=$(comm -23 <(printf "%s\n" "$ASS_ONLY" | sort -u) <(printf "%s\n" "$TR_ONLY" | sort -u) || true)
ONLY_IN_TR=$(comm -13 <(printf "%s\n" "$ASS_ONLY" | sort -u) <(printf "%s\n" "$TR_ONLY" | sort -u) || true)
echo "SESSION_ID=$SID"
echo "ASSISTANT_TOOLCALL_IDS:"
printf "  %s\n" $ASS_ONLY | sed '/^$/d'
echo "TOOL_RESULT_IDS:"
printf "  %s\n" $TR_ONLY | sed '/^$/d'
echo "ONLY_IN_ASSISTANT (no tool_result):"
printf "  %s\n" "$ONLY_IN_ASS" | sed '/^$/d'
echo "ONLY_IN_TOOL_RESULTS (no assistant toolcall):"
printf "  %s\n" "$ONLY_IN_TR" | sed '/^$/d'
