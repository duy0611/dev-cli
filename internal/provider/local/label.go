package local

import (
	"fmt"

	"github.com/duy0611/dev-cli/internal/model"
)

// Label keys. Namespaced so they cannot collide with the devcontainer CLI's own
// devcontainer.* labels, or with whatever the project's image already sets.
const (
	labelWorkspace = "dev.workspace"
	labelContainer = "dev.container"
)

// idLabels returns the labels identifying one container, in a fixed order. This
// is the only place they are built. The CLI infers a label from
// --workspace-folder when none is given, so a call that passes a different set,
// or none, creates a second container beside the first.
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

// dockerFilterArgs renders the same labels as docker filters.
func dockerFilterArgs(c model.Container) []string {
	var args []string
	for _, l := range idLabels(c) {
		args = append(args, "--filter", "label="+l)
	}
	return args
}

// VolumeName is the docker volume a folderless or isolated container keeps its
// work in. It is built here so create, up and remove agree on the spelling.
func VolumeName(workspace, container string) string {
	return fmt.Sprintf("dev-%s-%s", workspace, container)
}

// WorkspaceFolder is where a container's workspace volume is mounted.
func WorkspaceFolder(container string) string {
	return "/workspaces/" + container
}

// StateVolumeName is the docker volume a container keeps its agents'
// configuration on. The workspace is part of the name because container names
// are unique only within a workspace.
func StateVolumeName(workspace, container string) string {
	return fmt.Sprintf("dev-%s-%s-state", workspace, container)
}
