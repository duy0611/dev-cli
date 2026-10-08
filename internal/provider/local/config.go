package local

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
)

// MergedConfig asks the devcontainer CLI for the configuration it would run.
//
// read-configuration with --include-merged-configuration, which combines the
// project's document with every feature's metadata — a feature's privileged
// and mounts arrive only through this, since no file in the project mentions
// them. The override dev builds is passed too, because it is part of what runs.
//
// No --id-label and no container: the CLI otherwise looks for an existing
// container and reads the merged form off its image metadata, which for a
// rebuild is the configuration being replaced rather than the one about to
// run. Without a container it resolves the features afresh, from the document.
//
// The CLI shells out to docker even to read a file, and resolving features may
// pull their metadata from a registry; both are why this is a provider method
// and why a unit test stubs the binary rather than calling it.
func (p *Provider) MergedConfig(ctx context.Context, c model.Container) ([]byte, []string, error) {
	if err := requireBinary(devcontainerBin); err != nil {
		return nil, nil, err
	}
	args := []string{"read-configuration",
		"--workspace-folder", c.Source,
		"--include-merged-configuration",
		"--log-format", "json",
	}
	if c.ConfigPath != "" {
		args = append(args, "--config", c.ConfigPath)
	}
	if c.OverrideConfigPath != "" {
		args = append(args, "--override-config", c.OverrideConfigPath)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, devcontainerBin, args...)
	cmd.Stdout = &stdout
	// Progress and errors go to stderr; only a failure makes them worth
	// showing, and then only the last line says why.
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("reading the configuration of container %s: %w: %s",
			c.Name, err, lastLine(stderr.String()))
	}

	var out struct {
		Merged json.RawMessage `json:"mergedConfiguration"`
	}
	if err := json.Unmarshal(lastJSONLine(stdout.Bytes()), &out); err != nil {
		return nil, nil, fmt.Errorf("reading the configuration of container %s: %w", c.Name, err)
	}
	if len(out.Merged) == 0 || string(out.Merged) == "null" {
		return nil, nil, fmt.Errorf("reading the configuration of container %s: the devcontainer CLI reported no merged configuration", c.Name)
	}
	compose, err := composeFiles(out.Merged, c.ConfigPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the configuration of container %s: %w", c.Name, err)
	}
	return out.Merged, compose, nil
}

// composeFiles resolves dockerComposeFile — a string or a list, each relative
// to the configuration's own directory, which is how the CLI resolves them.
func composeFiles(merged json.RawMessage, configPath string) ([]string, error) {
	var doc struct {
		DockerComposeFile json.RawMessage `json:"dockerComposeFile"`
	}
	if err := json.Unmarshal(merged, &doc); err != nil {
		return nil, err
	}
	if len(doc.DockerComposeFile) == 0 {
		return nil, nil
	}
	var files []string
	var one string
	if err := json.Unmarshal(doc.DockerComposeFile, &one); err == nil {
		files = []string{one}
	} else if err := json.Unmarshal(doc.DockerComposeFile, &files); err != nil {
		return nil, fmt.Errorf("dockerComposeFile is neither a string nor a list: %s", doc.DockerComposeFile)
	}
	dir := filepath.Dir(configPath)
	for i, f := range files {
		if !filepath.IsAbs(f) {
			files[i] = filepath.Join(dir, f)
		}
	}
	return files, nil
}

// lastJSONLine is the result line. The CLI's stdout should hold only the JSON
// result, but a stray line ahead of it from a feature or a wrapper must not
// turn into a parse error that reads as a broken configuration.
func lastJSONLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 && l[0] == '{' {
			return l
		}
	}
	return b
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}
