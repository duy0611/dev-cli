package review

import (
	"context"
	"os"
	"os/exec"
)

// OpenEditor runs $VISUAL, else $EDITOR, else vi on path, attached to the
// terminal. The command goes through a shell so an editor given with arguments
// ("code --wait") works, and the path always arrives as one argument.
func OpenEditor(ctx context.Context, path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", editor+` "$@"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
