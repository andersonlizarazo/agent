package main

import "testing"

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
