package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
)

const (
	ENV_BASE                  = "AGENT_"
	ENV_OPENAI                = ENV_BASE + "OPENAI_"
	ENV_OPENAI_BASE_URL       = ENV_OPENAI + "BASE_URL"
	ENV_OPENAI_API_KEY        = ENV_OPENAI + "API_KEY"
	ENV_OPENAI_LANGUAGE_MODEL = ENV_OPENAI + "LANGUAGE_MODEL"
)

func main() {
	if err := do(os.Args, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func do(args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	provider, err := openaicompat.New(openaicompat.WithBaseURL(getenv(ENV_OPENAI_BASE_URL)), openaicompat.WithAPIKey(getenv(ENV_OPENAI_API_KEY)))
	if err != nil {
		return err
	}

	ctx := context.Background()

	model, err := provider.LanguageModel(ctx, getenv(ENV_OPENAI_LANGUAGE_MODEL))
	if err != nil {
		return err
	}

	type shellQuery struct {
		Cmd string `json:"cmd" description:"The shell command to execute."`
	}

	shellInfo := func(ctx context.Context, input shellQuery, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		fmt.Fprintf(stdout, "SHELL -> %s\n", input.Cmd)
		data, err := exec.Command("sh", "-c", input.Cmd).CombinedOutput()
		if err != nil {
			return fantasy.ToolResponse{}, err
		}

		out := string(data)
		if !strings.HasSuffix(out, "\n") {
			out += "\n"
		}

		fmt.Fprintf(stdout, "%s<- END OUTPUT\n", out)
		return fantasy.NewTextResponse(out), nil
	}

	shellTool := fantasy.NewAgentTool("shell", "Execute shell commands.", shellInfo)

	agent := fantasy.NewAgent(
		model, fantasy.WithTools(shellTool),
	)

	var messages []fantasy.Message
	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "USER -> ")
		if !scanner.Scan() {
			break
		}

		prompt := scanner.Text()
		result, err := agent.Generate(ctx, fantasy.AgentCall{Prompt: prompt, Messages: messages})
		if err != nil {
			return err
		}
		messages = result.Steps[len(result.Steps)-1].Messages

		fmt.Fprintf(stdout, "AGENT -> %s\n", result.Response.Content.Text())
	}

	return scanner.Err()
}
