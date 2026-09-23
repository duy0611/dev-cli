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
	// SSHForward carries the host's SSH agent into this workspace's
	// containers, for the length of one dev command. See internal/relay.
	SSHForward bool
	CreatedAt  time.Time
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
	// SourceNone is a container with no host folder at all. Its work lives in
	// a volume the provider owns — a docker named volume on local, the PVC on
	// k8s — because there is no host directory to bind-mount.
	SourceNone SourceKind = "none"
)

// Container is a devcontainer this tool knows about. It records what the
// container was made from, never whether it is currently running: live status
// is read from the engine, because a stored copy goes stale the moment anything
// happens outside dev.
type Container struct {
	Name          string
	WorkspaceName string
	SourceKind    SourceKind
	Source        string // absolute physical host path when SourceKind is folder, empty when none
	ConfigPath    string // the resolved .devcontainer config file
	// GeneratedConfig is the devcontainer.json dev wrote for this container,
	// empty when the project ships its own. It lives here rather than on disk
	// so that deleting the workspace takes it too, and so a cloud provider
	// inherits it with the row. The devcontainer CLI only accepts a path, so
	// it is materialised to a temporary file per invocation.
	GeneratedConfig string
	// PersistState says whether this container keeps its agents' configuration
	// on a volume of its own, so that plugins, marketplaces and MCP definitions
	// outlive a rebuild. Fixed at create: a rebuild must not be able to change
	// what a container is mounted on.
	//
	// Credentials are not part of it. Agents authenticate from environment
	// variables resolved per invocation, so nothing worth stealing rests here.
	PersistState bool
	// OverrideConfigPath is a merged devcontainer.json built for the length of
	// one invocation: the project's own document with dev's state mount in it.
	//
	// Not persisted, and so needing no migration. A stored merge would freeze a
	// snapshot of a file dev does not own — add a feature to the project's
	// devcontainer.json and a rebuild would silently use the document as it
	// stood at create. That is invariant 4's reasoning applied to a file on the
	// other side of the fence. Set by materialise, read by the local provider.
	OverrideConfigPath string
	// WorktreeRepo is git's common directory for a worktree-backed container,
	// empty otherwise. Not persisted: it is looked up from the worktrees table
	// and set on this value per invocation, by whatever resolves the container,
	// so that materialise can merge the two bind mounts invariant 11 requires
	// into a project's own devcontainer.json the same way it merges the state
	// mount. A generated document needs no such field — its two mounts are
	// baked in at render time from the stored row instead.
	WorktreeRepo string
	CreatedAt    time.Time
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

// Worktree is a git worktree checkout that a container was created on.
//
// One per container at most, and it is deleted with the container: the pair is
// created together by `dev worktree create` and the whole point of recording
// the link is that neither can be left behind without the other.
type Worktree struct {
	WorkspaceName string
	ContainerName string
	// Repo is git's common directory — the directory holding the object store
	// and the worktree administration. Not the repository root: the two differ
	// for a bare repository, and this is the path the checkout's .git file
	// points into, so it is what gets bind-mounted into the container.
	Repo   string
	Branch string
	// Path is the checkout, resolved. Equal to the container's Source today,
	// and kept separately because the two answer different questions.
	Path string
	// HerdrWorkspace is the id Herdr gave this checkout, empty when Herdr was
	// not running or declined. Only used to close that workspace on removal.
	HerdrWorkspace string
	CreatedAt      time.Time
}
