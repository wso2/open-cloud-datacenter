#!/usr/bin/env python3
"""PreToolUse guard for the Bash tool.

Mechanically enforces the two "hard rules" from CLAUDE.md that have already
cost this project real work when left to memory:

1. No in-place stream editors (sed -i / perl -i / awk -i inplace).
   BSD sed on macOS has silently truncated tracked files to 0 bytes here —
   twice. Use the Edit tool (or Write for full rewrites) instead.

2. No `git commit -a` / `-am` / `--all`.
   Auto-staging sweeps up edits made by parallel sub-agents and has shipped
   untested changes. Stage specific files with `git add <paths>` instead.

Contract (Claude Code PreToolUse hook): receives the tool call as JSON on
stdin; exit 0 allows the call, exit 2 blocks it and feeds stderr back to the
model. Any parse failure fails open — this guard must never break unrelated
commands.
"""
import json
import re
import sys


def block(reason: str) -> int:
    print(reason, file=sys.stderr)
    return 2


def main() -> int:
    try:
        data = json.load(sys.stdin)
    except Exception:
        return 0
    command = (data.get("tool_input") or {}).get("command") or ""

    if (
        re.search(r"\b[gs]?sed\b[^|;&]*\s-[A-Za-z]*i", command)
        or re.search(r"\bperl\s+-[A-Za-z]*i", command)
        or re.search(r"\bg?awk\b[^|;&]*\binplace\b", command)
    ):
        return block(
            "BLOCKED by .claude/hooks/bash_guard.py: in-place stream editing "
            "(sed -i / perl -i / awk inplace) is banned in this repo — BSD sed "
            "has silently truncated tracked files to 0 bytes here twice. Use "
            "the Edit tool for in-place changes (or Write for full rewrites). "
            "Read-only sed/awk piped to stdout is fine."
        )

    commit = re.search(r"\bgit\b[^|;&]*?\bcommit\b([^|;&]*)", command)
    if commit:
        for token in commit.group(1).split():
            if token == "--all" or (
                re.fullmatch(r"-[A-Za-z]+", token) and "a" in token
            ):
                return block(
                    "BLOCKED by .claude/hooks/bash_guard.py: `git commit -a/"
                    "-am/--all` is banned in this repo — auto-staging has swept "
                    "up untested edits from parallel sub-agents before. Stage "
                    "the specific files with `git add <paths>`, review `git "
                    "status`, then commit."
                )

    return 0


if __name__ == "__main__":
    sys.exit(main())
