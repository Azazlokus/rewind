#!/usr/bin/env bash
# PreToolUse-хук на Write|Edit: запрещает ручную правку golden-дампов.
# Легитимный путь обновления — только `go test ./internal/protocol -update`
# (это Bash, а не Edit), с последующим ревью дифа. Ручная правка ломает смысл
# golden как независимого эталона.
#
# Выход 2 = блокировка, причина уходит модели.
set -uo pipefail

payload=$(cat)
f=$(printf '%s' "$payload" | jq -r '.tool_input.file_path // empty')

case "$f" in
*.golden)
	echo "Ручная правка '$f' запрещена: golden обновляются только через 'go test ./internal/protocol -update' с ревью дифа." >&2
	exit 2
	;;
esac
exit 0
