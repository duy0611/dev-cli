package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/dcgen"
)

func newContainerToolsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tools",
		Short: "List the tools a generated container can install",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerTools(a)
		},
	}
}

func runContainerTools(a *app) error {
	return a.table(func(w io.Writer) {
		header(w, "TOOL", "SUMMARY", "SOURCE")
		for _, t := range dcgen.Catalog() {
			// Whether a feature is published by the devcontainers project (or
			// the tool's own vendor) is the operator's business: the rest are
			// community images they are choosing to trust.
			source := "community"
			if t.Official {
				source = "official"
			}
			row(w, t.ID, t.Summary, source)
		}
	})
}

func newContainerConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the configuration a container is built from",
		Args:  noArgs(),
		RunE:  groupRunE,
	}

	var workspace string
	show := &cobra.Command{
		Use:   "show NAME",
		Short: "Print the devcontainer config a container is built from",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContainerConfigShow(a, workspace, args[0])
		},
	}
	addWorkspaceFlag(show, &workspace)

	cmd.AddCommand(show)
	return cmd
}

func runContainerConfigShow(a *app, workspace, name string) error {
	t, err := a.resolve(workspace, name)
	if err != nil {
		return err
	}
	defer t.release()

	if t.container.GeneratedConfig != "" {
		a.printf("%s", t.container.GeneratedConfig)
		return nil
	}

	// A project-owned container. The file on disk is not the whole answer:
	// dev merges its state mount into it per invocation and hands the result
	// to the devcontainer CLI, so the document the container is actually built
	// from exists only for the length of one command. Printing the path alone
	// would name a file that is not quite what ran.
	//
	// A parse failure is deliberately fatal here, unlike on stop or remove:
	// this command exists to answer "what was this built from", and the honest
	// answer when the project's file will not parse is the error, not the file
	// that dev could not use.
	if err := t.requireOverride(); err != nil {
		return err
	}

	source := t.container.ConfigPath
	merged := t.container.OverrideConfigPath
	if merged == "" {
		// No merge to show: a container that persists no state gets its
		// document through --config untouched, so the project's file is
		// literally what the CLI receives. Said out loud, because an operator
		// comparing two containers should not have to infer why one has a
		// mounts entry dev added and the other does not.
		warnf(a, "%s (no merge: state off)", source)
		return printFile(a, source)
	}

	warnf(a, "merged from %s", source)
	return printFile(a, merged)
}

// printFile copies a configuration document to the command's output.
//
// The document goes to stdout while the path above it goes to stderr, so
// `config show | jq` still receives valid JSON and nothing else.
func printFile(a *app, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	a.printf("%s", body)
	return nil
}
