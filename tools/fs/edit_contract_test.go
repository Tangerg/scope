package fs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestEditRejectsInvalidReplacementsWithoutChangingFile(t *testing.T) {
	tests := []struct {
		name, content, old, replacement string
		binary                          bool
	}{
		{name: "indentation", content: "func f() {\n\treturn 1\n}\n", old: "    return 1", replacement: "    return 2"},
		{name: "trailing whitespace", content: "alpha   \nbeta\n", old: "alpha\nbeta", replacement: "x\ny"},
		{name: "internal whitespace", content: "total = a  +  b\n", old: "total = a + b", replacement: "total = a - b"},
		{name: "string literal whitespace", content: "value = \"a  b\"\n", old: "value = \"a b\"", replacement: "value = \"changed\""},
		{name: "unchanged", content: "hello", old: "hello", replacement: "hello"},
		{name: "binary replacement", content: "hello", old: "hello", replacement: "a\x00b", binary: true},
	}
	for _, test := range tests {
		for _, viaTool := range []bool{false, true} {
			mode := "direct"
			if viaTool {
				mode = "tool"
			}
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				path := writeTemp(t, root, "file.txt", test.content)
				executor := mustLocalExecutor(t, root)
				request := EditRequest{Path: path, OldString: test.old, NewString: test.replacement}
				var err error
				if viaTool {
					executable := mustEditTool(t, executor)
					binding, bindErr := toolcontract.Bind(executable)
					if bindErr != nil {
						t.Fatal(bindErr)
					}
					arguments, encodeErr := json.Marshal(request)
					if encodeErr != nil {
						t.Fatal(encodeErr)
					}
					invocation, prepareErr := binding.Prepare(chat.ToolCall{ID: "edit", Name: "edit", Arguments: string(arguments)})
					if prepareErr != nil {
						t.Fatal(prepareErr)
					}
					_, err = binding.Call(context.Background(), invocation)
				} else {
					_, err = executor.Edit(t.Context(), request)
				}
				if err == nil {
					t.Fatal("invalid replacement succeeded")
				}
				if test.binary && !errors.Is(err, ErrBinaryFile) {
					t.Fatalf("error = %v, want ErrBinaryFile", err)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != test.content {
					t.Fatalf("file = %q, want unchanged %q", got, test.content)
				}
			})
		}
	}
}
