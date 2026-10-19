// Package model holds the persisted domain types, as plain data.
package model

import "time"

// ProviderKind says which engine a provider drives.
type ProviderKind string

const (
	// KindLocal runs devcontainers through the devcontainer CLI against a
	// Docker-compatible engine.
	KindLocal ProviderKind = "local"
	// KindK8s runs them in a Kubernetes cluster.
	KindK8s ProviderKind = "k8s"
)

// Provider is a configured place for devcontainers to run.
type Provider struct {
	Name      string
	Kind      ProviderKind
	Config    string // provider-specific JSON, "{}" when empty
	CreatedAt time.Time
}

// Workspace groups containers and carries the settings they are launched with.
type Workspace struct {
	Name         string
	ProviderName string
	// SSHForward forwards the host's SSH agent into the containers for the
	// length of one dev command.
	SSHForward bool
	CreatedAt  time.Time
}

// Setting is one environment variable a workspace contributes to its
// containers. Spec is a value specification such as keychain:NAME, never the
// resolved value.
type Setting struct {
	Key  string
	Spec string
}

// SourceKind says what a container was created from.
type SourceKind string

const (
	// SourceFolder is a directory on the host.
	SourceFolder SourceKind = "folder"
	// SourceNone is a container with no host folder; its work lives in a volume
	// the provider owns.
	SourceNone SourceKind = "none"
)

// Mode is how much of the operator's checkout a container can reach. Fixed at
// create.
type Mode string

const (
	// ModeGuarded mounts the folder live with its .git read-only.
	ModeGuarded Mode = "guarded"
	// ModeIsolated copies the folder into a volume and never mounts it.
	ModeIsolated Mode = "isolated"
	// ModeLegacy is a container created before modes existed.
	ModeLegacy Mode = "legacy"
	// ModeNone is a folderless container.
	ModeNone Mode = "none"
)

// Container is a devcontainer this tool knows about. It records what the
// container was made from, never whether it is running: live status comes from
// the engine.
type Container struct {
	Name          string
	WorkspaceName string
	SourceKind    SourceKind
	Source        string // absolute physical host path when SourceKind is folder, empty when none
	ConfigPath    string // the resolved .devcontainer config file
	// GeneratedConfig is the devcontainer.json dev wrote for this container,
	// empty when the project ships its own.
	GeneratedConfig string
	// PersistState says whether agent configuration lives on a volume of its
	// own. Fixed at create.
	PersistState bool
	// AgentConfig says which agents.yaml this container applies: "" for the
	// project's own, "none" for --no-agent-config, otherwise a physical path.
	AgentConfig string
	// AgentConfigPending says applying AgentConfig is still owed.
	AgentConfigPending bool
	// Mode is how much of the checkout this container reaches. Fixed at create.
	Mode Mode
	// GitGuard says host git is shielded from writes under .git. Meaningful for
	// legacy containers only.
	GitGuard bool
	// AllowPrivileged says the operator allowed host access (privileged mode,
	// the engine socket, host namespaces, host mounts). Set by create's flag
	// only, never by a project file an agent can edit.
	AllowPrivileged bool
	// IdleStop says a watchdog stops the container once nobody is connected.
	// Fixed at create.
	IdleStop bool
	// ConfigDigest is the digest of the project configuration last built from,
	// so a rebuild can refuse one that changed unseen. Empty for generated
	// configurations.
	ConfigDigest string
	// ConfigDigestFields holds a hash per field of that configuration, so a
	// changed digest can name the fields that changed.
	ConfigDigestFields string
	// OverrideConfigPath is the merged devcontainer.json for one invocation. It
	// is not persisted: a stored merge would freeze a snapshot of a file dev
	// does not own.
	OverrideConfigPath string
	// WorktreeRepo is git's common directory when the folder is a git worktree,
	// empty otherwise.
	WorktreeRepo string
	// GitGuardMounts are the mount entries that shield host git, derived per
	// invocation and not persisted.
	GitGuardMounts []string
	// Folders are the host folders beyond Source, sorted by host path.
	Folders []Folder
	// WorkspacePath is where an isolated container's primary volume is
	// mounted, empty for every other mode.
	WorkspacePath string
	CreatedAt     time.Time
}

// Folder is a host folder a container has beyond its primary, and where it
// appears inside the container.
type Folder struct {
	HostPath      string
	ContainerPath string
	// WorktreeRepo is git's common directory when a guarded folder is a
	// worktree checkout, empty otherwise.
	WorktreeRepo string
}

// Status is a container's liveness as the engine reports it.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusAbsent means no container exists yet for this record.
	StatusAbsent Status = "absent"
)

// Worktree is a git worktree checkout made by `dev worktree create`. It belongs
// to a workspace, not to any container, and is removed only by `dev worktree
// remove`.
type Worktree struct {
	WorkspaceName string
	Name          string
	// Repo is git's common directory, not the repository root: the two differ
	// for a bare repository, and this is the path the checkout's .git file
	// points into.
	Repo   string
	Branch string
	// Path is the checkout, resolved.
	Path string
	// HerdrWorkspace is the id of this checkout's Herdr workspace, empty when
	// Herdr was not running.
	HerdrWorkspace string
	CreatedAt      time.Time
}

// DiffReview is a container's review state: the draft being written and the
// last round sent.
type DiffReview struct {
	WorkspaceName, ContainerName string
	Draft                        string    // JSON, review.EncodeComments; "" when none
	Sent                         string    // JSON of the last round sent; "" before the first
	Snapshot                     string    // review.Snapshot of the diff at the last send
	SentAt                       time.Time // zero before the first send
}
