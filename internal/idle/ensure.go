package idle

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/duy0611/dev-cli/internal/dcgen"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// State is what Ensure found or did.
type State string

const (
	Started     State = "started"
	Running     State = "running"
	Unavailable State = "unavailable" // not root and no passwordless sudo
)

// Stop is the last time the watchdog stopped the container.
type Stop struct {
	Outcome string // "stopped" or "reaped"
	At      time.Time
	Idle    time.Duration
}

// Options zero values mean the defaults.
type Options struct{ After, Tick time.Duration }

const fallbackDir = "/tmp/dev-idle"

// aliveFunc matches the whole command line rather than a substring: the probe
// and ensure scripts name watchdog.sh themselves, so a pidfile left stale by a
// restart that lands on one of their processes would read as a live watchdog.
const aliveFunc = `alive() {
	[ "$1" != "$$" ] || return 1
	case $(tr '\0' ' ' < "/proc/$1/cmdline" 2>/dev/null) in
	"sh $2/watchdog.sh run "*) return 0 ;;
	esac
	return 1
}
`

// ensureScript takes every value as an argument: sudo strips the environment.
// Aliveness is read from /proc because a root-owned watchdog refuses kill -0
// from a non-root caller.
const ensureScript = aliveFunc + `state=$1 after=$2 tick=$3 fallback=$4
if [ "$(id -u)" = 0 ]; then run=
elif sudo -n true 2>/dev/null; then run="sudo -n"
else cat >/dev/null; echo unavailable; exit 0; fi
if [ -d "$state" ]; then dir=$state/idle; else dir=$fallback; fi
$run mkdir -p "$dir" || exit 1
if $run test -e "$dir/marker"; then
	echo "marker $($run cat "$dir/marker")"
	$run rm -f "$dir/marker"
fi
pid=$($run cat "$dir/pid" 2>/dev/null)
case $pid in ''|*[!0-9]*) ;; *)
	if alive "$pid" "$dir"; then cat >/dev/null; echo running; exit 0; fi ;;
esac
$run sh -c 'cat > "$1"' _ "$dir/watchdog.sh" || exit 1
if command -v setsid >/dev/null 2>&1; then
	$run setsid sh "$dir/watchdog.sh" run "$dir" "$after" "$tick" </dev/null >/dev/null 2>&1 &
else
	$run nohup sh "$dir/watchdog.sh" run "$dir" "$after" "$tick" </dev/null >/dev/null 2>&1 &
fi
echo started`

// Ensure copies the watchdog into the container and starts it unless one is
// already running, and returns the stop it recorded since the last call.
func Ensure(ctx context.Context, p provider.Provider, c model.Container, o Options) (State, *Stop, error) {
	return ensure(ctx, p, c, o, dcgen.StateDir, fallbackDir)
}

func ensure(ctx context.Context, p provider.Provider, c model.Container, o Options, stateDir, fallback string) (State, *Stop, error) {
	if o.After <= 0 {
		o.After = DefaultAfter
	}
	if o.Tick <= 0 {
		o.Tick = DefaultTick
	}
	var out, errb bytes.Buffer
	cmd := []string{"sh", "-c", ensureScript, "dev-idle", stateDir,
		strconv.Itoa(seconds(o.After)), strconv.Itoa(seconds(o.Tick)), fallback}
	err := p.Exec(ctx, c, cmd, provider.ExecOpts{Stdin: bytes.NewReader(Script), Stdout: &out, Stderr: &errb})
	if err != nil {
		return "", nil, fmt.Errorf("idle watchdog in %s: %w: %s", c.Name, err, strings.TrimSpace(errb.String()))
	}
	var stop *Stop
	var state State
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 4 && f[0] == "marker":
			at, e1 := strconv.ParseInt(f[2], 10, 64)
			idle, e2 := strconv.ParseInt(f[3], 10, 64)
			if e1 != nil || e2 != nil {
				return "", nil, badOutput(c, out.String())
			}
			stop = &Stop{Outcome: f[1], At: time.Unix(at, 0), Idle: time.Duration(idle) * time.Second}
		case len(f) == 1 && state == "" && (f[0] == string(Started) || f[0] == string(Running) || f[0] == string(Unavailable)):
			state = State(f[0])
		default:
			return "", nil, badOutput(c, out.String())
		}
	}
	if state == "" {
		return "", nil, badOutput(c, out.String())
	}
	return state, stop, nil
}

func badOutput(c model.Container, out string) error {
	return fmt.Errorf("idle watchdog in %s: unexpected output %q", c.Name, out)
}

// seconds is at least 1: the watchdog's sleep 0 would spin and an AFTER of 0 stops at once.
func seconds(d time.Duration) int {
	return max(1, int(d.Seconds()))
}

// probeScript is read-only: it never writes, so it needs no sudo and cannot
// race the watchdog.
const probeScript = aliveFunc + `state=$1 fallback=$2
if [ -d "$state" ]; then dir=$state/idle; else dir=$fallback; fi
[ -e "$dir/marker" ] && { echo ensure; exit 0; }
pid=$(cat "$dir/pid" 2>/dev/null)
case $pid in ''|*[!0-9]*) echo ensure; exit 0 ;; esac
if alive "$pid" "$dir"; then echo ok; else echo ensure; fi`

// Healthy reports whether c has a live watchdog and no stop to report, without
// the devcontainer CLI. When it is false, Ensure is what to call.
func Healthy(ctx context.Context, p provider.IdleStopper, c model.Container) (bool, error) {
	return healthy(ctx, p, c, dcgen.StateDir, fallbackDir)
}

func healthy(ctx context.Context, p provider.IdleStopper, c model.Container, stateDir, fallback string) (bool, error) {
	var out bytes.Buffer
	if err := p.ProbeIdle(ctx, c, []string{"sh", "-c", probeScript, "dev-idle-probe", stateDir, fallback}, &out); err != nil {
		return false, fmt.Errorf("idle watchdog probe in %s: %w", c.Name, err)
	}
	switch strings.TrimSpace(out.String()) {
	case "ok":
		return true, nil
	case "ensure":
		return false, nil
	}
	return false, badOutput(c, out.String())
}
