#!/usr/bin/env bash
# SessionStart-хук: короткий контекст на старте сессии — ветка, последний коммит
# и следующая по плану итерация из CLAUDE.md. stdout добавляется в контекст.
set -uo pipefail

dir="${CLAUDE_PROJECT_DIR:-.}"
cd "$dir" 2>/dev/null || exit 0

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')
head=$(git log -1 --pretty='%h %s' 2>/dev/null)
next=$(grep -oE 'Следующая — итерация [0-9]+[^.]*' CLAUDE.md 2>/dev/null | head -1)

echo "arena · ветка ${branch} · ${head}"
[ -n "$next" ] && echo "Дальше по плану: ${next}."
