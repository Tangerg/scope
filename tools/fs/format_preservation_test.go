package fs

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestWriteAndEditPreserveCRLFAfterLongFirstLine(t *testing.T) {
	for _, firstLineBytes := range []int{64<<10 - 1, 64 << 10, 65 << 10} {
		for _, bom := range []string{"", "\ufeff"} {
			for _, operation := range []string{"write", "edit"} {
				t.Run(operation, func(t *testing.T) {
					first := strings.Repeat("x", firstLineBytes)
					directory := t.TempDir()
					path := writeTemp(t, directory, "long.txt", bom+first+"\r\nsecond\r\n")
					executor := mustLocalExecutor(t, directory)
					want := bom + "updated\r\n"
					var err error
					if operation == "write" {
						_, err = executor.Write(t.Context(), WriteRequest{Path: path, Content: "updated\n"})
					} else {
						_, err = executor.Edit(t.Context(), EditRequest{Path: path, OldString: "second", NewString: "updated"})
						want = bom + first + "\r\nupdated\r\n"
					}
					if err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Fatalf("first line=%d BOM=%t size=%d error=%v", firstLineBytes, bom != "", len(got), err)
					}
				})
			}
		}
	}
}

func TestWriteRejectsUnboundedFormatDetectionBeforeMutation(t *testing.T) {
	directory := t.TempDir()
	path := writeTemp(t, directory, "large.txt", "original")
	if err := os.Truncate(path, defaultMutationInputBytes+1); err != nil {
		t.Fatal(err)
	}
	response, err := mustLocalExecutor(t, directory).Write(t.Context(), WriteRequest{Path: path, Content: "replacement"})
	if !errors.Is(err, ErrFileTooLarge) || !errors.Is(err, ErrMutationRejected) || response.BytesWritten != 0 {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != defaultMutationInputBytes+1 {
		t.Fatalf("original file changed: info=%v error=%v", info, err)
	}
}
