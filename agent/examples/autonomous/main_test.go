package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "20 + 22 = 42\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestMainEntryPointRunsToCompletion(t *testing.T) {
	// main wires the same in-process example as run; exercising it keeps the
	// command entry point covered. run uses only in-process fakes, so it cannot
	// reach the os.Exit failure branch.
	main()
}
