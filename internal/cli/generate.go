package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"git.supermetrics.com/duy-nguyen/devcontainer-claude-setup/internal/dcgen"
)

// parseToolList splits a --tools value. Empty entries are dropped rather than
// rejected, so a trailing comma is not a usage error.
func parseToolList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// generatedConfigFor decides whether to generate a configuration and renders it.
//
// Three paths, matching `provider configure`: told to, so do it; not told but
// at a terminal, so ask; neither, so return nothing and let the caller report
// the missing configuration. The scripted run must never block on a question
// nobody will see.
func generatedConfigFor(name string, generate bool, tools []string, in *os.File, out io.Writer) (string, error) {
	if !generate {
		if !isTerminal(in) {
			return "", nil // the caller reports the missing configuration
		}
		if !confirm(in, out, "generate a base Ubuntu devcontainer?") {
			return "", nil
		}
		picked, err := pickTools(catalogItems(dcgen.Catalog()))
		if err != nil {
			if errors.Is(err, errPickCancelled) {
				// Nothing is written: a cancelled question is not an answer.
				return "", errors.New("cancelled")
			}
			return "", err
		}
		tools = picked
	}

	resolved, err := dcgen.Resolve(tools)
	if err != nil {
		return "", usageError(err)
	}
	// Say what was added on the operator's behalf. Silently installing a tool
	// they did not ask for is the kind of surprise that gets noticed months
	// later, in an image nobody can explain.
	for _, id := range resolved {
		if !slices.Contains(tools, id) {
			fmt.Fprintf(out, "adding %s, which the selection requires\n", id)
		}
	}

	config, err := dcgen.Render(name, resolved)
	if err != nil {
		return "", usageError(err)
	}
	return config, nil
}
