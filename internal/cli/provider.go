package cli

import (
	"errors"
	"io"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/store"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/xpath"
	"github.com/spf13/cobra"
)

func newProviderCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Configure where devcontainers run",
	}
	cmd.AddCommand(newProviderConfigureCmd(a), newProviderListCmd(a))
	return cmd
}

func newProviderConfigureCmd(a *app) *cobra.Command {
	var kind string

	cmd := &cobra.Command{
		Use:   "configure NAME",
		Short: "Create or update a provider",
		Long: "Create or update a provider.\n\n" +
			"Idempotent on purpose: re-running it with a different --kind is how a\n" +
			"provider is corrected, which beats deleting one that workspaces already\n" +
			"reference.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderConfigure(a, args[0], kind)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", string(model.KindLocal),
		"engine this provider drives (local)")
	return cmd
}

func runProviderConfigure(a *app, name, kind string) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}

	switch model.ProviderKind(kind) {
	case model.KindLocal:
	case model.KindK8s:
		return usageErrorf("provider kind %q is not implemented yet", kind)
	default:
		return usageErrorf("unknown provider kind %q (local)", kind)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	if err := st.PutProvider(model.Provider{Name: name, Kind: model.ProviderKind(kind)}); err != nil {
		return err
	}
	a.printf("provider %s (%s)\n", name, kind)
	return nil
}

func newProviderListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured providers",
		Args:  noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderList(a)
		},
	}
}

func runProviderList(a *app) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	providers, err := st.ListProviders()
	if err != nil {
		return err
	}
	if len(providers) == 0 {
		a.printf("no providers; run: dev provider configure local --kind local\n")
		return nil
	}
	return a.table(func(w io.Writer) {
		header(w, "NAME", "KIND")
		for _, p := range providers {
			row(w, p.Name, string(p.Kind))
		}
	})
}

// providerExists reports whether a provider is configured, translating the
// store's not-found into the command-level message.
func providerExists(st *store.Store, name string) error {
	if _, err := st.GetProvider(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFoundErrorf("no such provider: %s", name)
		}
		return err
	}
	return nil
}
