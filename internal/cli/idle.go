package cli

import (
	"context"
	"os"
	"time"

	"github.com/duy0611/dev-cli/internal/idle"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// ensureIdle starts the idle watchdog in c, and reports the stop it recorded
// since the last call. An error only warns: idle stop never fails a command.
func (a *app) ensureIdle(ctx context.Context, p provider.Provider, c model.Container) {
	if _, ok := p.(provider.IdleStopper); !ok || !c.IdleStop {
		return
	}
	if a.idleSeen == nil {
		a.idleSeen = map[string]bool{}
	}
	key := c.WorkspaceName + "/" + c.Name
	seen := a.idleSeen[key]
	a.idleSeen[key] = true

	state, stop, err := idle.Ensure(ctx, p, c, idleOptions())
	if err != nil {
		warnf(a, "idle stop for %s: %v", c.Name, err)
		return
	}
	// A rebuild in the same run calls again; the warning is once per run.
	if state == idle.Unavailable && !seen {
		warnf(a, "idle stop is unavailable in %s: its user is not root and has no passwordless sudo", c.Name)
	}
	if stop == nil {
		return
	}
	minutes := int(stop.Idle.Minutes())
	a.record("idle-stop", c.WorkspaceName, c.Name, map[string]any{
		"outcome":      stop.Outcome,
		"stopped_at":   stop.At.UTC().Format(time.RFC3339),
		"idle_minutes": minutes,
	})
	what := "was stopped"
	if stop.Outcome == "reaped" {
		what = "had its processes stopped"
	}
	a.printf("container %s %s at %s after %d idle minutes\n", c.Name, what, stop.At.Local().Format("15:04"), minutes)
}

// idleOptions reads the smoke test's overrides. A value that does not parse is
// ignored.
func idleOptions() idle.Options {
	var o idle.Options
	if d, err := time.ParseDuration(os.Getenv("DEV_IDLE_AFTER")); err == nil {
		o.After = d
	}
	if d, err := time.ParseDuration(os.Getenv("DEV_IDLE_TICK")); err == nil {
		o.Tick = d
	}
	return o
}
