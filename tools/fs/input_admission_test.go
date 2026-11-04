package fs_test

import (
	"errors"
	"os"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	scopefs "github.com/Tangerg/scope/tools/fs"
	"github.com/Tangerg/scope/tools/httpreq"
	"github.com/Tangerg/scope/tools/shell"
)

func TestPureInputErrorsAreRejectedBeforeExecution(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if writeErr := root.WriteFile("existing", []byte("old"), 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	local, err := scopefs.NewLocalExecutor(root)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	write, err := scopefs.NewWriteTool(local)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := scopefs.NewEditTool(local)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := scopefs.NewApplyPatchTool(local)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := shell.NewLocalExecutor(shell.LocalExecutorConfig{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sh, err := shell.NewTool(shell.Config{Executor: executor})
	if err != nil {
		t.Fatal(err)
	}
	client, err := httpreq.NewClient(httpreq.ClientConfig{AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	httpTool, err := httpreq.NewTool(client)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		executable tool.Tool
		arguments  string
	}{
		{"write_nul", write, `{"path":"new.txt","content":"bad\u0000text"}`},
		{"edit_empty", edit, `{"path":"existing","old_string":"","new_string":"new"}`},
		{"edit_identical", edit, `{"path":"existing","old_string":"old","new_string":"old"}`},
		{"patch_syntax", patch, `{"patch":"not a diff"}`},
		{"shell_blank", sh, `{"command":"   "}`},
		{"http_url", httpTool, `{"url":"not a URL"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding, err := tool.Bind(test.executable)
			if err != nil {
				t.Fatal(err)
			}
			_, prepareErr := binding.Contract().Prepare(tool.Request{Name: binding.Contract().Name(), Arguments: test.arguments}, tool.Admission{})
			if !errors.Is(prepareErr, tool.ErrInvalidInvocation) {
				t.Fatalf("Prepare error = %v, want invalid invocation before any execution", prepareErr)
			}
		})
	}
}
