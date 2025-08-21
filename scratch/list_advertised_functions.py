#!/usr/bin/env python3
import io
import json
import os
import sys
import gzip
from pathlib import Path
from typing import Iterable

LOG_DIR = Path(os.path.expanduser("~/.crush/logs"))
CUR = LOG_DIR / "provider-wire.log"

files: list[Path] = []
if CUR.exists():
    files.append(CUR)
# Include a few most recent rotated gz logs
if LOG_DIR.exists():
    gz = sorted(LOG_DIR.glob("provider-wire-*.log.gz"), key=lambda p: p.stat().st_mtime, reverse=True)
    files.extend(gz[:5])

if not files:
    print("No provider wire logs under ~/.crush/logs", file=sys.stderr)
    sys.exit(0)

functions: set[str] = set()

def iter_lines(p: Path) -> Iterable[str]:
    if p.suffixes[-2:] == ['.log', '.gz']:
        with gzip.open(p, 'rt', encoding='utf-8', errors='ignore') as f:
            for line in f:
                yield line
    else:
        with p.open('r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                yield line

def extract_from_obj(obj: dict):
    payload = obj.get('payload') or {}
    tools = payload.get('tools')
    if not isinstance(tools, list) or not tools:
        return
    for t in tools:
        name = None
        if isinstance(t, dict):
            # Responses style: name at top-level, type=function
            if isinstance(t.get('name'), str) and (t.get('type') == 'function' or 'parameters' in t):
                name = t['name']
            # Chat Completions style
            if not name:
                fn = t.get('function')
                if isinstance(fn, dict):
                    name = fn.get('name') or name
            # Responses union style {"OfFunction": {"name": ...}}
            if not name:
                ofn = t.get('OfFunction')
                if isinstance(ofn, dict):
                    name = ofn.get('name') or name
            # Capitalized variant
            if not name:
                cap_fn = t.get('Function')
                if isinstance(cap_fn, dict):
                    name = cap_fn.get('name') or cap_fn.get('Name') or name
        if name:
            functions.add(name)

for p in files:
    try:
        for line in iter_lines(p):
            line = line.strip()
            if not line or not line.startswith('{'):
                continue
            try:
                obj = json.loads(line)
            except Exception:
                continue
            # Either explicit event_type for streaming request or generic direction=request
            if obj.get('direction') != 'request' and obj.get('event_type') != 'chat.completions.new_streaming':
                continue
            extract_from_obj(obj)
    except Exception as e:
        print(f"warn: failed to read {p}: {e}", file=sys.stderr)

for n in sorted(functions):
    print(n)
