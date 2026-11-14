package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/buzzcli"
	"github.com/duy0611/dev-cli/internal/model"
	"github.com/duy0611/dev-cli/internal/provider/local"
	"github.com/duy0611/dev-cli/internal/store"
	"github.com/duy0611/dev-cli/internal/xpath"
)

func newWorkspaceBuzzCmd(a *app) *cobra.Command {
	var workspace, arm64, amd64 string

	cmd := &cobra.Command{
		Use:   "buzz-binary",
		Short: "Name the host files holding the buzz CLI for a workspace's containers",
		Long: "Name the host files holding the buzz CLI for a workspace's containers.\n\n" +
			"block/buzz publishes no Linux build, so you build one per architecture\n" +
			"and name it here. A container mounts the file matching its own\n" +
			"architecture, read-only, at /usr/local/bin/buzz when it is created or\n" +
			"rebuilt. A file that is missing or is not a Linux ELF for its\n" +
			"architecture is refused. Experimental, like dev container acp.\n\n" +
			"With no flag, prints what is set. An empty value clears one:\n" +
			"  dev workspace buzz-binary --arm64 ~/buzz/linux-arm64 --amd64 ''\n\n" +
			"A Kubernetes workspace has no host mounts; its image must carry buzz.",
		Args: noArgs(),
		RunE: func(cmd *cobra.Command, args []string) error {
			var armP, amdP *string
			if cmd.Flags().Changed("arm64") {
				armP = &arm64
			}
			if cmd.Flags().Changed("amd64") {
				amdP = &amd64
			}
			return runWorkspaceBuzz(a, workspace, armP, amdP)
		},
	}
	addWorkspaceFlag(cmd, &workspace)
	cmd.Flags().StringVar(&arm64, "arm64", "", "host file for linux/arm64 containers")
	cmd.Flags().StringVar(&amd64, "amd64", "", "host file for linux/amd64 containers")
	return cmd
}

// runWorkspaceBuzz sets the buzz CLI files named by the non-nil arguments; an
// empty one clears its architecture. With both nil it only prints.
func runWorkspaceBuzz(a *app, workspace string, arm64, amd64 *string) error {
	name, err := a.workspaceName(workspace)
	if err != nil {
		return err
	}
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
	if arm64 == nil && amd64 == nil {
		a.printf("buzz arm64 %s\nbuzz amd64 %s\n", orNone(ws.BuzzArm64), orNone(ws.BuzzAmd64))
		return nil
	}

	rec, err := st.GetProvider(ws.ProviderName)
	if err != nil {
		return err
	}
	if rec.Kind == model.KindK8s {
		return usageErrorf("workspace %s runs on Kubernetes, which has no host mounts: "+
			"put the buzz CLI in the container image instead", name)
	}

	next := map[string]*string{"arm64": &ws.BuzzArm64, "amd64": &ws.BuzzAmd64}
	given := map[string]*string{"arm64": arm64, "amd64": amd64}
	for _, arch := range buzzcli.Arches {
		v := given[arch]
		if v == nil {
			continue
		}
		if strings.TrimSpace(*v) == "" {
			*next[arch] = ""
			continue
		}
		path, err := resolveBuzzFile(*v, arch)
		if err != nil {
			return usageError(err)
		}
		*next[arch] = path
	}
	if err := st.SetBuzzBinaries(name, ws.BuzzArm64, ws.BuzzAmd64); err != nil {
		return err
	}
	a.printf("buzz arm64 %s\nbuzz amd64 %s\n", orNone(ws.BuzzArm64), orNone(ws.BuzzAmd64))
	a.printf("containers in workspace %s pick this up when they are next created or rebuilt\n", name)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func resolveBuzzFile(path, arch string) (string, error) {
	abs, err := xpath.ResolveFile(path)
	if err != nil {
		return "", fmt.Errorf("the buzz binary for linux/%s: %w", arch, err)
	}
	// The mounts entry is comma-separated, so a comma would split it.
	if strings.Contains(abs, ",") {
		return "", fmt.Errorf("%s: a comma in the path cannot be mounted", abs)
	}
	if err := buzzcli.CheckELF(abs, arch); err != nil {
		return "", err
	}
	return abs, nil
}

// buzzMount is the mounts entry that puts the workspace's buzz CLI in c, or ""
// when none applies: k8s, no setting, or no file for this container's
// architecture. A file that is there but wrong is an error, so it fails at
// create and rebuild rather than mounting nothing useful.
func (a *app) buzzMount(ctx context.Context, c model.Container, overrides bool) (string, error) {
	if !overrides {
		return "", nil
	}
	st, err := a.store()
	if err != nil {
		return "", err
	}
	ws, err := st.GetWorkspace(c.WorkspaceName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if ws.BuzzArm64 == "" && ws.BuzzAmd64 == "" {
		return "", nil
	}
	arch, err := a.engineArch(ctx)
	if err != nil {
		return "", err
	}
	file := ws.BuzzBinary(arch)
	if file == "" {
		return "", nil
	}
	if err := buzzcli.CheckELF(file, arch); err != nil {
		return "", fmt.Errorf("%w (change it with: dev workspace buzz-binary --%s PATH)", err, arch)
	}
	return buzzcli.MountEntry(file), nil
}

// engineArch is local.EngineArch asked once per run: planning happens on every
// command, and each ask is a `docker version`. Only a success is kept.
func (a *app) engineArch(ctx context.Context) (string, error) {
	a.archMu.Lock()
	defer a.archMu.Unlock()
	if a.arch != "" {
		return a.arch, nil
	}
	arch, err := local.EngineArch(ctx)
	if err == nil {
		a.arch = arch
	}
	return arch, err
}
