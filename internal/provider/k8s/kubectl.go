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
type kubectl struct {
	context   string
	namespace string
}

func newKubectl(c Config) kubectl {
	return kubectl{context: c.Context, namespace: c.Namespace}
}

// args prepends both: a call on the wrong cluster does not fail, it succeeds
// elsewhere.
func (k kubectl) args(extra ...string) []string {
	args := make([]string, 0, 4+len(extra))
	// Empty means the kubeconfig's selection, since --context "" would name a
	// context "".
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

// stream attaches the caller's streams and returns the exit error unwrapped, so
// exec exits with the inner command's code.
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

// apply sends a manifest on stdin. Server-side, so a field this tool does not
// set, such as a webhook's sidecar, survives an update.
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

// deleteIgnoringMissing treats an object already gone as success.
func (k kubectl) deleteIgnoringMissing(ctx context.Context, kind, name string) error {
	return k.run(ctx, "delete", kind, name, "--ignore-not-found")
}

// pipeInto streams produce's output into kubectl's stdin. The producer's error
// takes precedence: a failed tar makes kubectl report a truncated archive, not
// the real cause.
func (k kubectl) pipeInto(ctx context.Context, produce func(io.Writer) error, extra ...string) error {
	if err := requireBinary(kubectlBin); err != nil {
		return err
	}
	args := k.args(extra...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	// StdinPipe, not an io.Reader on cmd.Stdin: os/exec abandons a plain
	// reader's goroutine when the command exits, leaving the producer blocked
	// forever. A real pipe fails with EPIPE.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return kubectlError(args, stderr.String(), err)
	}

	produceErr := produce(stdin)
	// Closing ends the stream; kubectl waits forever without it.
	closeErr := stdin.Close()
	runErr := cmd.Wait()

	// EPIPE means kubectl stopped reading first, so its error is the one worth
	// reporting.
	if produceErr != nil && !errors.Is(produceErr, syscall.EPIPE) {
		return produceErr
	}
	if runErr != nil {
		return kubectlError(args, stderr.String(), runErr)
	}
	if produceErr != nil {
		// Exit 0 with none of the archive taken is still a failed copy.
		return fmt.Errorf("%s exited before the archive was sent: %s",
			kubectlBin, lastLine(stderr.String()))
	}
	return closeErr
}

// kubectlError quotes kubectl's stderr, where it explains a refusal, rather
// than 'exit status 1'.
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
