package testserver_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openaicompat"
	"code.andersonlizarazo.com/agent/internal/testserver"
)

func model(t *testing.T, server *testserver.Server) fantasy.LanguageModel {
	t.Helper()

	provider, err := openaicompat.New(
		openaicompat.WithBaseURL(server.URL),
		openaicompat.WithAPIKey("test-key"),
	)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	languageModel, err := provider.LanguageModel(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("language model: %v", err)
	}

	return languageModel
}

func prompt() fantasy.Prompt {
	return fantasy.Prompt{
		{Role: fantasy.MessageRoleUser, Content: []fantasy.MessagePart{fantasy.TextPart{Text: "hello"}}},
	}
}

func TestServerStreamsText(t *testing.T) {
	t.Parallel()

	server := testserver.New(testserver.Reply{Text: "hello back"})
	defer server.Close()

	stream, err := model(t, server).Stream(t.Context(), fantasy.Call{Prompt: prompt()})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	var text strings.Builder
	for part := range stream {
		if part.Type == fantasy.StreamPartTypeTextDelta {
			text.WriteString(part.Delta)
		}
	}

	want := "hello back"
	if got := text.String(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func TestServerScriptsToolCall(t *testing.T) {
	t.Parallel()

	server := testserver.New(testserver.Reply{
		ToolCalls: []testserver.ToolCall{{ID: "call-1", Name: "shell", Arguments: `{"cmd":"echo hi"}`}},
	})
	defer server.Close()

	response, err := model(t, server).Generate(t.Context(), fantasy.Call{Prompt: prompt()})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var calls []fantasy.ToolCallContent
	for _, content := range response.Content {
		if call, ok := fantasy.AsContentType[fantasy.ToolCallContent](content); ok {
			calls = append(calls, call)
		}
	}

	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].ToolName != "shell" || calls[0].Input != `{"cmd":"echo hi"}` {
		t.Fatalf("tool call = %+v", calls[0])
	}
}

type echoInput struct {
	Cmd string `json:"cmd" description:"Command to run."`
}

func TestServerDrivesAgentToolLoop(t *testing.T) {
	t.Parallel()

	server := testserver.New(
		testserver.Reply{ToolCalls: []testserver.ToolCall{{ID: "call-1", Name: "shell", Arguments: `{"cmd":"hi"}`}}},
		testserver.Reply{Text: "all done"},
	)
	defer server.Close()

	var ran string
	tool := fantasy.NewAgentTool("shell", "Run a command.", func(ctx context.Context, input echoInput, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
		ran = input.Cmd
		return fantasy.NewTextResponse("ok"), nil
	})

	agent := fantasy.NewAgent(model(t, server), fantasy.WithTools(tool))

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	retries := 0
	var text strings.Builder
	if _, err := agent.Stream(ctx, fantasy.AgentStreamCall{
		Messages:   []fantasy.Message{fantasy.NewUserMessage("run it")},
		MaxRetries: &retries,
		OnTextDelta: func(_, delta string) error {
			text.WriteString(delta)
			return nil
		},
	}); err != nil {
		t.Fatalf("agent stream: %v", err)
	}

	wantToolInput := "hi"
	if ran != wantToolInput {
		t.Fatalf("tool input = %q, want %q", ran, wantToolInput)
	}

	wantText := "all done"
	if got := text.String(); got != wantText {
		t.Fatalf("text = %q, want %q", got, wantText)
	}

	wantRequests := 2
	if len(server.Requests()) != wantRequests {
		t.Fatalf("requests = %d, want %d", len(server.Requests()), wantRequests)
	}
}
