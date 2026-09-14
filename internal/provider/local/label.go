package local

import (
	"fmt"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
)

// Label keys. Namespaced so they cannot collide with the devcontainer CLI's own
// devcontainer.* labels, or with whatever the project's image already sets.
const (
	labelWorkspace = "dev.workspace"
	labelContainer = "dev.container"
)

// idLabels returns the labels identifying one container, in a fixed order.
//
// This is the single place they are built, and it has to stay that way. The
// devcontainer CLI infers an id label from --workspace-folder when none is
// given, so two containers created from one folder would otherwise be the same
// container. Passing them explicitly also means every lookup filters on our own
// keys rather than on a path that a symlink could spell two ways.
//
// The consequence: the same set must be passed on *every* invocation for a
// container. A call that omits one looks up a container that does not exist,
// and the CLI helpfully creates a second one.
func idLabels(c model.Container) []string {
	return []string{
		fmt.Sprintf("%s=%s", labelWorkspace, c.WorkspaceName),
		fmt.Sprintf("%s=%s", labelContainer, c.Name),
	}
}

// idLabelArgs renders the labels as devcontainer CLI arguments.
func idLabelArgs(c model.Container) []string {
	var args []string
	for _, l := range idLabels(c) {
		args = append(args, "--id-label", l)
	}
	return args
}

// dockerFilterArgs renders the same labels as docker filters, so that the two
// tools are always asked about the same container.
func dockerFilterArgs(c model.Container) []string {
	var args []string
	for _, l := range idLabels(c) {
		args = append(args, "--filter", "label="+l)
	}
	return args
}
