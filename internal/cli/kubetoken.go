package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/duy0611/dev-cli/internal/kubetoken"
	"github.com/duy0611/dev-cli/internal/provider"
)

// kubeconfigKey is the variable kubectl reads for its kubeconfig.
const kubeconfigKey = "KUBECONFIG"

// kubeconfigPath is under /tmp: a token must not outlive the container's own
// filesystem.
const kubeconfigPath = "/tmp/dev-kube/config"

// writeKubeconfig puts stdin at kubeconfigPath. Written then renamed, since
// kubectl rereads the file on each call and a rename is atomic.
var writeKubeconfig = []string{"sh", "-c",
	"umask 077 && mkdir -p /tmp/dev-kube && cat > " + kubeconfigPath + ".tmp && " +
		"mv " + kubeconfigPath + ".tmp " + kubeconfigPath}

// addKubeTokenFlag registers --kube-token on a command that launches a process
// in a container.
func addKubeTokenFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "kube-token", false,
		"mint a short-lived service account token on the host and point KUBECONFIG at it")
}

// askKubeTokenIf asks only when wanted. A nil request means no token, so
// callers can pass it on unconditionally.
func askKubeTokenIf(ctx context.Context, want bool) (*kubetoken.Request, error) {
	if !want {
		return nil, nil
	}
	req, err := askKubeToken(ctx)
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// applyKubeToken mints and writes the token when one was asked for. Without a
// request, environ is returned unchanged, so a plain shell never gets
// KUBECONFIG.
func (a *app) applyKubeToken(ctx context.Context, t *target, req *kubetoken.Request,
	environ []provider.EnvVar) ([]provider.EnvVar, error) {
	if req == nil {
		return environ, nil
	}
	kv, err := a.injectKubeToken(ctx, t, *req)
	if err != nil {
		return nil, err
	}
	return withEnv(environ, kv), nil
}

// askKubeToken asks which token to mint. Called before the container starts, so
// a host-side failure starts nothing.
func askKubeToken(ctx context.Context) (kubetoken.Request, error) {
	// Refused rather than skipped: a silently missing token fails later with an
	// error that names nothing dev did.
	if !isTerminal(os.Stdin) {
		return kubetoken.Request{}, usageErrorf(
			"--kube-token asks which context, namespace and service account to use, and needs a terminal")
	}
	req, err := newChooser().ask(ctx)
	if errors.Is(err, errPickCancelled) {
		return kubetoken.Request{}, errors.New("cancelled")
	}
	return req, err
}

// chooser asks the four questions. Its streams are the prompter's, so the
// free-text half is testable.
type chooser struct {
	prompt *prompter
	out    io.Writer
}

// newChooser asks on stderr, so a question never lands in a captured stdout.
func newChooser() chooser {
	return chooser{prompt: newPrompter(os.Stdin, os.Stderr), out: os.Stderr}
}

func (c chooser) ask(ctx context.Context) (kubetoken.Request, error) {
	contexts, current, err := kubetoken.Contexts(ctx)
	if err != nil {
		return kubetoken.Request{}, err
	}
	if len(contexts) == 0 {
		return kubetoken.Request{}, errors.New("the host kubeconfig has no contexts to mint a token from")
	}
	kubeContext, err := c.choose(ctx, "context", contexts, current)
	if err != nil {
		return kubetoken.Request{}, err
	}

	namespaces, def, err := kubetoken.Namespaces(ctx, kubeContext)
	if err != nil {
		c.listFailed("namespaces", err)
	}
	namespace, err := c.choose(ctx, "namespace", namespaces, def)
	if err != nil {
		return kubetoken.Request{}, err
	}

	accounts, err := kubetoken.ServiceAccounts(ctx, kubeContext, namespace)
	if err != nil {
		c.listFailed("service accounts", err)
	}
	account, err := c.choose(ctx, "service account", accounts, "")
	if err != nil {
		return kubetoken.Request{}, err
	}

	duration, err := c.askDuration()
	if err != nil {
		return kubetoken.Request{}, err
	}
	return kubetoken.Request{
		Context: kubeContext, Namespace: namespace, ServiceAccount: account, Duration: duration,
	}, nil
}

// listFailed warns and falls back to free text. RBAC often forbids listing what
// one may still use by name.
func (c chooser) listFailed(what string, err error) {
	fmt.Fprintf(c.out, "dev: cannot list %s (%v); type the name instead\n", what, err)
}

// choose picks one of items, with def first: fzf when present, free text
// otherwise.
func (c chooser) choose(ctx context.Context, label string, items []string, def string) (string, error) {
	items = defaultFirst(items, def)
	if len(items) > 0 {
		if _, err := exec.LookPath("fzf"); err == nil {
			return fzf(ctx, label, items)
		}
		fmt.Fprintf(c.out, "%ss: %s\n", label, strings.Join(items, ", "))
	}
	answer, err := c.prompt.ask(label, def)
	if err != nil {
		return "", err
	}
	if answer == "" {
		// EOF with no default means the terminal went away, not an answer.
		return "", errPickCancelled
	}
	return answer, nil
}

// fzf takes the choices on stdin and returns the pick on stdout; it draws on
// /dev/tty itself.
func fzf(ctx context.Context, label string, items []string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "fzf", "--prompt", label+"> ", "--height", "40%", "--reverse")
	cmd.Stdin = strings.NewReader(strings.Join(items, "\n") + "\n")
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		// 130 is Esc or ^C, 1 is no match. Both mean the operator walked away.
		if errors.As(err, &exit) && (exit.ExitCode() == 130 || exit.ExitCode() == 1) {
			return "", errPickCancelled
		}
		return "", fmt.Errorf("running fzf: %w", err)
	}
	pick := strings.TrimSpace(out.String())
	if pick == "" {
		return "", errPickCancelled
	}
	return pick, nil
}

