package cli

import (
	"context"
	"os"
	"sync"
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
	key := c.WorkspaceName + "/" + c.Name
	a.idleMu.Lock()
	if a.idleSeen == nil {
		a.idleSeen = map[string]bool{}
	}
	seen := a.idleSeen[key]
	a.idleSeen[key] = true
	a.idleMu.Unlock()

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

// repairTimeout bounds the whole repair pass.
var repairTimeout = 10 * time.Second

// repairIdle restarts the watchdog in every running container this run did not
// already ensure: one started behind dev's back has none. Best-effort, and
// returns by the deadline whatever the engine is doing.
func (a *app) repairIdle(ctx context.Context) {
	if !a.isOpened || a.openErr != nil {
		return
	}
	type job struct {
		c  model.Container
		ws model.Workspace
		p  provider.Provider
	}
	// Every store read happens here, before the fan-out: a goroutine still
	// running at the deadline must not touch the store after the caller
	// closes it.
	containers, err := a.st.ListContainers("")
	if err != nil {
		return
	}
	workspaces := map[string]model.Workspace{}
	providers := map[string]provider.Provider{}
	var jobs []job
	for _, c := range containers {
		key := c.WorkspaceName + "/" + c.Name
		a.idleMu.Lock()
		seen := a.idleSeen[key]
		a.idleMu.Unlock()
		if !c.IdleStop || seen {
			continue
		}
		ws, ok := workspaces[c.WorkspaceName]
		if !ok {
			if ws, err = a.st.GetWorkspace(c.WorkspaceName); err != nil {
				continue
			}
			workspaces[c.WorkspaceName] = ws
		}
		p, ok := providers[ws.Name]
		if !ok {
			if p, err = a.providerFor(ws); err != nil {
				continue
			}
			providers[ws.Name] = p
		}
		if _, ok := p.(provider.IdleStopper); ok {
			jobs = append(jobs, job{c, ws, p})
		}
	}
	if len(jobs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, repairTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s, err := statusWithin(ctx, j.p, j.c); err != nil || s != model.StatusRunning {
				return
			}
			t, err := a.target(ctx, j.c, j.ws, j.p)
			if err != nil {
				return
			}
			defer t.release()
			a.ensureIdle(ctx, j.p, t.container)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
