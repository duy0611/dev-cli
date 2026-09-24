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

// agentConfigNone is what a container created with --no-agent-config stores,
// so that a later rebuild does not pick up a project file it opted out of.
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
	// Resolved now, so a rebuild run from another directory reads the same
	// file.
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
		// Re-derived every time rather than stored: the project can gain or
		// lose the file, and the next rebuild should see which.
		if c.SourceKind != model.SourceFolder {
			return "", nil
		}
		p := filepath.Join(c.Source, agentcfg.FileName)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
		return "", nil
	}
	// A file the operator named explicitly going missing is not "no file":
	// quietly applying nothing would leave a rebuilt container without what it
	// was created with.
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

// markAgentConfig validates the file c.AgentConfig leads to and records
// whether applying it is owed. Called before the row is written, so a typo in
// agents.yaml is exit 2 with nothing created.
func markAgentConfig(c *model.Container) error {
	spec, err := loadAgentConfig(*c)
	if err != nil {
		return err
	}
	c.AgentConfigPending = spec != nil
	return nil
}

// applyAgentConfig applies spec inside the running container. A nil spec only
// clears a pending flag that no longer has anything behind it.
//
// The flag is set before the first step and cleared after the last, so an
// apply that fails or is interrupted is retried by the next start.
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

	// A warning, not an error: the operator may set it after create, and the
	// agent resolves the reference each time it starts.
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
	// The same environment every other exec gets, so the steps see
	// CLAUDE_CONFIG_DIR and its siblings and write onto the state volume.
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
