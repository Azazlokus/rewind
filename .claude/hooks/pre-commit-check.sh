#!/usr/bin/env bash
# PreToolUse-хук на Bash: перед 'git commit' прогоняет обязательный гейт
# `make check` и блокирует коммит, если он красный. Держит правило репозитория
# «ни один коммит с красным make check» железно, а не на доверии.
#
# Срабатывает только на командах с 'git commit'; прочие Bash-вызовы пропускает
# мгновенно. Выход 2 = блокировка tool-вызова, stderr уходит обратно модели.
set -uo pipefail

payload=$(cat)
cmd=$(printf '%s' "$payload" | jq -r '.tool_input.command // empty')

case "$cmd" in
*"git commit"*) ;;
*) exit 0 ;; # не коммит — не мешаем
esac

if out=$(make check 2>&1); then
	exit 0
fi

{
	echo "make check КРАСНЫЙ — коммит заблокирован (правило: ни один коммит с красным check)."
	echo "Почини гейт и повтори. Хвост вывода:"
	echo "-----"
	printf '%s\n' "$out" | tail -n 30
} >&2
exit 2
