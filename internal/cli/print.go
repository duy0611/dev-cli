package cli

import (
	"fmt"
	"io"
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
