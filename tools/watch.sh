#!/bin/bash
set -euo pipefail

# WATCH_DB selects build tags for the backend (see Makefile TAGS). sqlite sets
# TAGS for CGO sqlite support. Other drivers use the default binary without
# sqlite tags; caller-supplied TAGS (pam, bindata, etc.) are preserved. When
# WATCH_DB is unset, TAGS is left unchanged.
# WATCH_ACT=true is reserved for starting act_runner alongside watch (not yet implemented).
if [[ -n "${WATCH_DB:-}" ]]; then
	case "${WATCH_DB}" in
	sqlite)
		export TAGS="sqlite sqlite_unlock_notify"
		;;
	mysql | pgsql | postgres | mssql)
		# Do not touch TAGS so pam/bindata/other local tags from the caller are kept.
		;;
	*)
		echo "watch.sh: unknown WATCH_DB=${WATCH_DB} (use sqlite, mysql, pgsql, postgres, or mssql)" >&2
		exit 1
		;;
	esac
fi

if [[ "${WATCH_ACT:-}" == "true" ]]; then
	echo "watch.sh: WATCH_ACT=true is not implemented yet (act_runner integration pending); ignoring." >&2
fi

make --no-print-directory watch-frontend &
make --no-print-directory watch-backend &

trap 'kill $(jobs -p)' EXIT
wait
