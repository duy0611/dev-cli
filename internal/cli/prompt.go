package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// prompter asks questions on a terminal. It takes its streams so the logic is
// testable without a pty; callers check isTerminal first.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: bufio.NewReader(in), out: out}
}

// ask reads one answer, returning def when the line is empty.
func (p *prompter) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}

	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		// EOF with nothing typed. Treated as accepting the default rather than
		// as a failure, so a piped answer that omits a trailing newline works.
		if err == io.EOF {
			return def, nil
		}
		return "", err
	}

	answer := strings.TrimSpace(line)
	if answer == "" {
		return def, nil
	}
	return answer, nil
}

// askRequired repeats the question until there is an answer.
func (p *prompter) askRequired(label, def string) (string, error) {
	for {
		answer, err := p.ask(label, def)
		if err != nil {
			return "", err
		}
		if answer != "" {
			return answer, nil
		}
		fmt.Fprintf(p.out, "  %s is required\n", label)
	}
}
