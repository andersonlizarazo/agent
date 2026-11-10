package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"code.andersonlizarazo.com/agent/config"
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

	type shellQuery struct {
		Cmd string `json:"cmd" description:"The shell command to execute."`
	}
	shellInfo := func(ctx context.Context, input shellQuery, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		var cancel context.CancelFunc
		if t := configDefault.Guardrail.ShellTimeout; t != 0 {
			ctx, cancel = context.WithTimeout(ctx, time.Millisecond*time.Duration(t))
			defer cancel()
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", input.Cmd)

		cmdOut, err := cmd.StdoutPipe()
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		cmdErr, err := cmd.StderrPipe()
		if err != nil {
			return fantasy.ToolResponse{}, err
		}

		if err := cmd.Start(); err != nil {
			return fantasy.ToolResponse{}, err
		}

		outputBudget := int64(configDefault.Guardrail.MaxShellOutputBytes)
		if outputBudget == 0 {
			// outputBudget = math.MaxInt64
			outputBudget = 1024 * 1024 * 128 // TODO: Stream instead of reserving buffers.
		}

		dataOut := make([]byte, outputBudget)
		dataErr := make([]byte, outputBudget)
		var nOut, nErr int
		var errOut, errErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			nOut, errOut = drainCommandOutput(cmdOut, dataOut)
		})
		wg.Go(func() {
			nErr, errErr = drainCommandOutput(cmdErr, dataErr)
		})
		wg.Wait()

		outputBudget -= int64(nOut)
		output := string(dataOut[:nOut])
		if errOut != nil {
			output += addNewLine(output) + "[harness: cmd-error]: " + errOut.Error()
		}

		nErr = min(int(outputBudget), nErr)

		if nErr != 0 && len(bytes.TrimSpace(dataErr[:nErr])) != 0 {
			output = addNewLine(output) + "[harness: stderr]\n" + string(dataErr[:nErr])
		}
		if errErr != nil {
			output = addNewLine(output) + "[harness: cmd-error]: " + errErr.Error()
		}

		if outputBudget == 0 {
			output = addNewLine(output) + "[harness: tool output too large]"
		}

		if err := cmd.Wait(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("%s\n[harness: command took too much time executing: %w]", output, ctxErr)
			}

			return fantasy.ToolResponse{}, fmt.Errorf("%s\n[%w]", output, err)
		}

		return fantasy.NewTextResponse(output), nil
	}
	shellTool := fantasy.NewAgentTool("shell", "Execute shell commands.", shellInfo)

	agent := fantasy.NewAgent(
		model, fantasy.WithProviderOptions(providerOptions), fantasy.WithSystemPrompt(systemPrompt), fantasy.WithTools(shellTool),
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

func addNewLine(s string) string {
	return strings.TrimSuffix(s, "\n") + "\n"
}

func drainCommandOutput(r io.Reader, buf []byte) (n int, err error) {
	n, err = io.ReadFull(r, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return n, nil
	}
	if err != nil {
		return n, err
	}

	if _, err = io.ReadAll(r); err != nil {
		fmt.Fprintln(os.Stderr, "ignored error:", err)
	}

	return n, nil
}
