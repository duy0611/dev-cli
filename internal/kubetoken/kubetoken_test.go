package kubetoken

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// stubKubectl replaces PATH with a directory holding one kubectl script.
//
// Replaces, not precedes, for the reason the k8s harness gives: a stub PATH
// that still ends in the host's lets every call the stub does not expect fall
// through to the operator's real kubectl — and a real cluster.
//
// The body is a shell case over "$*", and every call is appended to a log so a
// test can assert what was asked.
func stubKubectl(t *testing.T, body string) (log string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	log = filepath.Join(dir, "kubectl.log")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n" +
		body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestContexts(t *testing.T) {
	stubKubectl(t, `case "$*" in
"config get-contexts -o name") printf 'kind-dev\nprod-eu\nstaging\n' ;;
"config current-context") printf 'prod-eu\n' ;;
*) exit 9 ;;
esac`)

	names, current, err := Contexts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "kind-dev,prod-eu,staging" || current != "prod-eu" {
		t.Errorf("Contexts = %v, %q", names, current)
	}
}

// A kubeconfig with no current context is ordinary; the list is still usable.
func TestContextsWithoutCurrent(t *testing.T) {
	stubKubectl(t, `case "$*" in
"config get-contexts -o name") printf 'a\n' ;;
*) echo 'error: current-context is not set' >&2; exit 1 ;;
esac`)

	names, current, err := Contexts(context.Background())
	if err != nil || len(names) != 1 || current != "" {
		t.Errorf("Contexts = %v, %q, %v", names, current, err)
	}
}

func TestNamespacesCarryTheContext(t *testing.T) {
	log := stubKubectl(t, `case "$*" in
"--context prod-eu get namespaces -o name") printf 'namespace/default\nnamespace/team-a\n' ;;
"config view --minify --context prod-eu -o jsonpath={..namespace}") printf 'team-a' ;;
*) exit 9 ;;
esac`)

	names, def, err := Namespaces(context.Background(), "prod-eu")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "default,team-a" || def != "team-a" {
		t.Errorf("Namespaces = %v, %q", names, def)
	}
	for _, c := range calls(t, log) {
		if !strings.Contains(c, "--context prod-eu") {
			t.Errorf("call without the context: %q", c)
		}
	}
}

func TestServiceAccounts(t *testing.T) {
	stubKubectl(t, `case "$*" in
"--context prod-eu --namespace team-a get serviceaccounts -o name")
  printf 'serviceaccount/default\nserviceaccount/reader\n' ;;
*) exit 9 ;;
esac`)

	names, err := ServiceAccounts(context.Background(), "prod-eu", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "default,reader" {
		t.Errorf("ServiceAccounts = %v", names)
	}
}

// A listing kubectl refuses is an error the caller can fall back from, and it
// names what kubectl said.
func TestListingForbidden(t *testing.T) {
	stubKubectl(t, `echo 'Error from server (Forbidden): serviceaccounts is forbidden' >&2; exit 1`)

	_, err := ServiceAccounts(context.Background(), "prod-eu", "team-a")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("err = %v", err)
	}
}

func TestNoKubectl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, err := Contexts(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "kubectl") {
		t.Errorf("err = %v", err)
	}
}

// fakeJWT builds a token with the given claims segment. The signature is junk:
// nothing here verifies it, and nothing should.
func fakeJWT(claims string) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." +
		enc.EncodeToString([]byte(claims)) + ".c2ln"
}

// The host view carries the operator's own credentials; none of them may reach
// the container.
const hostView = `{
  "clusters": [{"name": "prod", "cluster": {
    "server": "https://prod.example:6443",
    "certificate-authority-data": "Q0FEQVRB"}}],
  "users": [{"name": "me", "user": {
    "client-certificate-data": "SE9TVENFUlQ=",
    "client-key-data": "SE9TVEtFWQ==",
    "exec": {"command": "aws"}}}],
  "contexts": [{"name": "prod-eu", "context": {"cluster": "prod", "user": "me"}}],
  "current-context": "prod-eu"
}`

func mintStub(t *testing.T, token string) string {
	t.Helper()
	return stubKubectl(t, `case "$*" in
"--context prod-eu --namespace team-a create token reader"*) printf '%s\n' '`+token+`' ;;
"--context prod-eu config view --minify --flatten -o json") /bin/cat <<'EOF'
`+hostView+`
EOF
;;
*) exit 9 ;;
esac`)
}

