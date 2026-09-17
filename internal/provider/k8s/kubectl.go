package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
)

const kubectlBin = "kubectl"

// kubectl runs commands against one context and namespace.
//
// Every call goes through args(), which prepends both. Done by construction
// rather than by remembering: a call that reaches the wrong cluster does not
// fail, it succeeds somewhere else.
type kubectl struct {
	context   string
	namespace string
}

func newKubectl(c Config) kubectl {
	return kubectl{context: c.Context, namespace: c.Namespace}
}

func (k kubectl) args(extra ...string) []string {
	args := make([]string, 0, 4+len(extra))
	// An empty context means "whatever kubeconfig has selected", which is
	// allowed; passing --context "" would instead select a context named "".
	if k.context != "" {
		args = append(args, "--context", k.context)
	}
	if k.namespace != "" {
		args = append(args, "--namespace", k.namespace)
	}
	return append(args, extra...)
}

// run executes a command that has nothing to say on success.
func (k kubectl) run(ctx context.Context, extra ...string) error {
	if err := requireBinary(kubectlBin); err != nil {
		return err
	}
	args := k.args(extra...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return kubectlError(args, stderr.String(), err)
	}
	return nil
}

// output executes a command and returns its stdout.
func (k kubectl) output(ctx context.Context, extra ...string) (string, error) {
	if err := requireBinary(kubectlBin); err != nil {
		return "", err
	}
	args := k.args(extra...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return "", kubectlError(args, stderr.String(), err)
	}
	return string(out), nil
}

// streamOpts wires a command to real streams, for exec and logs.
type streamOpts struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// stream executes a command with the caller's streams attached, and returns the
// command's own exit error unwrapped, so `dev container exec … -- false` exits
// the way the inner command did.
func (k kubectl) stream(ctx context.Context, opts streamOpts, extra ...string) error {
	if err := requireBinary(kubectlBin); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, kubectlBin, k.args(extra...)...)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

// apply sends a manifest to the cluster on stdin.
//
// Server-side apply, so that a field this tool does not set — a mutating
// webhook's sidecar, a defaulted resource limit — is left alone on update
// rather than being stripped.
func (k kubectl) apply(ctx context.Context, manifest []byte) error {
	if err := requireBinary(kubectlBin); err != nil {
		return err
	}
	args := k.args("apply", "--server-side", "--force-conflicts", "-f", "-")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stdin = bytes.NewReader(manifest)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return kubectlError(args, stderr.String(), err)
	}
	return nil
}

// deleteIgnoringMissing removes an object, treating "already gone" as success:
// the caller's goal is that it not exist.
func (k kubectl) deleteIgnoringMissing(ctx context.Context, kind, name string) error {
	return k.run(ctx, "delete", kind, name, "--ignore-not-found")
}

// pipeInto runs a command with stdin attached to a producer, used to stream a
// tar into the pod. The producer runs while kubectl reads, and its error takes
// precedence: a failed tar leaves kubectl reporting a truncated archive, which
// says nothing about the real cause.
func (k kubectl) pipeInto(ctx context.Context, produce func(io.Writer) error, extra ...string) error {
	if err := requireBinary(kubectlBin); err != nil {
		return err
	}
	args := k.args(extra...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	// StdinPipe, not an io.Pipe handed to cmd.Stdin. os/exec drains an
	// io.Reader from a goroutine of its own, and that goroutine gives up the
	// moment the command exits — leaving the producer blocked on a pipe with no
	// reader, forever. A real pipe fails the write with EPIPE instead, which is
	// what a pod that died mid-sync should look like.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return kubectlError(args, stderr.String(), err)
	}

	produceErr := produce(stdin)
	// Closing is what tells kubectl the stream ended; without it the command
	// waits forever.
	closeErr := stdin.Close()
	runErr := cmd.Wait()

	// EPIPE is the one producer error that is not the cause of anything: it
	// means kubectl had already stopped reading, so whatever made it stop is
	// the thing worth reporting.
	if produceErr != nil && !errors.Is(produceErr, syscall.EPIPE) {
		return produceErr
	}
	if runErr != nil {
		return kubectlError(args, stderr.String(), runErr)
	}
	if produceErr != nil {
		// Exited cleanly having taken none of the archive. Returning nil would
		// claim a copy that never landed.
		return fmt.Errorf("%s exited before the archive was sent: %s",
			kubectlBin, lastLine(stderr.String()))
	}
	return closeErr
}

// kubectlError quotes kubectl's own stderr, which is where it explains a
// refusal; without it the operator gets "exit status 1".
func kubectlError(args []string, stderr string, err error) error {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return fmt.Errorf("kubectl %s: %s", strings.Join(args, " "), msg)
	}
	return fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
}

func requireBinary(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s is required but not on PATH", name)
	}
	return nil
}
