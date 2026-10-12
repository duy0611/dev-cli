// Package kubetoken mints a short-lived ServiceAccount token on the host and
// wraps it in a kubeconfig a container can use.
//
// Every call names its context explicitly, since a fallback to the current
// context would succeed silently against the wrong cluster.
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
	// Duration is the lifetime asked for. Zero leaves the server's default.
	Duration time.Duration
}

// Token is a minted token, already wrapped for the container.
type Token struct {
	// Kubeconfig holds a credential and must never be logged.
	Kubeconfig []byte
	// Expiry is read from the token's exp claim; zero when unreadable.
	Expiry time.Time
}

// shortfall is the tolerance for clock skew and latency before a token counts
// as shortened.
const shortfall = time.Minute

// Shortened reports whether the cluster issued less than was asked for.
//
// The server issues a shorter token rather than refusing one asked for too
// long.
func (t Token) Shortened(asked time.Duration, now time.Time) bool {
	if asked == 0 || t.Expiry.IsZero() {
		return false
	}
	return t.Expiry.Sub(now) < asked-shortfall
}

// Contexts lists the host kubeconfig's contexts and reports the current one.
//
// No current context is not an error.
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
	// --minify with --context yields that context's namespace.
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

	// --flatten inlines CA data; a CA path would name a host file.
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

// clusterOf picks the one cluster entry from a minified view. The user is
// dropped so the operator's own credentials never reach the container.
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

// name labels the cluster, user and context in the generated kubeconfig.
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

// expiry reads a JWT's exp claim, zero when there is none. The signature is not
// checked: that is the API server's job.
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
