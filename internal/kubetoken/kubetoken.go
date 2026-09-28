// Package kubetoken mints a short-lived ServiceAccount token on the host and
// wraps it in a kubeconfig a container can use.
//
// It lists what can be picked and mints; asking which to pick is the CLI's job.
// Every call names its context explicitly: a call that falls back on whatever
// kubeconfig has selected does not fail when that is the wrong cluster, it
// succeeds there.
package kubetoken

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const kubectlBin = "kubectl"

// Request says which token to mint.
type Request struct {
	Context        string
	Namespace      string
	ServiceAccount string
	// Duration is the lifetime asked for. Zero passes no --duration, so the
	// API server's own default applies rather than this package's idea of it.
	Duration time.Duration
}

// Token is a minted token, already wrapped for the container.
type Token struct {
	// Kubeconfig is a self-contained document: one cluster, one user carrying
	// the token, one context. It holds a credential and must never be logged.
	Kubeconfig []byte
	// Expiry is when the token stops working, read from the token itself. Zero
	// when it could not be read.
	Expiry time.Time
}

// shortfall is how far short of the request an issued token may fall before
// it counts as shortened. The API server stamps exp from its own clock after
// the request has travelled, so an exact comparison would warn every time.
const shortfall = time.Minute

// Shortened reports whether the cluster issued less than was asked for.
//
// A server whose --service-account-max-token-expiration is lower than the
// request does not refuse it: it quietly issues a shorter token. This is the
// only place that becomes visible before kubectl starts saying Unauthorized.
func (t Token) Shortened(asked time.Duration, now time.Time) bool {
	if asked == 0 || t.Expiry.IsZero() {
		return false
	}
	return t.Expiry.Sub(now) < asked-shortfall
}

// Contexts lists the host kubeconfig's contexts and reports the current one.
//
// No current context is an ordinary state, not an error: the list is still
// what the operator picks from.
func Contexts(ctx context.Context) (names []string, current string, err error) {
	out, err := kubectl(ctx, "config", "get-contexts", "-o", "name")
	if err != nil {
		return nil, "", err
	}
	current, _ = kubectl(ctx, "config", "current-context")
	return lines(out, ""), strings.TrimSpace(current), nil
}

// Namespaces lists the namespaces in a context and reports that context's
// default, which may be empty.
func Namespaces(ctx context.Context, kubeContext string) (names []string, def string, err error) {
	out, err := kubectl(ctx, "--context", kubeContext, "get", "namespaces", "-o", "name")
	if err != nil {
		return nil, "", err
	}
	// --minify with --context restricts the view to that context, so this is
	// its namespace rather than the first one in the file.
	def, _ = kubectl(ctx, "config", "view", "--minify", "--context", kubeContext,
		"-o", "jsonpath={..namespace}")
	return lines(out, "namespace/"), strings.TrimSpace(def), nil
}

// ServiceAccounts lists the service accounts in one namespace.
func ServiceAccounts(ctx context.Context, kubeContext, namespace string) ([]string, error) {
	out, err := kubectl(ctx, "--context", kubeContext, "--namespace", namespace,
		"get", "serviceaccounts", "-o", "name")
	if err != nil {
		return nil, err
	}
	return lines(out, "serviceaccount/"), nil
}

// Mint requests a token for the service account and wraps it in a kubeconfig.
func Mint(ctx context.Context, r Request) (Token, error) {
	args := []string{"--context", r.Context, "--namespace", r.Namespace,
		"create", "token", r.ServiceAccount}
	if r.Duration > 0 {
		args = append(args, "--duration", r.Duration.String())
	}
	out, err := kubectl(ctx, args...)
	if err != nil {
		return Token{}, fmt.Errorf("minting a token for %s/%s on %s: %w",
			r.Namespace, r.ServiceAccount, r.Context, err)
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return Token{}, fmt.Errorf("kubectl returned no token for %s/%s on %s",
			r.Namespace, r.ServiceAccount, r.Context)
	}

	// --flatten inlines the CA data: a certificate-authority path names a file
	// on the host, which does not exist in the container.
	view, err := kubectl(ctx, "--context", r.Context, "config", "view",
		"--minify", "--flatten", "-o", "json")
	if err != nil {
		return Token{}, fmt.Errorf("reading context %s: %w", r.Context, err)
	}
	cluster, err := clusterOf(view)
	if err != nil {
		return Token{}, fmt.Errorf("reading context %s: %w", r.Context, err)
	}

	kc, err := render(cluster, r.Namespace, token)
	if err != nil {
		return Token{}, err
	}
	return Token{Kubeconfig: kc, Expiry: expiry(token)}, nil
}

// clusterOf picks the one cluster entry out of a minified view.
//
// Only the cluster is taken. The view's user carries the operator's own
// credentials — client certificates, an exec plugin — and the whole point of
// the token is that none of those reach the container.
func clusterOf(view string) (map[string]any, error) {
	var v struct {
		Clusters []struct {
			Cluster map[string]any `json:"cluster"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(view), &v); err != nil {
		return nil, fmt.Errorf("parsing kubectl config view: %w", err)
	}
	if len(v.Clusters) != 1 {
		return nil, fmt.Errorf("expected one cluster in the minified view, found %d", len(v.Clusters))
	}
	out := map[string]any{}
	for _, k := range []string{"server", "certificate-authority-data",
		"insecure-skip-tls-verify", "tls-server-name", "proxy-url"} {
		if val, ok := v.Clusters[0].Cluster[k]; ok {
			out[k] = val
		}
	}
	if out["server"] == nil {
		return nil, errors.New("the context's cluster has no server")
	}
	return out, nil
}

// name labels the cluster, user and context in the generated kubeconfig. One
// of each, so there is nothing to tell apart.
const name = "dev"

func render(cluster map[string]any, namespace, token string) ([]byte, error) {
	doc := map[string]any{
		"apiVersion":      "v1",
		"kind":            "Config",
		"clusters":        []any{map[string]any{"name": name, "cluster": cluster}},
		"users":           []any{map[string]any{"name": name, "user": map[string]any{"token": token}}},
		"contexts":        []any{map[string]any{"name": name, "context": map[string]any{"cluster": name, "user": name, "namespace": namespace}}},
		"current-context": name,
	}
	return yaml.Marshal(doc)
}

// expiry reads a JWT's exp claim, zero when there is none to read.
//
// The signature is not checked. This reports what the cluster handed back so
// the operator knows when to refresh; trusting the token is the API server's
// business, not this package's.
func expiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

func kubectl(ctx context.Context, args ...string) (string, error) {
	if _, err := exec.LookPath(kubectlBin); err != nil {
		return "", errors.New("kubectl is not installed on this host; --kube-token mints the token with it")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}

// lines splits kubectl -o name output into plain names.
func lines(out, prefix string) []string {
	var names []string
	for l := range strings.SplitSeq(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, strings.TrimPrefix(l, prefix))
		}
	}
	return names
}
