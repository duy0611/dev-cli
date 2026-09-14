package k8s

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Defaults for the settings that have a sensible one.
const (
	// The host is Apple Silicon and cluster nodes are usually amd64. Building
	// for the host's architecture puts an arm64 image on an amd64 node, where
	// the pod crash-loops with "exec format error" — which reads like anything
	// but a build problem.
	defaultPlatform    = "linux/amd64"
	defaultStorageSize = "20Gi"
	defaultNamespace   = "default"
)

// Config is a k8s provider's settings, stored as JSON in providers.config.
type Config struct {
	// Context is the kubeconfig context to use. Empty means whatever is
	// current, which is deliberately allowed but worth being explicit about.
	Context string `json:"context,omitempty"`
	// Namespace must already exist; this tool does not create namespaces.
	Namespace string `json:"namespace"`
	// Registry is the prefix images are pushed to and pulled from, e.g.
	// europe-docker.pkg.dev/my-project/dev.
	Registry string `json:"registry"`
	// Platform is the architecture to build for.
	Platform string `json:"platform"`
	// StorageSize is the PVC size per container.
	StorageSize string `json:"storageSize"`
	// StorageClass is optional; empty means the cluster default.
	StorageClass string `json:"storageClass,omitempty"`
	// ServiceAccount is optional; empty means the namespace default.
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// ImagePullSecret is optional, for a registry the nodes cannot read
	// anonymously.
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

// Marshal renders the config for storage, defaults included so that a later
// change to a default cannot silently move an existing provider.
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

// validate catches the settings whose absence fails much later — a missing
// registry surfaces as a push to Docker Hub under a name that does not exist.
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
