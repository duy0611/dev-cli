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

// kubeconfigKey is the variable kubectl reads to find its kubeconfig.
const kubeconfigKey = "KUBECONFIG"

// kubeconfigPath is where the minted kubeconfig lands in the container.
//
// Under /tmp, not /var/dev-state: that volume outlives a rebuild and holds no
// credentials (invariant 10), and a token has no business outliving the
// container's own filesystem.
const kubeconfigPath = "/tmp/dev-kube/config"

// writeKubeconfig puts stdin at kubeconfigPath.
//
// umask 077 so the directory and the file are the remote user's alone. Written
// then renamed because kubectl in a running session reads the file on every
// call, and a rename is atomic: a read during a refresh sees the old kubeconfig
// or the new one, never half of either.
var writeKubeconfig = []string{"sh", "-c",
	"umask 077 && mkdir -p /tmp/dev-kube && cat > " + kubeconfigPath + ".tmp && " +
		"mv " + kubeconfigPath + ".tmp " + kubeconfigPath}

// addKubeTokenFlag registers --kube-token on a command that launches a process
// in a container.
func addKubeTokenFlag(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "kube-token", false,
		"mint a short-lived service account token on the host and point KUBECONFIG at it")
}

// askKubeTokenIf asks only when the flag is given. A nil request means no
// token was asked for, so every caller can pass the result on unconditionally.
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

// applyKubeToken mints and writes the token when one was asked for, and
// returns environ with KUBECONFIG pointing at it. Without a request, environ
// comes back unchanged: a plain shell never gets KUBECONFIG, even with a file
// left there, so it stays on whatever credentials it had — the pod's own
// in-cluster ones on k8s — rather than quietly taking the last token minted.
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

// askKubeToken asks which token --kube-token should mint.
//
// Called before anything that needs a running container, so nobody answers a
// question after waiting through a slow up — and so a host-side failure (no
// kubectl, no contexts, a cancelled pick) stops start-agent before it starts a
// container for nothing.
func askKubeToken(ctx context.Context) (kubetoken.Request, error) {
	// Refused rather than skipped: the operator asked for a token, and a
	// scripted run that silently went without one would fail later inside the
	// container with an error that names nothing dev did.
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

// chooser asks the four questions.
//
// Its streams are the prompter's, so the free-text half is testable over a
// pipe; fzf draws on /dev/tty by itself.
type chooser struct {
	prompt *prompter
	out    io.Writer
}

// newChooser asks on stderr, so a question never lands in an exec's captured
// stdout.
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

// listFailed warns and lets the question fall back to free text. RBAC commonly
// forbids listing namespaces or service accounts to someone who may still use
// one by name.
func (c chooser) listFailed(what string, err error) {
	fmt.Fprintf(c.out, "dev: cannot list %s (%v); type the name instead\n", what, err)
}

// choose picks one of items, with def offered first.
//
// fzf when the host has it, a free-text prompt otherwise. An empty list goes
// straight to free text: there is nothing for fzf to show.
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
		// EOF with nothing typed and no default. There is no way to go on
		// without a name, so this is the terminal going away, not an answer.
		return "", errPickCancelled
	}
	return answer, nil
}

// fzf runs one pick. The choices go in on stdin and the pick comes back on
// stdout; fzf draws on /dev/tty itself, so neither stream is the terminal.
func fzf(ctx context.Context, label string, items []string) (string, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "fzf", "--prompt", label+"> ", "--height", "40%", "--reverse")
	cmd.Stdin = strings.NewReader(strings.Join(items, "\n") + "\n")
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		// 130 is Esc or ^C; 1 is no match, which with no free-text fallback
		// inside fzf is also the operator walking away.
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

// askDuration asks how long the token should live, again until it parses.
//
// Empty is zero, which passes no --duration at all: the API server's default
// then applies, rather than dev's belief about what that default is.
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
// returning the variable that points the launched command at it.
func (a *app) injectKubeToken(ctx context.Context, t *target, req kubetoken.Request) (provider.EnvVar, error) {
	return a.injectKubeTokenWith(ctx, t, req, kubetoken.Mint)
}

// injectKubeTokenWith is injectKubeToken with the mint supplied, so the write
// can be tested without a cluster.
func (a *app) injectKubeTokenWith(ctx context.Context, t *target, req kubetoken.Request,
	mint func(context.Context, kubetoken.Request) (kubetoken.Token, error)) (provider.EnvVar, error) {
	tok, err := mint(ctx, req)
	if err != nil {
		return provider.EnvVar{}, err
	}
	reportExpiry(a, req, tok, time.Now())
	// Which identity went into the container and until when — never the token.
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

	// On stdin, never as an argument: an argument shows in the process list on
	// the host and in the container.
	var stderr bytes.Buffer
	if err := t.provider.Exec(ctx, t.container, writeKubeconfig, provider.ExecOpts{
		Stdin: bytes.NewReader(tok.Kubeconfig), Stdout: io.Discard, Stderr: &stderr,
	}); err != nil {
		// Fail before the command runs: a session without the token it asked
		// for fails later, naming nothing dev did.
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

// reportExpiry says when the token stops working, and warns when the cluster
// issued less than was asked for — it does that without refusing, so this is
// the only place the operator finds out before kubectl says Unauthorized.
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

// withEnv appends v, dropping any earlier entry of the same key. Last, so the
// flag on this command wins over a workspace setting of the same name: it is
// the more specific request.
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
