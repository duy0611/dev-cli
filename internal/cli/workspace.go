package cli

import (
	"errors"
	"io"
	"strings"

	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
	"github.com/spf13/cobra"
)

func newWorkspaceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Group containers and the settings they launch with",
	}
	cmd.AddCommand(
		newWorkspaceInitCmd(a),
		newWorkspaceUseCmd(a),
		newWorkspaceListCmd(a),
		newWorkspaceSetCmd(a),
		newWorkspaceUnsetCmd(a),
		newWorkspaceShowCmd(a),
		newWorkspaceRemoveCmd(a),
	)
	return cmd
}

func newWorkspaceRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an empty workspace",
		Long: "Remove a workspace and its settings.\n\n" +
			"Refused while it still holds containers, running or not. Removing the\n" +
			"records would leave their containers on the engine with nothing left\n" +
			"that knows their names — remove them first.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceRemove(a, args[0])
		},
	}
}

func runWorkspaceRemove(a *app, name string) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	if _, err := st.GetWorkspace(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such workspace: %s", name)
		}
		return err
	}

	containers, err := st.ListContainers(name)
	if err != nil {
		return err
	}
	if len(containers) > 0 {
		names := make([]string, 0, len(containers))
		for _, c := range containers {
			names = append(names, c.Name)
		}
		return usageErrorf("workspace %s still holds %d container(s): %s",
			name, len(names), strings.Join(names, ", "))
	}

	if err := st.DeleteWorkspace(name); err != nil {
		return err
	}

	// Leaving the pointer behind would make the next command report "no such
	// workspace" about a name the operator never typed.
	active, err := st.ActiveWorkspace()
	if err != nil {
		return err
	}
	if active == name {
		if err := st.ClearActiveWorkspace(); err != nil {
			return err
		}
		a.printf("workspace %s removed; no active workspace now\n", name)
		return nil
	}
	a.printf("workspace %s removed\n", name)
	return nil
}

func newWorkspaceInitCmd(a *app) *cobra.Command {
	var provider string

	cmd := &cobra.Command{
		Use:   "init NAME",
		Short: "Create a workspace",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceInit(a, args[0], provider)
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "provider this workspace runs on (required)")
	return cmd
}

func runWorkspaceInit(a *app, name, provider string) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}
	if provider == "" {
		return usageErrorf("--provider is required")
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	if err := providerExists(st, provider); err != nil {
		return err
	}

	err = st.CreateWorkspace(model.Workspace{Name: name, ProviderName: provider})
	if errors.Is(err, store.ErrExists) {
		return usageErrorf("workspace %s already exists", name)
	}
	if err != nil {
		return err
	}
	a.printf("workspace %s (provider %s)\n", name, provider)

	// The first workspace becomes active, so that a fresh install does not
	// demand a `workspace use` before anything else works.
	active, err := st.ActiveWorkspace()
	if err != nil {
		return err
	}
	if active == "" {
		if err := st.SetActiveWorkspace(name); err != nil {
			return err
		}
		a.printf("active workspace is now %s\n", name)
	}
	return nil
}

func newWorkspaceUseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Set the active workspace",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.store()
			if err != nil {
				return err
			}
			if err := st.SetActiveWorkspace(args[0]); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return notFoundErrorf("no such workspace: %s", args[0])
				}
				return err
			}
			a.printf("active workspace is now %s\n", args[0])
			return nil
		},
	}
}

func newWorkspaceListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceList(a)
		},
	}
}

func runWorkspaceList(a *app) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	workspaces, err := st.ListWorkspaces()
	if err != nil {
		return err
	}
	if len(workspaces) == 0 {
		a.printf("no workspaces; run: dev workspace init NAME --provider PROVIDER\n")
		return nil
	}
	active, err := st.ActiveWorkspace()
	if err != nil {
		return err
	}

	return a.table(func(w io.Writer) {
		header(w, "", "NAME", "PROVIDER", "CONTAINERS")
		for _, ws := range workspaces {
			containers, err := st.ListContainers(ws.Name)
			count := "?"
			if err == nil {
				count = itoa(len(containers))
			}
			marker := " "
			if ws.Name == active {
				marker = "*"
			}
			row(w, marker, ws.Name, ws.ProviderName, count)
		}
	})
}

func newWorkspaceSetCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "set KEY SPEC",
		Short: "Set one environment value for a workspace's containers",
		Long: "Set one environment value for a workspace's containers.\n\n" +
			"SPEC says where the value comes from, and is never the value itself:\n\n" +
			"  literal:https://example.invalid   used as written\n" +
			"  keychain:SERVICE                  read from the macOS Keychain\n" +
			"  op://vault/item/field             read with the 1Password CLI\n",
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceSet(a, workspace, args[0], args[1])
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func runWorkspaceSet(a *app, workspace, key, spec string) error {
	if err := xpath.ValidateEnvKey(key); err != nil {
		return usageError(err)
	}
	if strings.TrimSpace(spec) == "" {
		return usageErrorf("empty spec for %s", key)
	}

	name, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	if err := st.SetSetting(name, key, spec); err != nil {
		return err
	}
	a.printf("%s set on workspace %s\n", key, name)
	return nil
}

func newWorkspaceUnsetCmd(a *app) *cobra.Command {
	var workspace string

	cmd := &cobra.Command{
		Use:   "unset KEY",
		Short: "Remove one setting from a workspace",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := a.workspaceName(workspace)
			if err != nil {
				return err
			}
			st, err := a.store()
			if err != nil {
				return err
			}
			if err := st.UnsetSetting(name, args[0]); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return notFoundErrorf("workspace %s has no setting %s", name, args[0])
				}
				return err
			}
			a.printf("%s removed from workspace %s\n", args[0], name)
			return nil
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	return cmd
}

func newWorkspaceShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show a workspace's settings",
		Long: "Show a workspace's settings.\n\n" +
			"Names the workspace outright rather than defaulting to the active one:\n" +
			"this is the command an operator reads before trusting what a container\n" +
			"will launch with, and it should not depend on a pointer set elsewhere.\n\n" +
			"Prints the specs, never the resolved values: a secret that reaches a\n" +
			"terminal reaches the scrollback and the shell history with it.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceShow(a, args[0])
		},
	}
}

func runWorkspaceShow(a *app, name string) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	ws, err := st.GetWorkspace(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such workspace: %s", name)
		}
		return err
	}
	settings, err := st.ListSettings(name)
	if err != nil {
		return err
	}

	a.printf("workspace %s\n", ws.Name)
	a.printf("provider  %s\n", ws.ProviderName)
	if len(settings) == 0 {
		a.printf("settings  none\n")
		return nil
	}
	a.printf("settings\n")
	return a.table(func(w io.Writer) {
		for _, s := range settings {
			row(w, "  "+s.Key, s.Spec)
		}
	})
}

// addWorkspaceFlag installs the --workspace override that every
// workspace-scoped and container-scoped command accepts.
func addWorkspaceFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "workspace", "", "workspace to act on (default: the active one)")
}
