package team

import "fmt"

// Worker returns the command and stream parser for a worker tool. Both tools
// read the task text from stdin, so no argv element ever carries it.
func Worker(tool string, w WorkerSpec) (WorkerCmd, Parser, error) {
	switch tool {
	case "claude":
		cmd, parser := claudeWorker(w)
		return cmd, parser, nil
	case "opencode":
		cmd, parser := opencodeWorker(w)
		return cmd, parser, nil
	}
	return WorkerCmd{}, nil, fmt.Errorf("unsupported worker tool %q: only claude and opencode are implemented", tool)
}
