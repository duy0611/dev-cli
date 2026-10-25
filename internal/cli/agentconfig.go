package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/agent"
	"github.com/duy0611/dev-cli/internal/agentcfg"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/xpath"
)

// agentConfigNone is stored by --no-agent-config, so a rebuild does not pick up
// the project's file.
const agentConfigNone = "none"

// agentConfigChoice turns create's two flags into what the row stores.
func agentConfigChoice(path string, none bool) (string, error) {
	switch {
	case path != "" && none:
		return "", usageErrorf("--agent-config and --no-agent-config contradict each other")
	case none:
		return agentConfigNone, nil
	case path == "":
		return "", nil
	}
	// Resolved now, so a rebuild from another directory reads the same file.
	resolved, err := xpath.ResolveFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", notFoundErrorf("agent config %s does not exist", path)
	}
	if err != nil {
		return "", usageError(err)
	}
	return resolved, nil
}

// agentConfigFile returns the agents.yaml c applies, or "" when it has none.
func agentConfigFile(c model.Container) (string, error) {
	switch c.AgentConfig {
	case agentConfigNone:
		return "", nil
	case "":
		// Re-derived each time, not stored: the project may gain or lose the
		// file.
		if c.SourceKind != model.SourceFolder || c.Source == "" {
			return "", nil
		}
		p := filepath.Join(c.Source, agentcfg.FileName)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
		return "", nil
	}
	// A missing named file is an error, not "no file", or a rebuild would
	// silently drop what the container was created with.
	if _, err := os.Stat(c.AgentConfig); errors.Is(err, fs.ErrNotExist) {
		return "", notFoundErrorf("agent config %s no longer exists; restore it, or remove the container and create it again",
			c.AgentConfig)
	} else if err != nil {
		return "", err
	}
	return c.AgentConfig, nil
}

// loadAgentConfig reads and validates c's agents.yaml; nil when it has none.
func loadAgentConfig(c model.Container) (*agentcfg.Spec, error) {
	file, err := agentConfigFile(c)
	if err != nil || file == "" {
		return nil, err
	}
	spec, err := agentcfg.Load(file)
	if err != nil {
		return nil, usageError(err)
	}
	return spec, nil
}

// markAgentConfig validates c's agents.yaml and records whether an apply is
// owed. Runs before the row is written, so a typo fails with nothing created.
func markAgentConfig(c *model.Container) error {
	spec, err := loadAgentConfig(*c)
	if err != nil {
		return err
	}
	c.AgentConfigPending = spec != nil
	return nil
}

// applyAgentConfig applies spec inside the running container; a nil spec only
// clears a stale pending flag. The flag stays set until the last step, so an
// interrupted apply is retried by the next start.
func (a *app) applyAgentConfig(ctx context.Context, p provider.Provider, c model.Container,
	environ []provider.EnvVar, spec *agentcfg.Spec) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	if spec == nil {
		if c.AgentConfigPending {
			return st.SetAgentConfigPending(c.WorkspaceName, c.Name, false)
		}
		return nil
	}

	// Warn, not fail: the variable may be set after create.
	defined := make(map[string]bool, len(environ))
	for _, e := range environ {
		defined[e.Key] = true
	}
	for _, name := range spec.Refs() {
		if !defined[name] {
			warnf(a, "agents.yaml references ${%s}, which no workspace setting defines; "+
				"set it with: dev workspace set %s SPEC", name, name)
		}
	}

	if err := st.SetAgentConfigPending(c.WorkspaceName, c.Name, true); err != nil {
		return err
	}
	// The same environment as every other exec, so the steps write to the state
	// volume.
	run := func(cmd []string, stdin []byte) ([]byte, error) {
		var stdout, stderr bytes.Buffer
		opts := provider.ExecOpts{Env: environ, Stdout: &stdout, Stderr: &stderr}
		if stdin != nil {
			opts.Stdin = bytes.NewReader(stdin)
		}
		if err := p.Exec(ctx, c, cmd, opts); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("%w: %s", err, msg)
			}
			return nil, err
		}
		return stdout.Bytes(), nil
	}
	if err := agent.Apply(spec, run,
		func(desc string) { a.printf("agents.yaml: %s\n", desc) },
		func(msg string) { warnf(a, "%s", msg) },
	); err != nil {
		return fmt.Errorf("applying agents.yaml to %s: %w (the container is running; "+
			"the next dev container start retries)", c.Name, err)
	}
	return st.SetAgentConfigPending(c.WorkspaceName, c.Name, false)
}
