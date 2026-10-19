#!/bin/sh
set -u

PROC=${DEV_IDLE_PROC:-/proc}
KILL=${DEV_IDLE_KILL:-kill}
GRACE=${DEV_IDLE_GRACE:-10}

sessions() {
	n=0
	for s in "$PROC"/[0-9]*/stat; do
		read -r line < "$s" 2>/dev/null || continue
		pid=${line%% *}
		set -- ${line##*) }
		[ "$1" = Z ] && continue
		[ "$2" = 0 ] && [ "$pid" != 1 ] && n=$((n + 1))
	done
	echo "$n"
}

mark() {
	echo "$1 $(date +%s) $idle" > "$DIR/marker"
}

case "${1:-}" in
count)
	sessions
	;;
run)
	DIR=$2 AFTER=$3 TICK=$4
	echo "$$" > "$DIR/pid"
	idle=0
	while :; do
		sleep "$TICK"
		if [ "$(sessions)" -gt 0 ]; then
			idle=0
		else
			idle=$((idle + TICK))
		fi
		[ "$idle" -ge "$AFTER" ] && break
	done
	mark stopped
	$KILL -TERM 1
	sleep "$GRACE"
	if [ -e "$PROC/1" ]; then
		# PID 1 in its own namespace ignores every signal it has no handler for, KILL included.
		mark reaped
		$KILL -KILL -1
	fi
	exit 0
	;;
*)
	echo "usage: watchdog.sh count | run DIR AFTER_SECONDS TICK_SECONDS" >&2
	exit 2
	;;
esac
