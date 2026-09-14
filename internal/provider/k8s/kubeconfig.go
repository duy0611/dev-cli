package k8s

import (
	"context"
	"os/exec"
	"strings"
)

// KubeconfigDefaults reports the current context and its namespace.
//
// Defaults for the interactive configure, so the common case is three presses
// of return. Best effort on purpose: no kubeconfig, no kubectl, or no current
// context are all ordinary states on a machine that has not been pointed at a
// cluster yet, and none of them should stop a provider being configured by
// hand.
func KubeconfigDefaults(ctx context.Context) (kubeContext, namespace string) {
	kubeContext = kubeconfigValue(ctx, "config", "current-context")
	if kubeContext == "" {
		return "", ""
	}
	// --minify restricts the view to the current context, so this is that
	// context's namespace rather than the first one in the file.
	namespace = kubeconfigValue(ctx, "config", "view", "--minify",
		"-o", "jsonpath={..namespace}")
	return kubeContext, namespace
}

func kubeconfigValue(ctx context.Context, args ...string) string {
	if _, err := exec.LookPath(kubectlBin); err != nil {
		return ""
	}
	out, err := exec.CommandContext(ctx, kubectlBin, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
