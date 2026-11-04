package textread_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/tools/textread"
)

func TestVisitLinesStopsAfterCallbackCancellation(t *testing.T) {
	const source = "first\nsecond\nthird\n"
	failure := errors.New("source read failed")
	for _, test := range []struct {
		name   string
		reader io.Reader
		cause  error
	}{
		{name: "buffered lines", reader: strings.NewReader(source)},
		{name: "buffered read failure", reader: &readFailure{content: source, cause: failure}, cause: failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			cause := errors.New("visitor stopped")
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var visited []int
			err := textread.VisitLines(ctx, test.reader, textread.Limits{
				InputBytes: int64(len(source)), LineBytes: len(source),
			}, func(number int, _ []byte) error {
				visited = append(visited, number)
				cancel(cause)
				return nil
			})
			if !errors.Is(err, cause) || !slices.Equal(visited, []int{1}) {
				t.Fatalf("VisitLines = %v, %v; want only the first callback and its cancellation cause", visited, err)
			}
			if test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("cancellation lost the observed source failure: %v", err)
			}
		})
	}
}

func TestVisitLinesPreservesCallbackStop(t *testing.T) {
	const source = "first\nsecond\n"
	stop := errors.New("visitor rejected line")
	cause := errors.New("visitor canceled scan")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	var visited []int
	err := textread.VisitLines(ctx, strings.NewReader(source), textread.Limits{
		InputBytes: int64(len(source)), LineBytes: len(source),
	}, func(number int, _ []byte) error {
		visited = append(visited, number)
		cancel(cause)
		return stop
	})
	if !errors.Is(err, stop) || errors.Is(err, cause) || !slices.Equal(visited, []int{1}) {
		t.Fatalf("VisitLines = %v, %v; want the callback's own stop", visited, err)
	}
}

type readFailure struct {
	content string
	cause   error
}

func (r *readFailure) Read(buffer []byte) (int, error) {
	n := copy(buffer, r.content)
	r.content = r.content[n:]
	return n, r.cause
}
