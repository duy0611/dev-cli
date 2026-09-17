// Package model holds the persisted domain types. They are plain data: every
// behaviour that needs them lives in the package that acts on them.
package model

import "time"

// ProviderKind says which engine a provider drives.
type ProviderKind string

const (
	// KindLocal runs devcontainers through the devcontainer CLI against a
	// Docker-compatible engine.
	KindLocal ProviderKind = "local"
	// KindK8s will run them in a Kubernetes cluster. Not implemented yet; the
	// constant exists so the stored value has one spelling from the start.
	KindK8s ProviderKind = "k8s"
)

// Provider is a configured place for devcontainers to run.
type Provider struct {
	Name      string
	Kind      ProviderKind
	Config    string // provider-specific JSON, "{}" when there is nothing to say
	CreatedAt time.Time
}

// Workspace groups containers and carries the settings they are launched with.
type Workspace struct {
	Name         string
	ProviderName string
	SSHForward   bool
	GPGForward   bool
	CreatedAt    time.Time
}

// Setting is one environment variable a workspace contributes to its
// containers. Spec is a value specification, not a value: see internal/secret.
// The resolved value is never stored.
type Setting struct {
	Key  string
	Spec string
}

// SourceKind says what a container was created from.
type SourceKind string

const (
	// SourceFolder is a directory on the host that ships its own
	// .devcontainer configuration.
	SourceFolder SourceKind = "folder"
)

// Container is a devcontainer this tool knows about. It records what the
// container was made from, never whether it is currently running: live status
// is read from the engine, because a stored copy goes stale the moment anything
// happens outside dev.
type Container struct {
	Name          string
	WorkspaceName string
	SourceKind    SourceKind
	Source        string // absolute physical host path when SourceKind is folder
	ConfigPath    string // the resolved .devcontainer config file
	// GeneratedConfig is the devcontainer.json dev wrote for this container,
	// empty when the project ships its own. It lives here rather than on disk
	// so that deleting the workspace takes it too, and so a cloud provider
	// inherits it with the row. The devcontainer CLI only accepts a path, so
	// it is materialised to a temporary file per invocation.
	GeneratedConfig string
	CreatedAt       time.Time
}

// Status is a container's liveness as the engine reports it.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusAbsent means no container exists for this record yet, which is the
	// normal state between `container create --no-start` and the first start.
	StatusAbsent Status = "absent"
)
