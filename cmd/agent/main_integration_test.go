package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy/providers/openai"
	"code.andersonlizarazo.com/agent/config"
	"code.andersonlizarazo.com/agent/internal/testserver"
	"github.com/BurntSushi/toml"
)

func TestBasicLoop(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{Text: "1"},
		testserver.Reply{Text: "3"},
		testserver.Reply{Text: "5"},
	)
	getenv := makeEnv(t, server.URL, config.Config{}, config.ConfigPrivate{})

	stdin := bytes.NewBufferString("0\n2\n4")
	if err := do([]string{"test-agent"}, getenv, stdin, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	gotStdin := stdin.Len()
	wantStdin := 0
	if gotStdin != wantStdin {
		t.Errorf("stdin: %q, got: %d, want: %d", stdin.String(), gotStdin, wantStdin)
	}

	requests := server.Requests()

	gotReqLen := len(requests)
	wantReqLen := 3
	if gotReqLen != wantReqLen {
		t.Errorf("got: %d, want: %d", gotReqLen, wantReqLen)
	}
}

func TestBasicToolCall(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{Text: "1"},
		testserver.Reply{
			Text: "2",
			ToolCalls: []testserver.ToolCall{
				{
					ID:        "0",
					Name:      "shell",
					Arguments: `{"cmd": "echo hi"}`,
				},
			},
		},
	)
	getenv := makeEnv(t, server.URL, config.Config{}, config.ConfigPrivate{})

	stdin := bytes.NewBufferString("2\n4")
	if err := do([]string{"test-agent"}, getenv, stdin, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	gotStdin := stdin.Len()
	wantStdin := 0
	if gotStdin != wantStdin {
		t.Errorf("stdin: %q, got: %d, want: %d", stdin.String(), gotStdin, wantStdin)
	}

	requests := server.Requests()
	for _, r := range requests {
		t.Logf("%s", r)
	}

	gotReqLen := len(requests)
	wantReqLen := 2 + 1 // Two user requests + the tool call.
	if gotReqLen != wantReqLen {
		t.Errorf("got: %d, want: %d", gotReqLen, wantReqLen)
	}
}

func TestToolCallMaxBytes(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{
			ToolCalls: []testserver.ToolCall{
				{
					ID:        "0",
					Name:      "shell",
					Arguments: `{"cmd": "echo 12345678"}`,
				},
			},
		},
	)

	var defaultConfig config.Config
	defaultConfig.Guardrail.MaxShellOutputBytes = 4
	getenv := makeEnv(t, server.URL, defaultConfig, config.ConfigPrivate{})

	stdin := bytes.NewBufferString("test")
	if err := do([]string{"test-agent"}, getenv, stdin, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	requests := server.Requests()

	gotLen := len(requests)
	wantLen := 2
	if gotLen != wantLen {
		t.Errorf("got: %d, want: %d", gotLen, wantLen)
	}

	toolCallAnswerRaw := requests[1]

	type toolCallRequest struct {
		Messages []struct {
			Content string `json:"content"`
			Role    string `json:"role"`
		} `json:"messages"`
	}

	var toolCall toolCallRequest
	if err := json.Unmarshal(toolCallAnswerRaw, &toolCall); err != nil {
		t.Fatal(err)
	}

	// We expect these message roles: user, assistant & tool.
	messages := toolCall.Messages

	gotMsgLen := len(messages)
	wantMsgLen := 3
	if gotMsgLen != wantMsgLen {
		t.Errorf("got: %d, want: %d", gotMsgLen, wantMsgLen)
	}

	gotMsgUser := messages[0].Role
	wantMsgUser := "user"
	if gotMsgUser != wantMsgUser {
		t.Errorf("got: %q, want: %q", gotMsgUser, wantMsgUser)
	}

	gotMsgAssistant := messages[1].Role
	wantMsgAssistant := "assistant"
	if gotMsgAssistant != wantMsgAssistant {
		t.Errorf("got: %q, want: %q", gotMsgAssistant, wantMsgAssistant)
	}

	toolMsg := messages[2]

	gotMsgTool := toolMsg.Role
	wantMsgTool := "tool"
	if gotMsgTool != wantMsgTool {
		t.Errorf("got: %q, want: %q", gotMsgTool, wantMsgTool)
	}

	gotToolAnswer := toolMsg.Content
	wantToolAnswer := "1234\n[harness: tool output too large]"
	if gotToolAnswer != wantToolAnswer {
		t.Errorf("got: %q, want: %q", gotToolAnswer, wantToolAnswer)
	}
}

func makeEnv(t *testing.T, baseURL string, configData config.Config, privateData config.ConfigPrivate) func(string) string {
	t.Helper()

	tempDir := t.TempDir()

	configData.OpenAI.ReasoningEffort = openai.ReasoningEffortLow

	configBytes, err := toml.Marshal(configData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "config.toml"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	privateData.OpenAI.BaseURL = baseURL
	privateData.OpenAI.LanguageModel = "test"

	privateBytes, err := toml.Marshal(privateData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "private.toml"), privateBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	return func(key string) string {
		if key == config.EnvAgentConfigDir {
			return tempDir
		}

		return ""
	}
}
