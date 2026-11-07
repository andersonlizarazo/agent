package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"code.andersonlizarazo.com/agent/config"
)

func main() {
	if err := do(os.Args, os.Getenv, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func do(_ []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	configDir, err := config.Directory(getenv)
	if err != nil {
		return err
	}

	configDefaultData, err := os.ReadFile(filepath.Join(configDir, config.DefaultFile))
	if err != nil {
		return err
	}
	configPrivateData, err := os.ReadFile(filepath.Join(configDir, config.DefaultPrivateFile))
	if err != nil {
		return err
	}

	configDefault, configPrivate, err := config.LoadConfig(configDefaultData, configPrivateData)
	if err != nil {
		return err
	}

	provider, err := openaicompat.New(openaicompat.WithBaseURL(configPrivate.OpenAI.BaseURL), openaicompat.WithAPIKey(configPrivate.OpenAI.APIKey))
	if err != nil {
		return err
	}
	providerOptions := openaicompat.NewProviderOptions(&openaicompat.ProviderOptions{
		ReasoningEffort: new(configDefault.OpenAI.ReasoningEffort),
	})

	ctx := context.Background()

	model, err := provider.LanguageModel(ctx, configPrivate.OpenAI.LanguageModel)
	if err != nil {
		return err
	}

	type shellQuery struct {
		Cmd string `json:"cmd" description:"The shell command to execute."`
	}
	shellInfo := func(ctx context.Context, input shellQuery, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		var cancel context.CancelFunc
		if t := configDefault.Guardrail.ShellTimeout; t != 0 {
			ctx, cancel = context.WithTimeout(ctx, time.Millisecond*time.Duration(t))
			defer cancel()
		}

		data, err := exec.CommandContext(ctx, "sh", "-c", input.Cmd).CombinedOutput()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("command took too much time executing: %w", ctxErr)
			}

			return fantasy.ToolResponse{}, err
		}

		if b := configDefault.Guardrail.MaxShellOutputBytes; b != 0 {
			if size := len(data); size > int(b) {
				return fantasy.ToolResponse{}, fmt.Errorf("output is too large: %d bytes", size)
			}
		}

		return fantasy.NewTextResponse(string(data)), nil
	}
	shellTool := fantasy.NewAgentTool("shell", "Execute shell commands.", shellInfo)

	var systemPrompt string
	if systemPromptDir := configDefault.Instruction.SystemPromptDir; systemPromptDir != "" {
		systemPrompt, err = makeSystemPrompt(os.DirFS(systemPromptDir).(fs.ReadDirFS))
		if err != nil {
			return err
		}
	}

	agent := fantasy.NewAgent(
		model, fantasy.WithProviderOptions(providerOptions), fantasy.WithSystemPrompt(systemPrompt), fantasy.WithTools(shellTool),
	)

	var messages []fantasy.Message
	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "-> USER: ")
		if !scanner.Scan() {
			break
		}

		prompt := scanner.Text()
		streamCall := fantasy.AgentStreamCall{
			Prompt: prompt, Messages: messages,

			OnTextStart: func(id string) error {
				fmt.Fprint(stdout, "-> AGENT: ")
				return nil
			},
			OnTextDelta: func(id, text string) error {
				fmt.Fprint(stdout, text)
				return nil
			},
			OnReasoningStart: func(id string, reasoning fantasy.ReasoningContent) error {
				fmt.Fprint(stdout, "-> DRAFT: ")
				return nil
			},
			OnReasoningDelta: func(id, text string) error {
				fmt.Fprint(stdout, text)
				return nil
			},
			OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
				fmt.Fprint(stdout, "\n")
				return nil
			},
			OnToolCall: func(toolCall fantasy.ToolCallContent) error {
				var query shellQuery
				if err := json.Unmarshal([]byte(toolCall.Input), &query); err != nil {
					return err
				}

				fmt.Fprintf(stdout, "-> SHELL: %s\n", query.Cmd)
				return nil
			},
			OnToolResult: func(res fantasy.ToolResultContent) error {
				text, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](res.Result)
				if !ok {
					return fmt.Errorf("unable to retrieve tool call result")
				}
				fmt.Fprintln(stdout, strings.TrimSuffix(text.Text, "\n"))

				return nil
			},
		}
		result, err := agent.Stream(ctx, streamCall)
		if err != nil {
			return err
		}
		messages = result.Steps[len(result.Steps)-1].Messages

		fmt.Fprint(stdout, "\n")
	}

	return scanner.Err()
}

func makeSystemPrompt(directory fs.ReadDirFS) (string, error) {
	entries, err := directory.ReadDir(".")
	if err != nil {
		return "", err
	}

	var prompt strings.Builder
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		file, err := directory.Open(entry.Name())
		if err != nil {
			return "", err
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			return "", err
		}

		prompt.WriteString(string(data))
	}

	return prompt.String(), nil
}
