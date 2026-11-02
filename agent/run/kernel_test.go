package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func controlValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func observeOutcome(ctx context.Context, runtime *Runtime, program Program[Payload, Payload], input Payload) (Result[Payload], error) {
	handle, err := runtime.Start(ctx, program, StartRequest[Payload]{ID: hostStartID(), Input: input})
	if err != nil {
		return Result[Payload]{}, err
	}
	return handle.Await(ctx)
}
func frameworkProductionGoFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../run/*.go")
	if err != nil {
		t.Fatal(err)
	}
	var source []string
	for _, path := range paths {
		if !strings.HasSuffix(path, "_test.go") {
			source = append(source, path)
		}
	}
	return source
}
