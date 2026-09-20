package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// header and row write one tab-separated line each, for a tabwriter to align.
// Kept as functions rather than format strings so a column added to the header
// and forgotten in the rows is visible at the call site.

func header(w io.Writer, cells ...string) {
	row(w, cells...)
}

func row(w io.Writer, cells ...string) {
	fmt.Fprintln(w, strings.Join(cells, "\t"))
}

func itoa(n int) string { return strconv.Itoa(n) }

// onOff renders a flag for a human reading a status line.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// warnf reports something the operator should know about but that did not stop
// the command. Stderr, so it stays out of a piped list.
func warnf(a *app, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dev: "+format+"\n", args...)
}
