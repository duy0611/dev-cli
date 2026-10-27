package team

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

// installScript is fixed text: the target path is passed to the shell as an
// argument, so a path with a space, quote or $(...) is data rather than script.
const installScript = `mkdir -p "$(dirname "$1")" && cat > "$1.part" && chmod 700 "$1.part" && mv "$1.part" "$1"`

// Install copies the dispatch binary to path inside the container, creating the
// parent directory first. It always copies: the path is fixed rather than named
// by content, so an existing file says nothing about which build it holds.
func Install(ctx context.Context, run func(ctx context.Context, argv []string, stdin io.Reader) error,
	path string, bin []byte) error {
	// Moved into place from a temporary name, so a copy that dies midway leaves
	// nothing executable at the path dispatch will use.
	if err := run(ctx, []string{"sh", "-c", installScript, "sh", path}, bytes.NewReader(bin)); err != nil {
		return fmt.Errorf("installing the team dispatch binary into the container: %w", err)
	}
	return nil
}
