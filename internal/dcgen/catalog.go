// Package dcgen generates a devcontainer configuration for a folder that ships
// none, from a fixed catalog of tools. It renders JSON only. Installation is
// done by devcontainer Features, so the package never writes a shell script.
package dcgen

import "sort"

// BaseImage is what a generated container is built from. It is pinned to the
// Ubuntu release rather than the floating tag, which would move the
// distribution under a working container. The `1-` prefix still takes that
// release's security rebuilds.
const BaseImage = "mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04"

// Feature references shared by more than one catalog entry.
const (
	// kubeFeature installs kubectl, helm and minikube, each gated by its own
	// version option.
	kubeFeature = "ghcr.io/devcontainers/features/kubectl-helm-minikube:1"
	// aptFeature installs plain distribution packages, for tools no feature
	// publishes.
	aptFeature = "ghcr.io/devcontainers-extra/features/apt-get-packages:1"
	// npmFeature installs one global npm package and needs a node runtime. A
	// features map holds one entry per reference, so only one tool using it can
	// set a package option.
	npmFeature = "ghcr.io/devcontainers-extra/features/npm-package:1"
	// npmPackagesFeature installs a comma-separated list of global npm
	// packages. It is a different reference from npmFeature, so tools that
	// need it can coexist with opencode, and it takes every such tool in one
	// entry. It declares no installsAfter, so Render orders it after node.
	npmPackagesFeature = "ghcr.io/devcontainers-extra/features/npm-packages:1"
	// nodeFeatureID is the node feature without its tag, the form
	// overrideFeatureInstallOrder matches on.
	nodeFeatureID = "ghcr.io/devcontainers/features/node"
)

// Tool is one installable entry in the catalog.
type Tool struct {
	ID      string
	Summary string
	// Official marks a feature published by the devcontainers project or the
	// tool's vendor.
	Official bool
	// Feature is the OCI reference. Empty when the tool is an apt package.
	Feature string
	Options map[string]any
	// Apt is the package name, set only when Feature is empty.
	Apt string
	// Npm is the package, with no version, set only when the tool installs
	// through npmPackagesFeature.
	Npm string
	// Requires names catalog entries this one cannot work without. They are
	// added to the selection.
	Requires []string
}

// catalog is the whole set. Option names are copied from each feature's
// published metadata, since an unknown option name is accepted silently and
// does nothing.
var catalog = []Tool{
	{ID: "aws", Summary: "AWS CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/aws-cli:1"},
	// The adapter bundles the Claude Agent SDK, so it works without the
	// claude-code entry. Its package needs node 22 or later, which the node
	// feature's default (lts) satisfies.
	{ID: "claude-agent-acp", Summary: "Claude ACP adapter",
		Npm: "@agentclientprotocol/claude-agent-acp", Requires: []string{"node"}},
	{ID: "claude-code", Summary: "Claude Code", Official: true,
		Feature: "ghcr.io/anthropics/devcontainer-features/claude-code:1"},
	{ID: "codex-acp", Summary: "Codex ACP adapter",
		Npm: "@agentclientprotocol/codex-acp", Requires: []string{"node"}},
	// Codex has no official feature, so this is a community one. It pins
	// nothing.
	{ID: "codex", Summary: "Codex CLI",
		Feature: "ghcr.io/jsburckhardt/devcontainer-features/codex:1"},
	{ID: "gcloud", Summary: "Google Cloud CLI",
		Feature: "ghcr.io/dhoeric/features/google-cloud-cli:1"},
	{ID: "gh", Summary: "GitHub CLI", Official: true,
		Feature: "ghcr.io/devcontainers/features/github-cli:1"},
	// installGoTools, from feature 1.4.0, also gates golangci-lint. Older
	// releases ignore the unknown option and build the tools anyway.
	{ID: "go", Summary: "Go", Official: true,
		Feature: "ghcr.io/devcontainers/features/go:1",
		Options: map[string]any{"installGoTools": false}},
	{ID: "helm", Summary: "Helm", Official: true, Feature: kubeFeature},
	{ID: "hermes", Summary: "Hermes agent",
		Feature: "ghcr.io/devcontainer-community/devcontainer-features/hermes-agent.nousresearch.com:1"},
	{ID: "kubectl", Summary: "kubectl", Official: true, Feature: kubeFeature},
	{ID: "mise", Summary: "Mise",
		Feature: "ghcr.io/devcontainers-extra/features/mise:1"},
	{ID: "node", Summary: "Node.js", Official: true,
		Feature: nodeFeatureID + ":2"},
	// @opencode/cli, not opencode-ai: 1.x embeds a Bun that crashes under
	// docker exec and kubectl exec, and 2.x is on npm only.
	{ID: "opencode", Summary: "OpenCode agent", Feature: npmFeature,
		Options: map[string]any{"package": "@opencode/cli"}, Requires: []string{"node"}},
	{ID: "python", Summary: "Python", Official: true,
		Feature: "ghcr.io/devcontainers/features/python:1"},
	{ID: "yq", Summary: "yq", Apt: "yq"},
}

// Catalog returns every tool, sorted by id for listing.
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
