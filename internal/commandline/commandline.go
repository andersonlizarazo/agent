// Package commandline implements helpers for dealing with streaming outputs of the command line interface.
package commandline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
)

const (
	NoOutputLimit int64 = -1
)

func Execute(ctx context.Context, command []string, opts ...ExecuteOption) (stdout string, stdoutSize int64, stderr string, stderrSize int64, err error) {
	if len(command) == 0 {
		panic("arg [command] should have at least one item")
	}

	executeOptions := executeOption{
		outputLimit: -1,
		teeStdout:   io.Discard,
		teeStderr:   io.Discard,
	}
	for _, apply := range opts {
		apply(&executeOptions)
	}

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", 0, "", 0, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", 0, "", 0, err
	}
	if err := cmd.Start(); err != nil {
		return "", 0, "", 0, err
	}

	stdoutReader := io.TeeReader(stdoutPipe, executeOptions.teeStdout)
	stderrReader := io.TeeReader(stderrPipe, executeOptions.teeStderr)

	var bufStdout, bufStderr bytes.Buffer
	var sizeStdout, sizeStderr int64
	var errStdout, errStderr error

	var wg sync.WaitGroup
	wg.Go(func() {
		if executeOptions.outputLimit != -1 {
			sizeStdout, errStdout = drainWithLimit(&bufStdout, stdoutReader, executeOptions.outputLimit)
			return
		}

		sizeStdout, errStdout = io.Copy(&bufStdout, stdoutReader)
	})
	wg.Go(func() {
		if executeOptions.outputLimit != -1 {
			sizeStderr, errStderr = drainWithLimit(&bufStderr, stderrReader, executeOptions.outputLimit)
			return
		}

		sizeStderr, errStderr = io.Copy(&bufStderr, stderrReader)
	})
	wg.Wait()

	if executeOptions.outputLimit != -1 && int64(bufStdout.Len()+bufStderr.Len()) > executeOptions.outputLimit {
		n := min(bufStderr.Len(), int(executeOptions.outputLimit)-bufStdout.Len())
		bufStderr.Truncate(n)
	}

	stdoutString := bufStdout.String()
	stderrString := bufStderr.String()

	errWait := cmd.Wait()

	err = errStdout
	if err == nil {
		err = errStderr
	}
	if err != nil {
		return stdoutString, sizeStdout, stderrString, sizeStderr, err
	}

	if errWait != nil {
		return stdoutString, sizeStdout, stderrString, sizeStderr, errWait
	}

	return stdoutString, sizeStdout, stderrString, sizeStderr, nil
}

func drainWithLimit(buf *bytes.Buffer, pipe io.Reader, limit int64) (int64, error) {
	n1, err := io.CopyN(buf, pipe, limit)
	if err != nil && !errors.Is(err, io.EOF) {
		return n1, err
	}

	n2, err := io.Copy(io.Discard, pipe)

	return n1 + n2, err
}

type ExecuteOption func(*executeOption)

type executeOption struct {
	outputLimit int64
	teeStdout   io.Writer
	teeStderr   io.Writer
}

func WithOutputLimit(limit int64) ExecuteOption {
	return func(o *executeOption) {
		o.outputLimit = limit
	}
}

func WithTeeStdout(w io.Writer) ExecuteOption {
	return func(o *executeOption) {
		o.teeStdout = w
	}
}

func WithTeeStderr(w io.Writer) ExecuteOption {
	return func(o *executeOption) {
		o.teeStderr = w
	}
}
