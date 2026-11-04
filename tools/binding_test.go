package tools_test

import (
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/fs"
	"github.com/Tangerg/scope/tools/httpreq"
	"github.com/Tangerg/scope/tools/shell"
	"github.com/Tangerg/scope/tools/web"
)

func TestBuiltinsDoNotExposeReplaceableExecution(t *testing.T) {
	caller := reflect.TypeFor[tool.Caller]()
	for _, model := range []reflect.Type{
		reflect.TypeFor[fs.ReadTool](), reflect.TypeFor[fs.WriteTool](),
		reflect.TypeFor[fs.EditTool](), reflect.TypeFor[fs.GlobTool](),
		reflect.TypeFor[fs.GrepTool](), reflect.TypeFor[fs.ApplyPatchTool](),
		reflect.TypeFor[shell.Tool](), reflect.TypeFor[httpreq.Tool](),
		reflect.TypeFor[web.SearchTool](), reflect.TypeFor[web.FetchTool](),
	} {
		t.Run(model.PkgPath()+"/"+model.Name(), func(t *testing.T) {
			for field := range model.NumField() {
				member := model.Field(field)
				if member.IsExported() && (member.Type.Implements(caller) || member.Type.Kind() == reflect.Func) {
					t.Errorf("%s exposes replaceable execution in %s; binding cannot freeze its declared contract", model, member.Name)
				}
			}
		})
	}
}
