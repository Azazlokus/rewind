#!/usr/bin/env bash
# statusLine: одна строка статуса — проект, ветка, следующая итерация из CLAUDE.md.
# На stdin приходит JSON сессии; берём из него рабочий каталог.
set -uo pipefail

input=$(cat)
dir=$(printf '%s' "$input" | jq -r '.workspace.current_dir // .cwd // "."')
cd "$dir" 2>/dev/null || true

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')
next=$(grep -oE 'Следующая — итерация [0-9]+' CLAUDE.md 2>/dev/null | head -1 | grep -oE '[0-9]+')

printf 'arena · %s · iter→%s' "$branch" "${next:-?}"
