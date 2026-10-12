package k8s

import (
	"context"
	"os/exec"
	"strings"
)

// KubeconfigDefaults reports the current context and its namespace.
//
// Best effort: a missing kubeconfig, kubectl or current context is an ordinary
// state.
func KubeconfigDefaults(ctx context.Context) (kubeContext, namespace string) {
	kubeContext = kubeconfigValue(ctx, "config", "current-context")
	if kubeContext == "" {
		return "", ""
	}
	// --minify keeps this to the current context; without it the first
	// namespace in the file wins.
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
