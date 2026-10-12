// Package env assembles the environment a container is launched with.
package env

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider"
	"github.com/duy0611/dev-cli/internal/secret"
)

// Git identity variables the devcontainer images conventionally read.
const (
	gitNameKey  = "DEVCONTAINER_GIT_NAME"
	gitEmailKey = "DEVCONTAINER_GIT_EMAIL"
)

// Assemble resolves a workspace's settings into environment variables. The
// host's git identity sits beneath them, so a setting of the same name wins.
func Assemble(ctx context.Context, r *secret.Resolver, settings []model.Setting) ([]provider.EnvVar, error) {
	defined := make(map[string]bool, len(settings))
	for _, s := range settings {
		defined[s.Key] = true
	}

	var out []provider.EnvVar
	for _, e := range gitIdentity(ctx) {
		if !defined[e.Key] {
			out = append(out, e)
		}
	}

	for _, s := range settings {
		v, err := r.Resolve(ctx, s.Spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Key, err)
		}
		out = append(out, provider.EnvVar{Key: s.Key, Value: v})
	}
	return out, nil
}

// gitIdentity reads the host's name and email. Signing config is left out: the
// container has no secret key, so every commit would fail.
func gitIdentity(ctx context.Context) []provider.EnvVar {
	var out []provider.EnvVar
	if v := gitConfig(ctx, "user.name"); v != "" {
		out = append(out, provider.EnvVar{Key: gitNameKey, Value: v})
	}
	if v := gitConfig(ctx, "user.email"); v != "" {
		out = append(out, provider.EnvVar{Key: gitEmailKey, Value: v})
	}
	return out
}

// gitConfig reads one git setting, returning "" when git is absent or the key
// is unset.
func gitConfig(ctx context.Context, key string) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	cmd.Stderr = &bytes.Buffer{}

	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
