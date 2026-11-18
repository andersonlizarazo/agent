package agentictools

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
)

func TestMakeCommandOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stdout     string
		stdoutTrun bool
		stderr     string
		stderrTrun bool
		exitCode   int
		extra      string
		want       string
	}{
		{
			name:     "empty stdout with exit code",
			exitCode: 3,
			want:     "[harness: exit code 3]",
		},
		{
			name:  "empty stdout with signal",
			extra: "terminated by signal: killed",
			want:  "[harness: terminated by signal: killed]",
		},
		{
			name:   "empty stdout with only stderr",
			stderr: "oops\n",
			want:   "[harness: stderr]\noops\n",
		},
		{
			name:     "stdout keeps its newline before a notice",
			stdout:   "1234",
			exitCode: 1,
			want:     "1234\n[harness: exit code 1]",
		},
		{
			name:       "dropped stderr still reports truncation",
			stdout:     "1234",
			stderrTrun: true,
			want:       "1234\n[harness: output was truncated]",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := makeCommandOutput(test.stdout, test.stdoutTrun, test.stderr, test.stderrTrun, test.exitCode, test.extra)
			if got != test.want {
				t.Errorf("got: %q, want: %q", got, test.want)
			}
		})
	}
}

// TestExtractCommandPayload pins the wire format the model must emit.
func TestExtractCommandPayload(t *testing.T) {
	t.Parallel()

	toolCall := fantasy.ToolCallContent{Input: `{"payload": ["sh", "-c", "ls", "."]}`}

	got, err := ExtractCommandPayload(toolCall)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"sh", "-c", "ls", "."}
	if !slices.Equal(got, want) {
		t.Errorf("got: %q, want: %q", got, want)
	}
}

func TestCleanPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		homeDir string
		want    string
	}{
		{
			name:    "inside the home directory",
			path:    "/home/user/Documents/agent",
			homeDir: "/home/user",
			want:    "~/Documents/agent",
		},
		{
			name:    "the home directory itself",
			path:    "/home/user",
			homeDir: "/home/user",
			want:    "~",
		},
		{
			name:    "a sibling with the home directory as prefix",
			path:    "/home/user2/Documents",
			homeDir: "/home/user",
			want:    "/home/user2/Documents",
		},
		{
			name:    "outside the home directory",
			path:    "/etc/hosts",
			homeDir: "/home/user",
			want:    "/etc/hosts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := cleanPath(test.path, test.homeDir)
			if got != test.want {
				t.Errorf("got: %q, want: %q", got, test.want)
			}
		})
	}
}

// TestCommandToolAppliesTimeout pins the guardrail: a millisecond budget passed
// to MakeCommandTool must bound the child process.
func TestCommandToolAppliesTimeout(t *testing.T) {
	t.Parallel()

	tool := MakeCommandTool(t.TempDir(), t.TempDir(), nil, 200*time.Millisecond, 0)

	start := time.Now()
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Input: `{"payload":["sleep","5"]}`})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	if elapsed > 2*time.Second {
		t.Errorf("elapsed: %v, want under %v: the guardrail did not bound the command", elapsed, 2*time.Second)
	}

	if !strings.Contains(resp.Content, context.DeadlineExceeded.Error()) {
		t.Errorf("got: %q, want it to report %q", resp.Content, context.DeadlineExceeded.Error())
	}
}