// defaultFirst moves def to the front, so enter in fzf takes it.
func defaultFirst(items []string, def string) []string {
	if def == "" {
		return items
	}
	out := []string{def}
	for _, it := range items {
		if it != def {
			out = append(out, it)
		}
	}
	return out
}

// askDuration asks how long the token lives, until it parses.
func (c chooser) askDuration() (time.Duration, error) {
	label := "duration, empty for kubectl's default (1h)"
	for {
		answer, err := c.prompt.ask(label, "")
		if err != nil {
			return 0, err
		}
		if answer == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(answer)
		if err == nil && d > 0 {
			return d, nil
		}
		fmt.Fprintf(c.out, "  %q is not a duration; use a form like 30m or 4h\n", answer)
	}
}

// injectKubeToken mints the token and writes it into the running container,
// returning the variable that points at it.
func (a *app) injectKubeToken(ctx context.Context, t *target, req kubetoken.Request) (provider.EnvVar, error) {
	return a.injectKubeTokenWith(ctx, t, req, kubetoken.Mint)
}

// injectKubeTokenWith takes the mint as an argument so the write can be tested
// without a cluster.
func (a *app) injectKubeTokenWith(ctx context.Context, t *target, req kubetoken.Request,
	mint func(context.Context, kubetoken.Request) (kubetoken.Token, error)) (provider.EnvVar, error) {
	tok, err := mint(ctx, req)
	if err != nil {
		return provider.EnvVar{}, err
	}
	reportExpiry(a, req, tok, time.Now())
	// Identity and expiry only, never the token.
	fields := map[string]any{
		"context":         req.Context,
		"namespace":       req.Namespace,
		"service_account": req.ServiceAccount,
	}
	if req.Duration > 0 {
		fields["requested"] = req.Duration.String()
	}
	if !tok.Expiry.IsZero() {
		fields["expires"] = tok.Expiry.UTC().Format(time.RFC3339)
	}
	a.record("kube-token", t.workspace.Name, t.container.Name, fields)

	// On stdin, never as an argument: arguments show in the host's process
	// list.
	var stderr bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, writeKubeconfig, provider.ExecOpts{
		Stdin: bytes.NewReader(tok.Kubeconfig), Stdout: io.Discard, Stderr: &stderr,
	}); err != nil {
		// Fail before the command runs, so the failure names what dev did.
		return provider.EnvVar{}, fmt.Errorf("writing the kubeconfig into container %s: %w%s",
			t.container.Name, err, indentStderr(stderr.String()))
	}

	// A warning, not a failure: another client in the container may read the
	// file. dev does not install kubectl (invariant 9).
	if err := t.provider.Exec(ctx, t.container, []string{"sh", "-c", "command -v kubectl"},
		provider.ExecOpts{Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		warnf(a, "container %s has no kubectl; the token is written but nothing there can use it — "+
			"add kubectl to the project's devcontainer.json, or use --tools kubectl for a generated one",
			t.container.Name)
	}
	return provider.EnvVar{Key: kubeconfigKey, Value: kubeconfigPath}, nil
}

// reportExpiry says when the token stops working, and warns if the cluster
// issued less than asked. That is the only place the operator learns it before
// kubectl says Unauthorized.
func reportExpiry(a *app, req kubetoken.Request, tok kubetoken.Token, now time.Time) {
	who := fmt.Sprintf("%s/%s on %s", req.Namespace, req.ServiceAccount, req.Context)
	if tok.Expiry.IsZero() {
		warnf(a, "token for %s minted; could not read when it expires", who)
		return
	}
	left := tok.Expiry.Sub(now).Round(time.Minute)
	warnf(a, "token for %s expires at %s (%s)", who, tok.Expiry.Local().Format("15:04"), left)
	if tok.Shortened(req.Duration, now) {
		warnf(a, "asked for %s, the cluster issued %s; its maximum is lower", req.Duration, left)
	}
}

// withEnv replaces any earlier entry of the same key. Appended last, so this
// command's flag beats a workspace setting.
func withEnv(environ []provider.EnvVar, v provider.EnvVar) []provider.EnvVar {
	out := make([]provider.EnvVar, 0, len(environ)+1)
	for _, e := range environ {
		if e.Key != v.Key {
			out = append(out, e)
		}
	}
	return append(out, v)
}

func indentStderr(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	return "\n  " + strings.ReplaceAll(s, "\n", "\n  ")
}
