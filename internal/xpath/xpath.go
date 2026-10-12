// Package xpath resolves host paths and validates the names that end up in
// container labels.
package xpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve turns path into an absolute, symlink-free directory path. The engine
// on macOS resolves path strings inside its VM, where /tmp is a real directory,
// so an unresolved bind mount silently mounts an empty directory.
func Resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", real)
	}
	return real, nil
}

// ValidateName checks a provider, workspace or container name: letters, digits,
// dot, underscore, hyphen.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("invalid name %q: use letters, digits, . _ -", name)
		}
	}
	return nil
}

// ValidateEnvKey checks a workspace setting key, which becomes an environment
// variable name in the container.
func ValidateEnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("empty setting key")
	}
	if key[0] >= '0' && key[0] <= '9' {
		return fmt.Errorf("invalid setting key %q: must not start with a digit", key)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_':
		default:
			return fmt.Errorf("invalid setting key %q: use letters, digits and _", key)
		}
	}
	return nil
}

// Shorten replaces a leading home directory with ~, for display only.
func Shorten(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// ResolveFile is Resolve for a regular file. A missing file wraps
// fs.ErrNotExist.
func ResolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a file: %s", real)
	}
	return real, nil
}
