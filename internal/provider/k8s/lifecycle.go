package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
)

// markerFile records that the create-time lifecycle commands have run.
//
// A file on the volume rather than an annotation on the Deployment, because the
// Deployment is re-applied on every start and a server-side apply drops fields
// it no longer sets — the marker would clear itself and postCreate would run
// again on each start. The volume is also the honest place for it: what the
// question really asks is whether these commands have run against this data.
const markerFile = "/.dev-lifecycle-done"

// parseCommand decodes one lifecycle entry into the commands it names.
//
// The spec allows three shapes and all three occur:
//
//	"npm install"                       a shell command line
//	["npm", "install"]                  an argv, run without a shell
//	{"deps": "npm i", "build": "make"}  named commands, run in parallel
//
// The named form is run sequentially here. Slower than the spec allows, never
// wrong, and it keeps the output of a failing command next to its name.
func parseCommand(raw json.RawMessage) ([][]string, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if asString == "" {
			return nil, nil
		}
		return [][]string{{"sh", "-c", asString}}, nil
	}

	var asArray []string
	if err := json.Unmarshal(raw, &asArray); err == nil {
		if len(asArray) == 0 {
			return nil, nil
		}
		return [][]string{asArray}, nil
	}

	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asObject); err == nil {
		names := make([]string, 0, len(asObject))
		for name := range asObject {
			names = append(names, name)
		}
		// Sorted, so a failure is reproducible rather than depending on map
		// iteration order.
		sort.Strings(names)

		var out [][]string
		for _, name := range names {
			cmds, err := parseCommand(asObject[name])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			out = append(out, cmds...)
		}
		return out, nil
	}

	return nil, fmt.Errorf("unrecognised lifecycle command: %s", string(raw))
}

// parseAll flattens a merged phase — an array of entries, one per contributor,
// since Features add commands of their own.
func parseAll(entries []json.RawMessage) ([][]string, error) {
	var out [][]string
	for _, e := range entries {
		cmds, err := parseCommand(e)
		if err != nil {
			return nil, err
		}
		out = append(out, cmds...)
	}
	return out, nil
}

// runPhase runs every command of one lifecycle phase, in order, stopping at the
// first failure.
//
// Stopping matters: postCreate is usually "install the dependencies", and
// carrying on into a container that is missing them produces a confusing
// failure much later.
func (p *Provider) runPhase(ctx context.Context, c model.Container, dev DevConfig,
	env []provider.EnvVar, phase string, entries []json.RawMessage) error {

	cmds, err := parseAll(entries)
	if err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	for _, cmd := range cmds {
		fmt.Fprintf(os.Stderr, "dev: %s: %s\n", phase, shellJoin(cmd))
		err := p.Exec(ctx, c, cmd, provider.ExecOpts{
			Env: env,
			// Straight through: a failing setup command explains itself in its
			// own output, and hiding that leaves only an exit status.
			Stdout: os.Stderr,
			Stderr: os.Stderr,
		})
		if err != nil {
			return fmt.Errorf("%s failed: %s: %w", phase, shellJoin(cmd), err)
		}
	}
	return nil
}

// lifecycleDone reports whether the create-time commands have already run
// against this volume.
func (p *Provider) lifecycleDone(ctx context.Context, c model.Container, dev DevConfig) bool {
	err := p.Exec(ctx, c, []string{"test", "-f", dev.HomeDir() + markerFile},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
	return err == nil
}

func (p *Provider) markLifecycleDone(ctx context.Context, c model.Container, dev DevConfig) error {
	return p.Exec(ctx, c, []string{"touch", dev.HomeDir() + markerFile},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
}

// clearLifecycleMarker makes the create-time commands run again, which is what
// a rebuild means: a new image, so whatever they installed is gone.
func (p *Provider) clearLifecycleMarker(ctx context.Context, c model.Container, dev DevConfig) error {
	return p.Exec(ctx, c, []string{"rm", "-f", dev.HomeDir() + markerFile},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard})
}

// runLifecycle runs the phases appropriate to what just happened.
//
// onCreate, updateContent and postCreate run once per image; postStart runs
// every time the container comes up, which is what the spec says and what a
// stop and start has to honour. postAttach is not run: nothing here attaches.
func (p *Provider) runLifecycle(ctx context.Context, c model.Container, dev DevConfig,
	lc Lifecycle, env []provider.EnvVar, rebuilt bool) error {

	if rebuilt {
		if err := p.clearLifecycleMarker(ctx, c, dev); err != nil {
			return err
		}
	}

	if !p.lifecycleDone(ctx, c, dev) {
		for _, phase := range []struct {
			name    string
			entries []json.RawMessage
		}{
			{"onCreateCommand", lc.OnCreate},
			{"updateContentCommand", lc.UpdateContent},
			{"postCreateCommand", lc.PostCreate},
		} {
			if err := p.runPhase(ctx, c, dev, env, phase.name, phase.entries); err != nil {
				return err
			}
		}
		// Only after all three succeed. A marker written over a failed install
		// would make the next start skip the fix.
		if err := p.markLifecycleDone(ctx, c, dev); err != nil {
			return err
		}
	}

	return p.runPhase(ctx, c, dev, env, "postStartCommand", lc.PostStart)
}
