package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
			"Re-running it changes the settings named on the command line and\n" +
			"leaves the rest as they were.\n\n" +
			"The kind is the exception: it cannot change. Workspaces keep naming\n" +
			"the provider, and the containers under them stay in the engine they\n" +
			"were created in, so a flipped kind would leave records pointing at an\n" +
			"engine that has never heard of them. Remove the provider and\n" +
			"configure it again instead.\n\n" +
			"The --context and later flags apply to --kind k8s only. Pass - as the\n" +
			"value of an optional one to clear it.",
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

func runProviderConfigure(a *app, name, kind string, flags k8s.Config) error {
	if err := xpath.ValidateName(name); err != nil {
		return usageError(err)
	}

	// Checked before the stored provider is, so a misspelt kind is reported as
	// one rather than as an attempt to change the kind.
	pk := model.ProviderKind(kind)
	if pk != model.KindLocal && pk != model.KindK8s {
		return usageErrorf("unknown provider kind %q (local, k8s)", kind)
	}

	st, err := a.store()
	if err != nil {
		return err
	}

	// Looked up before anything else is done with the input, for two reasons: a
	// kind change has to be refused before the operator answers eight prompts,
	// and the stored settings are what the unanswered ones fall back to.
	existing, err := existingProvider(st, name, pk)
	if err != nil {
		return err
	}

	var config string
	if pk == model.KindK8s {
		var stored k8s.Config
		if existing.Config != "" {
			if stored, err = k8s.ParseConfig(existing.Config); err != nil {
				return err
			}
		}

		// Ask for whatever the flags did not supply, but only at a terminal: a
		// scripted run must fail naming the flag rather than block on a prompt
		// nobody will see. Either way the stored settings are the fallback, so
		// both paths change only what was named.
		if isTerminal(os.Stdin) {
			p := newPrompter(os.Stdin, a.out)
			if flags, err = promptK8s(context.Background(), p, flags, stored); err != nil {
				return err
			}
		} else {
			flags = mergeK8s(flags, stored)
		}

		// Validated and defaulted now rather than at first use: a provider
		// missing its registry would otherwise look fine until a create spends
		// a minute building and then pushes nowhere.
		cfg, err := k8s.ParseConfig(mustJSON(flags))
		if err != nil {
			return usageError(err)
		}
		if config, err = cfg.Marshal(); err != nil {
			return err
		}
	}

	if err := st.PutProvider(model.Provider{
		Name:   name,
		Kind:   pk,
		Config: config,
	}); err != nil {
		return err
	}
	a.printf("provider %s (%s)\n", name, kind)
	return nil
}

// existingProvider returns the provider being reconfigured, or a zero value
// when this is a new one.
//
// A changed kind is refused rather than applied. Workspaces go on naming the
// provider and the containers under them stay in the engine they were created
// in, so a flip leaves records pointing at an engine that has never heard of
// them — `container list` then answers confidently and wrongly, and `remove`
// can never reach the real container. `provider remove` already refuses while a
// workspace names it, which is what makes remove-and-recreate the recoverable
// route rather than a second copy of that rule here.
func existingProvider(st *store.Store, name string, kind model.ProviderKind) (model.Provider, error) {
	p, err := st.GetProvider(name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return model.Provider{}, nil
	case err != nil:
		return model.Provider{}, err
	case p.Kind != kind:
		return model.Provider{}, usageErrorf(
			"provider %s is %s, not %s; to change its kind:\n"+
				"  dev provider remove %s\n"+
				"  dev provider configure %s --kind %s",
			name, p.Kind, kind, name, name, kind)
	}
	return p, nil
}

// clearMarker is the value that empties an optional setting.
//
// An omitted flag now means "keep what is stored", which on its own would leave
// no way to remove a pull secret: an empty flag and an absent one are the same
// string, and pressing return at a prompt keeps the default.
const clearMarker = "-"

// k8sSetting is one configurable setting: where it is being written, what the
// stored provider has for it, and how to ask for it.
type k8sSetting struct {
	target   *string
	stored   string
	label    string
	fallback string // when neither the flags nor the stored config has one
	required bool
}

// k8sSettings pairs every setting with its stored counterpart, so the prompted
// path and the scripted one walk one list and cannot drift apart.
func k8sSettings(cfg *k8s.Config, stored k8s.Config, kubeContext, kubeNamespace string) []k8sSetting {
	return []k8sSetting{
		{&cfg.Context, stored.Context, "kubeconfig context", kubeContext, false},
		{&cfg.Namespace, stored.Namespace, "namespace (must already exist)", firstNonEmpty(kubeNamespace, "default"), true},
		// No default worth guessing, and nothing works without it.
		{&cfg.Registry, stored.Registry, "registry prefix to push images to", "", true},
		{&cfg.Platform, stored.Platform, "platform to build for", "linux/amd64", true},
		{&cfg.StorageSize, stored.StorageSize, "volume size per container", "20Gi", true},
		{&cfg.StorageClass, stored.StorageClass, "storage class (blank for the cluster default)", "", false},
		{&cfg.ServiceAccount, stored.ServiceAccount, "service account (blank for the namespace default)", "", false},
		{&cfg.ImagePullSecret, stored.ImagePullSecret, "image pull secret (blank if the nodes can pull)", "", false},
	}
}

// mergeK8s fills in from the stored provider whatever the flags left out.
//
// The scripted half of the same rule the prompts follow, so that changing one
// setting from a script does not drop the seven the operator did not repeat.
func mergeK8s(flags, stored k8s.Config) k8s.Config {
	cfg := flags
	// The kubeconfig defaults are the prompts' business: a scripted run takes
	// what it was given, and ParseConfig supplies the rest.
	for _, s := range k8sSettings(&cfg, stored, "", "") {
		switch *s.target {
		case "":
			*s.target = s.stored
		case clearMarker:
			*s.target = ""
		}
	}
	return cfg
}

// promptK8s fills in the settings the flags left empty.
//
// Only the empty ones: a flag given on the command line is an answer already,
// and asking again would make the flags pointless. Defaults come from the
// stored provider first and the kubeconfig second, so re-running configure to
// change one setting and pressing return through the rest is a no-op rather
// than a reset to linux/amd64 and 20Gi.
func promptK8s(ctx context.Context, p *prompter, flags, stored k8s.Config) (k8s.Config, error) {
	kubeContext, kubeNamespace := k8s.KubeconfigDefaults(ctx)
	cfg := flags

	fmt.Fprintf(p.out, "configuring a Kubernetes provider; press return to accept a default\n")

	for _, s := range k8sSettings(&cfg, stored, kubeContext, kubeNamespace) {
		if *s.target != "" {
			if *s.target == clearMarker {
				*s.target = ""
			}
			continue
		}

		def := firstNonEmpty(s.stored, s.fallback)
		label := s.label
		// Advertised only where it does something: a required setting cannot
		// be cleared, and neither can one that is already empty.
		if def != "" && !s.required {
			label += " (- to clear)"
		}

		var (
			answer string
			err    error
		)
		if s.required {
			answer, err = p.askRequired(label, def)
		} else {
			answer, err = p.ask(label, def)
		}
		if err != nil {
			return cfg, err
		}
		if answer == clearMarker {
			answer = ""
		}
		*s.target = answer
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