func TestMint(t *testing.T) {
	exp := time.Date(2026, 9, 28, 19, 42, 0, 0, time.UTC)
	token := fakeJWT(`{"exp":` + strconv.FormatInt(exp.Unix(), 10) + `}`)
	log := mintStub(t, token)

	got, err := Mint(context.Background(), Request{
		Context: "prod-eu", Namespace: "team-a", ServiceAccount: "reader", Duration: 4 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Expiry.Equal(exp) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, exp)
	}
	if !strings.Contains(strings.Join(calls(t, log), "\n"), "create token reader --duration 4h0m0s") {
		t.Errorf("duration not passed: %v", calls(t, log))
	}

	var kc struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			User map[string]any `yaml:"user"`
		} `yaml:"users"`
		Contexts []struct {
			Context map[string]any `yaml:"context"`
		} `yaml:"contexts"`
		Current string `yaml:"current-context"`
	}
	if err := yaml.Unmarshal(got.Kubeconfig, &kc); err != nil {
		t.Fatalf("kubeconfig does not parse: %v\n%s", err, got.Kubeconfig)
	}
	if len(kc.Clusters) != 1 || len(kc.Users) != 1 || len(kc.Contexts) != 1 {
		t.Fatalf("want one of each:\n%s", got.Kubeconfig)
	}
	cl := kc.Clusters[0].Cluster
	if cl["server"] != "https://prod.example:6443" || cl["certificate-authority-data"] != "Q0FEQVRB" {
		t.Errorf("cluster = %v", cl)
	}
	if u := kc.Users[0].User; len(u) != 1 || u["token"] != token {
		t.Errorf("user = %v, want the token and nothing else", u)
	}
	if kc.Contexts[0].Context["namespace"] != "team-a" || kc.Current == "" {
		t.Errorf("context = %v, current %q", kc.Contexts[0].Context, kc.Current)
	}
	for _, leak := range []string{"SE9TVENFUlQ=", "SE9TVEtFWQ==", "aws"} {
		if strings.Contains(string(got.Kubeconfig), leak) {
			t.Errorf("host credential %q reached the kubeconfig", leak)
		}
	}
}

// An empty duration is kubectl's default, which only holds if dev passes none.
func TestMintDefaultDuration(t *testing.T) {
	log := mintStub(t, fakeJWT(`{"exp":1}`))

	if _, err := Mint(context.Background(), Request{
		Context: "prod-eu", Namespace: "team-a", ServiceAccount: "reader",
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls(t, log), "\n"), "--duration") {
		t.Errorf("--duration passed with none asked: %v", calls(t, log))
	}
}

func TestMintFailureNamesTheTarget(t *testing.T) {
	stubKubectl(t, `echo 'error: serviceaccounts "reader" not found' >&2; exit 1`)

	_, err := Mint(context.Background(), Request{
		Context: "prod-eu", Namespace: "team-a", ServiceAccount: "reader",
	})
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"prod-eu", "team-a", "reader", "not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestExpiry(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  int64 // 0: unreadable
	}{
		{"jwt", fakeJWT(`{"exp":1790000000,"sub":"x"}`), 1790000000},
		{"no exp", fakeJWT(`{"sub":"x"}`), 0},
		{"not a jwt", "opaque-token", 0},
		{"bad base64", "a.!!!.c", 0},
	}
	for _, c := range cases {
		got := expiry(c.token)
		if c.want == 0 && !got.IsZero() || c.want != 0 && got.Unix() != c.want {
			t.Errorf("%s: expiry = %v", c.name, got)
		}
	}
}

func TestShortened(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		asked, issued time.Duration
		want          bool
	}{
		{4 * time.Hour, time.Hour, true},
		{4 * time.Hour, 4*time.Hour - 30*time.Second, false}, // clock and latency
		{4 * time.Hour, 4 * time.Hour, false},
		{0, time.Hour, false}, // nothing asked, nothing to fall short of
	}
	for _, c := range cases {
		tok := Token{Expiry: now.Add(c.issued)}
		if got := tok.Shortened(c.asked, now); got != c.want {
			t.Errorf("asked %v issued %v: Shortened = %v", c.asked, c.issued, got)
		}
	}
	if (Token{}).Shortened(time.Hour, now) {
		t.Error("an unknown expiry reported as shortened")
	}
}
