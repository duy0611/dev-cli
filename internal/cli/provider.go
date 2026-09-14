package cli

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/model"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/provider/k8s"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/store"
	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/xpath"
	"github.com/spf13/cobra"
)

func newProviderCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Configure where devcontainers run",
	}
	cmd.AddCommand(newProviderConfigureCmd(a), newProviderListCmd(a), newProviderRemoveCmd(a))
	return cmd
}

func newProviderRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an unreferenced provider",
		Long: "Remove a provider.\n\n" +
			"Refused while any workspace still names it, since those workspaces\n" +
			"would have nowhere left to run.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderRemove(a, args[0])
		},
	}
}

func runProviderRemove(a *app, name string) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	if err := providerExists(st, name); err != nil {
		return err
	}

	// Checked rather than left to the foreign key, so the message names the
	// workspaces in the way instead of a constraint.
	users, err := st.WorkspacesUsing(name)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return usageErrorf("provider %s is still used by %d workspace(s): %s",
			name, len(users), strings.Join(users, ", "))
	}

	if err := st.DeleteProvider(name); err != nil {
		return err
	}
	a.printf("provider %s removed\n", name)
	return nil
}

func newProviderConfigureCmd(a *app) *cobra.Command {
	var (
		kind string
		k8s  k8s.Config
	)

	cmd := &cobra.Command{
		Use:   "configure NAME",
		Short: "Create or update a provider",
		Long: "Create or update a provider.\n\n" +
			"Idempotent on purpose: re-running it with a different --kind is how a\n" +
			"provider is corrected, which beats deleting one that workspaces already\n" +
			"reference.\n\n" +
			"The --context and later flags apply to --kind k8s only.",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderConfigure(a, args[0], kind, k8s)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", string(model.KindLocal),
		"engine this provider drives (local, k8s)")

	f := cmd.Flags()
	f.StringVar(&k8s.Context, "context", "", "k8s: kubeconfig context (default: the current one)")
	f.StringVar(&k8s.Namespace, "namespace", "", "k8s: namespace, which must already exist")
	f.StringVar(&k8s.Registry, "registry", "", "k8s: registry prefix images are pushed to")
	f.StringVar(&k8s.Platform, "platform", "", "k8s: platform to build for (default linux/amd64)")
	f.StringVar(&k8s.StorageSize, "storage-size", "", "k8s: PVC size per container (default 20Gi)")
	f.StringVar(&k8s.StorageClass, "storage-class", "", "k8s: storage class (default: the cluster's)")
	f.StringVar(&k8s.ServiceAccount, "service-account", "", "k8s: service account for the pod")
	f.StringVar(&k8s.ImagePullSecret, "image-pull-secret", "", "k8s: secret for pulling the built image")
	return cmd
}

func runProviderConfigure(a *app, name, kind string, k8sCfg k8s.Config) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}

	var config string
	switch model.ProviderKind(kind) {
	case model.KindLocal:
	case model.KindK8s:
		// Validated and defaulted now rather than at first use: a provider
		// missing its registry would otherwise look fine until a create spends
		// a minute building and then pushes nowhere.
		cfg, err := k8s.ParseConfig(mustJSON(k8sCfg))
		if err != nil {
			return usageError(err)
		}
		if config, err = cfg.Marshal(); err != nil {
			return err
		}
	default:
		return usageErrorf("unknown provider kind %q (local, k8s)", kind)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	if err := st.PutProvider(model.Provider{
		Name:   name,
		Kind:   model.ProviderKind(kind),
		Config: config,
	}); err != nil {
		return err
	}
	a.printf("provider %s (%s)\n", name, kind)
	return nil
}

// mustJSON renders the flag-populated config so ParseConfig can apply the same
// defaults and validation it applies to a stored one, rather than this file
// duplicating them.
func mustJSON(cfg k8s.Config) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		// Config is a flat struct of strings; there is no input that fails.
		panic(err)
	}
	return string(b)
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
