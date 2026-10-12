package k8s

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Defaults for the settings that have a sensible one.
const (
	// Cluster nodes are usually amd64, and an arm64 image there crash-loops
	// with "exec format error", which reads like anything but a build problem.
	defaultPlatform    = "linux/amd64"
	defaultStorageSize = "1Gi"
	defaultNamespace   = "default"
)

// Config is a k8s provider's settings, stored as JSON in providers.config.
type Config struct {
	// Empty means the kubeconfig's current context.
	Context string `json:"context,omitempty"`
	// Must already exist.
	Namespace string `json:"namespace"`
	// Registry is the image prefix, e.g. europe-docker.pkg.dev/my-project/dev.
	Registry string `json:"registry"`
	// Platform is the architecture to build for.
	Platform string `json:"platform"`
	// StorageSize is the PVC size per container.
	StorageSize string `json:"storageSize"`
	// StorageClass is optional; empty means the cluster default.
	StorageClass string `json:"storageClass,omitempty"`
	// ServiceAccount is optional; empty means the namespace default.
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// For a registry the nodes cannot pull from anonymously.
	ImagePullSecret string `json:"imagePullSecret,omitempty"`
}

// ParseConfig reads a provider record's config JSON, filling in defaults.
func ParseConfig(raw string) (Config, error) {
	c := Config{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Config{}, fmt.Errorf("reading the provider config: %w", err)
		}
	}
	c.applyDefaults()
	return c, c.validate()
}

// Marshal renders the config for storage. Defaults are stored too, so a later
// change to one cannot move an existing provider.
func (c Config) Marshal() (string, error) {
	c.applyDefaults()
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("writing the provider config: %w", err)
	}
	return string(b), nil
}

func (c *Config) applyDefaults() {
	if c.Namespace == "" {
		c.Namespace = defaultNamespace
	}
	if c.Platform == "" {
		c.Platform = defaultPlatform
	}
	if c.StorageSize == "" {
		c.StorageSize = defaultStorageSize
	}
}

// validate rejects settings whose absence only fails later, e.g. a push to
// Docker Hub with no registry.
func (c Config) validate() error {
	if c.Registry == "" {
		return fmt.Errorf("the k8s provider needs a registry (--registry)")
	}
	if strings.Contains(c.Registry, " ") {
		return fmt.Errorf("registry %q contains a space", c.Registry)
	}
	if !strings.Contains(c.Platform, "/") {
		return fmt.Errorf("platform %q should look like linux/amd64", c.Platform)
	}
	return nil
}
