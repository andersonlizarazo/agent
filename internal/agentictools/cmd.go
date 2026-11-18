// Package agentictools provides tool definitions for language model agents.
package agentictools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"charm.land/fantasy"
	"code.andersonlizarazo.com/agent/internal/commandline"
)

func MakeCommandTool(workingDirectory, homeDirectory string, wrapper []string, timeout time.Duration, maxOutputBytes uint32) fantasy.AgentTool {
	return fantasy.NewAgentTool("command", fmt.Sprintf("execute the given command on %q", cleanPath(workingDirectory, homeDirectory)), makeCommandInfo(workingDirectory, wrapper, timeout, maxOutputBytes))
}

func cleanPath(path string, homeDir string) string {
	path = filepath.Clean(path)
	homeDir = filepath.Clean(homeDir)

	if path == homeDir {
		return "~"
	}

	if !strings.HasPrefix(path, homeDir+string(filepath.Separator)) {
		return path
	}

	return filepath.Join("~", strings.TrimPrefix(path, homeDir))
}

func ExtractCommandPayload(toolCall fantasy.ToolCallContent) ([]string, error) {
	var query commandQuery
	if err := json.Unmarshal([]byte(toolCall.Input), &query); err != nil {
		return nil, err
	}

	return query.Payload, nil
}

type commandQuery struct {
	Payload []string `json:"payload" description:"command & args"`
}

func makeCommandInfo(workingDirectory string, wrapper []string, commandTimeout time.Duration, maxCommandOutputBytes uint32) func(context.Context, commandQuery, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, query commandQuery, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		if len(query.Payload) == 0 {
			return fantasy.NewTextResponse(makeCommandOutput("", false, "", false, 0, "payload must name a program to run")), nil
		}

		var cancel context.CancelFunc
		if t := commandTimeout; t != 0 {
			ctx, cancel = context.WithTimeout(ctx, t)
			defer cancel()
		}

		outputLimit := commandline.NoOutputLimit
		if limit := maxCommandOutputBytes; limit != 0 {
			outputLimit = int64(limit)
		}

		var extraInfo string
		var exitCode int
		cmdStdout, cmdStdoutSize, cmdStderr, cmdStderrSize, err := commandline.Execute(
			ctx, query.Payload,
			commandline.WithWrapper(wrapper),
			commandline.WithOutputLimit(outputLimit),
			commandline.WithWorkingDirectory(workingDirectory))
		if ctxErr := ctx.Err(); ctxErr != nil {
			extraInfo = ctxErr.Error()
		} else if err != nil {
			if e, ok := errors.AsType[*exec.ExitError](err); ok {
				if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					extraInfo = fmt.Sprintf("terminated by signal: %s", ws.Signal())
				} else {
					exitCode = e.ExitCode()
				}
			} else {
				extraInfo = err.Error()
			}
		}

		response := makeCommandOutput(cmdStdout, int64(len(cmdStdout)) != cmdStdoutSize, cmdStderr, int64(len(cmdStderr)) != cmdStderrSize, exitCode, extraInfo)

		return fantasy.NewTextResponse(response), nil
	}
}

func makeCommandOutput(stdout string, stdoutTruncated bool, stderr string, stderrTruncated bool, exitCode int, extra string) string {
	const truncatedMsg = "[harness: output was truncated]"

	output := stdout
	truncationReported := false
	if stdoutTruncated {
		output = addNewLine(output) + truncatedMsg
		truncationReported = true
	}

	if len(stderr) != 0 {
		output = addNewLine(output) + "[harness: stderr]\n" + stderr
	}

	if stderrTruncated && !truncationReported {
		output = addNewLine(output) + truncatedMsg
	}

	if exitCode != 0 {
		output = addNewLine(output) + fmt.Sprintf("[harness: exit code %d]", exitCode)
	}

	if len(extra) != 0 {
		output = addNewLine(output) + fmt.Sprintf("[harness: %s]", extra)
	}

	return output
}

// addNewLine appends a newline while avoiding duplicates unless [s] is
// empty.
func addNewLine(s string) string {
	if s == "" {
		return ""
	}

	return strings.TrimSuffix(s, "\n") + "\n"
}
