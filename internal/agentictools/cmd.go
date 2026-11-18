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

// WithFn customizes the configuration of a tool at construction time.
type WithFn func(*commandToolConfig)

// commandToolConfig holds every knob the command tool reads; construct it
// through [WithFn] options so no call site repeats positional arguments.
type commandToolConfig struct {
	workingDirectory string
	homeDirectory    string
	wrapper          []string
	timeout          time.Duration
	maxOutputBytes   uint32
}

// WithWorkingDirectory sets the directory the command runs in.
func WithWorkingDirectory(path string) WithFn {
	return func(c *commandToolConfig) {
		c.workingDirectory = path
	}
}

// WithHomeDirectory sets the directory displayed as `~` in the description.
func WithHomeDirectory(path string) WithFn {
	return func(c *commandToolConfig) {
		c.homeDirectory = path
	}
}

// WithWrapper sets the program that wraps every command, such as `bwrap`.
func WithWrapper(wrapper []string) WithFn {
	return func(c *commandToolConfig) {
		c.wrapper = wrapper
	}
}

// WithTimeout bounds each command; zero waits forever.
func WithTimeout(timeout time.Duration) WithFn {
	return func(c *commandToolConfig) {
		c.timeout = timeout
	}
}

// WithMaxOutputBytes caps each stream; zero keeps everything.
func WithMaxOutputBytes(limit uint32) WithFn {
	return func(c *commandToolConfig) {
		c.maxOutputBytes = limit
	}
}

func MakeCommandTool(options ...WithFn) fantasy.AgentTool {
	config := commandToolConfig{}
	for _, apply := range options {
		apply(&config)
	}

	return fantasy.NewAgentTool("command", fmt.Sprintf("execute the given command on %q", cleanPath(config.workingDirectory, config.homeDirectory)), makeCommandInfo(config))
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

func makeCommandInfo(config commandToolConfig) func(context.Context, commandQuery, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return func(ctx context.Context, query commandQuery, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		if len(query.Payload) == 0 {
			return fantasy.NewTextResponse(makeCommandOutput("", false, "", false, 0, "payload must name a program to run")), nil
		}

		var cancel context.CancelFunc
		if t := config.timeout; t != 0 {
			ctx, cancel = context.WithTimeout(ctx, t)
			defer cancel()
		}

		outputLimit := commandline.NoOutputLimit
		if limit := config.maxOutputBytes; limit != 0 {
			outputLimit = int64(limit)
		}

		var extraInfo string
		var exitCode int
		cmdStdout, cmdStdoutSize, cmdStderr, cmdStderrSize, err := commandline.Execute(
			ctx, query.Payload,
			commandline.WithWrapper(config.wrapper),
			commandline.WithOutputLimit(outputLimit),
			commandline.WithWorkingDirectory(config.workingDirectory))
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
