// Package dcgen generates a devcontainer configuration for a folder that ships
// none, from a fixed catalog of tools.
//
// It renders JSON and nothing else: no database, no cobra, no filesystem. The
// installing is done by devcontainer Features, so this package never writes a
// shell script and never learns how a tool is packaged for a distribution.
package dcgen

import "sort"

// BaseImage is what a generated container is built from.
//
// Pinned to the Ubuntu release rather than the floating `:ubuntu` tag: that tag
// moves to the next LTS eventually, which would change the distribution under a
// container that had been working for months. The `1-` prefix still takes the
// image's own security rebuilds within that release.
const BaseImage = "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04"

// Feature references shared by more than one catalog entry, named because the
// render and the reverse map both have to spell them identically.
const (
	// kubeFeature installs kubectl, helm and minikube from one feature, each
	// gated by its own version option where "none" means "skip this one".
	kubeFeature = "ghcr.io/devcontainers/features/kubectl-helm-minikube:1"
	// aptFeature installs plain distribution packages, for the tools no
	// feature publishes at a reference that resolves.
	//
	// Only for what the base image lacks. jq is not here because common-utils,
	// which base:ubuntu already includes, installs it — offering a tool that is
	// present would be a menu entry that does nothing.
	aptFeature = "ghcr.io/devcontainers-extra/features/apt-get-packages:1"
	// npmFeature installs one global npm package. It needs a node runtime,
	// which is why every tool using it declares Requires. A features map holds
	// one entry per reference, so a second tool on it would overwrite the
	// first's package option — it serves one tool until that changes.
	npmFeature = "ghcr.io/devcontainers-extra/features/npm-package:1"
)

// Tool is one installable entry in the catalog.
type Tool struct {
	ID      string
	Summary string
	// Official marks a feature published by the devcontainers project or by
	// the vendor of the tool itself. Everything else is a community image the
	// operator is choosing to trust, and `dev container tools` says so.
	Official bool
	// Feature is the OCI reference. Empty when the tool is an apt package.
	Feature string
	Options map[string]any
	// Apt is the package name, set only when Feature is empty. These collect
	// into one apt-get-packages feature, so a dozen packages do not become a
	// dozen image layers.
	Apt string
	// Requires names catalog entries this one cannot work without. They are
	// added to the selection, because the alternative is an image build that
	// fails several minutes in with a message about a missing runtime.
	Requires []string
}

// catalog is the whole set. Adding a tool is a line here and nothing else.
//
// Every reference was checked against ghcr.io on 2026-09-17; an option name
// that does not exist is accepted silently by the devcontainer CLI and then
// does nothing, so these are copied from each feature's published metadata
// rather than guessed.
var catalog = []Tool{
	{ID: "aws", Summary: "AWS CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/aws-cli:1"},
	{ID: "claude-code", Summary: "Claude Code", Official: true,
		Feature: "ghcr.io/anthropics/devcontainer-features/claude-code:1"},
	// No official feature exists for Codex, so this is a community one; it takes
	// no options and pins nothing, installing whatever is current.
	{ID: "codex", Summary: "Codex CLI",
		Feature: "ghcr.io/jsburckhardt/devcontainer-features/codex:1"},
	{ID: "gcloud", Summary: "Google Cloud CLI",
		Feature: "ghcr.io/dhoeric/features/google-cloud-cli:1"},
	{ID: "gh", Summary: "GitHub CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/github-cli:1"},
	{ID: "go", Summary: "Go", Official: true,
		Feature: "ghcr.io/devcontainers/features/go:1"},
	{ID: "helm", Summary: "Helm", Official: true, Feature: kubeFeature},
	{ID: "hermes", Summary: "Hermes agent",
		Feature: "ghcr.io/devcontainer-community/devcontainer-features/hermes-agent.nousresearch.com:1"},
	{ID: "kubectl", Summary: "kubectl", Official: true, Feature: kubeFeature},
	{ID: "mise", Summary: "Mise",
		Feature: "ghcr.io/devcontainers-extra/features/mise:1"},
	{ID: "node", Summary: "Node.js", Official: true,
		Feature: "ghcr.io/devcontainers/features/node:2"},
	// @opencode/cli, not opencode-ai or the standalone feature: those install
	// 1.x, whose embedded Bun 1.3.14 crashes with "embedder failed to suspend
	// thread" when started by docker exec or kubectl exec — which is how every
	// agent is started (oven-sh/bun#31832). 2.x embeds a fixed Bun and is on
	// npm only; the feature downloads GitHub releases, which stop at 1.x.
	{ID: "opencode", Summary: "OpenCode agent", Feature: npmFeature,
		Options: map[string]any{"package": "@opencode/cli"}, Requires: []string{"node"}},
	{ID: "python", Summary: "Python", Official: true,
		Feature: "ghcr.io/devcontainers/features/python:1"},
	{ID: "yq", Summary: "yq", Apt: "yq"},
}

// Catalog returns every tool, sorted by id. Sorted because it is printed as a
// list and read by a human looking for a name.
func Catalog() []Tool {
	out := make([]Tool, len(catalog))
	copy(out, catalog)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup finds one tool by id.
func Lookup(id string) (Tool, bool) {
	for _, t := range catalog {
		if t.ID == id {
			return t, true
		}
	}
	return Tool{}, false
}
