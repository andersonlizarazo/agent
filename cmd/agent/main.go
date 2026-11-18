package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"code.andersonlizarazo.com/agent/config"
	"code.andersonlizarazo.com/agent/internal/agentictools"
)

func main() {
	if err := do(os.Args, os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func do(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	flagSet := flag.NewFlagSet(filepath.Base(args[0]), flag.ContinueOnError)
	flagSet.SetOutput(stderr)

	var debugFlag, primeFlag string

	flagSet.StringVar(&debugFlag, "debug", "", "print loaded configuration and exit")
	flagSet.StringVar(&primeFlag, "prime", "", "prepend a 'prime' prompt in the first message")

	err := flagSet.Parse(args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

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

	var systemPrompt string
	if systemPromptDir := configDefault.Instruction.SystemPromptDir; systemPromptDir != "" {
		systemPrompt, err = makePrompt(os.DirFS(systemPromptDir).(fs.ReadDirFS))
		if err != nil {
			return err
		}
	}

	var primePrompt string
	if primeFlag != "" {
		primePrompt, err = makePrompt(os.DirFS(filepath.Join(configDefault.Instruction.PrimePromptsDir, primeFlag)).(fs.ReadDirFS))
		if err != nil {
			return err
		}
	}

	switch debugFlag {
	case "":
		// Don't print debug info.
	case "list":
		fmt.Fprintln(stdout, strings.Join([]string{
			"list",
			"system-prompt",
		}, "\t"))
		return nil
	case "system-prompt":
		fmt.Fprint(stdout, systemPrompt)
		return nil
	default:
		return fmt.Errorf("unknown debug flag: %q; use 'list' to show the defined flags", debugFlag)
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

	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "~"
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	agent := fantasy.NewAgent(
		model, fantasy.WithProviderOptions(providerOptions),
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(
			agentictools.MakeCommandTool(
				agentictools.WithWorkingDirectory(cwd),
				agentictools.WithHomeDirectory(homeDir),
				agentictools.WithWrapper(configDefault.CommandLine.Wrapper),
				agentictools.WithTimeout(time.Duration(configDefault.Guardrail.ShellTimeout)*time.Millisecond),
				agentictools.WithMaxOutputBytes(configDefault.Guardrail.MaxShellOutputBytes),
			),
		),
	)

	isFirstPrompt := true
	messages := make([]fantasy.Message, 0, 16)
	scanner := bufio.NewScanner(stdin)
	for {
		fmt.Fprint(stdout, "-> USER: ")
		if !scanner.Scan() {
			break
		}

		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "" {
			continue
		}

		if isFirstPrompt {
			prompt = primePrompt + prompt
			isFirstPrompt = false
		}

		messages = append(messages, fantasy.NewUserMessage(prompt))

		streamCall := fantasy.AgentStreamCall{
			Messages: messages,

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
				payload, err := agentictools.ExtractCommandPayload(toolCall)
				if err != nil {
					return err
				}

				fmt.Fprintf(stdout, "-> COMMAND: %s\n", strings.Join(payload, " "))

				return nil
			},
			OnToolResult: func(res fantasy.ToolResultContent) error {
				// TODO: handle fantasy.ToolResultOutputContentError as well.
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
		for _, step := range result.Steps {
			messages = append(messages, step.Messages...)
		}

		fmt.Fprint(stdout, "\n")
	}

	return scanner.Err()
}

func makePrompt(directory fs.ReadDirFS) (string, error) {
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
