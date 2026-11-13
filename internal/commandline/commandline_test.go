package commandline_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"code.andersonlizarazo.com/agent/internal/commandline"
)

func ExampleExecute_basic() {
	test := "test\n"
	output, n, _, _, err := commandline.Execute(context.Background(), []string{"printf", test})
	if err != nil {
		panic(err)
	}

	if n != int64(len(test)) {
		panic("wrong count!")
	}

	fmt.Println(output)

	// Output: test
}

func ExampleExecute_withOutputLimit() {
	output, _, _, _, err := commandline.Execute(context.Background(), []string{"printf", "%s\n", "12345678"}, commandline.WithOutputLimit(4))
	if err != nil {
		panic(err)
	}

	fmt.Println(output)

	// Output: 1234
}

func ExampleExecute_withOutputLimit_stderr() {
	stdout, _, stderr, n, err := commandline.Execute(context.Background(), []string{"sh", "-c", `printf '1234' && printf 'abcdefgh' >&2`}, commandline.WithOutputLimit(8))
	if err != nil {
		panic(err)
	}

	if n != 8 {
		panic("wrong count!")
	}

	fmt.Println(stdout, stderr)

	// Output: 1234 abcd
}

func ExampleExecute_withTee() {
	var bufStdout, bufStderr bytes.Buffer

	_, _, _, _, err := commandline.Execute(context.Background(), []string{"sh", "-c", `printf "Number four:" && echo "4" >&2`}, commandline.WithTeeStdout(&bufStdout), commandline.WithTeeStderr(&bufStderr))
	if err != nil {
		panic(err)
	}

	fmt.Println("Stdout:", bufStdout.String())
	fmt.Println("Stderr:", bufStderr.String())

	// Output:
	// Stdout: Number four:
	// Stderr: 4
}

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("tee failed") }

func TestExecuteTeeErrorReleasesResources(t *testing.T) {
	before, err := openFileDescriptors()
	if err != nil {
		t.Skip("open file descriptor count unavailable on this platform:", err)
	}

	for range 10 {
		_, _, _, _, err := commandline.Execute(context.Background(), []string{"sh", "-c", "printf hi"}, commandline.WithTeeStdout(failingWriter{}))
		if err == nil {
			t.Fatal("got: nil, want the tee error to surface")
		}
	}

	after, err := openFileDescriptors()
	if err != nil {
		t.Skip("open file descriptor count unavailable on this platform:", err)
	}

	if after != before {
		t.Errorf("open file descriptors: before %d, after %d, want them equal", before, after)
	}
}

func openFileDescriptors() (n int, err error) {
	fi, err := os.Stat("/proc/self/fd")
	if err != nil {
		return 0, err
	}

	// See: <https://lore.kernel.org/all/CABWYdi1LX5n1DdL1B7s+=TVK=5JDMVyp91d3yRDA0_GW4Xy8wg@mail.gmail.com/T/#u>.
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected underlying stat type")
	}

	return int(stat.Size), nil
}
