package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	wantToolAnswer := "1234\n[harness: output was truncated]"
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

// TestToolCallTruncatedStderrNotice pins the case where stdout fills the
// whole byte budget: the stderr content gets dropped, so the response must
// carry a truncation marker.
func TestToolCallTruncatedStderrNotice(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{
			ToolCalls: []testserver.ToolCall{
				{
					ID:        "0",
					Name:      "shell",
					Arguments: `{"cmd": "sh -c 'printf 1234; printf ERROR >&2'"}`,
				},
			},
		},
	)

	var defaultConfig config.Config
	defaultConfig.Guardrail.MaxShellOutputBytes = 4
	getenv := makeEnv(t, server.URL, defaultConfig, config.ConfigPrivate{})

	if err := do([]string{"test-agent"}, getenv, bytes.NewBufferString("test"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	requests := server.Requests()
	if len(requests) != 2 {
		t.Fatalf("got: %d requests, want: %d", len(requests), 2)
	}

	var toolCall toolCallRequest
	if err := json.Unmarshal(requests[1], &toolCall); err != nil {
		t.Fatal(err)
	}

	if len(toolCall.Messages) != 3 {
		t.Fatalf("got: %d messages, want: %d", len(toolCall.Messages), 3)
	}

	gotToolAnswer := toolCall.Messages[2].Content
	if !strings.HasPrefix(gotToolAnswer, "1234") {
		t.Errorf("got: %q, want prefix: %q", gotToolAnswer, "1234")
	}
	if !strings.Contains(gotToolAnswer, "[harness: output was truncated]") {
		t.Errorf("got: %q, want it to report the dropped stderr as truncated", gotToolAnswer)
	}
}

// TestToolCallSignalledChild pins the signal case: a child killed by a
// signal has no exit code, so the response must name the signal.
func TestToolCallSignalledChild(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{
			ToolCalls: []testserver.ToolCall{
				{
					ID:        "0",
					Name:      "shell",
					Arguments: `{"cmd": "kill -9 $$"}`,
				},
			},
		},
	)
	getenv := makeEnv(t, server.URL, config.Config{}, config.ConfigPrivate{})

	if err := do([]string{"test-agent"}, getenv, bytes.NewBufferString("test"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	requests := server.Requests()
	if len(requests) != 2 {
		t.Fatalf("got: %d requests, want: %d", len(requests), 2)
	}

	var toolCall toolCallRequest
	if err := json.Unmarshal(requests[1], &toolCall); err != nil {
		t.Fatal(err)
	}

	if len(toolCall.Messages) != 3 {
		t.Fatalf("got: %d messages, want: %d", len(toolCall.Messages), 3)
	}

	gotToolAnswer := toolCall.Messages[2].Content
	if !strings.Contains(gotToolAnswer, "terminated by signal: killed") {
		t.Errorf("got: %q, want it to name the signal", gotToolAnswer)
	}
	if strings.Contains(gotToolAnswer, "[harness: exit code -1]") {
		t.Errorf("got: %q, want no exit code for a signalled child", gotToolAnswer)
	}
}

type toolCallRequest struct {
	Messages []struct {
		Content string `json:"content"`
		Role    string `json:"role"`
	} `json:"messages"`
}
