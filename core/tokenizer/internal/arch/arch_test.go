// Package arch contains architecture fitness tests for the tokenizer package.
package arch

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/tokenizer"
)

func TestCapabilitiesRemainSmall(t *testing.T) {
	want := map[reflect.Type][]string{
		reflect.TypeFor[tokenizer.TextCounter](): {"CountText"},
		reflect.TypeFor[tokenizer.Encoder]():     {"Encode"},
		reflect.TypeFor[tokenizer.Decoder]():     {"Decode"},
		reflect.TypeFor[tokenizer.Tokenizer]():   {"Decode", "Encode"},
	}
	for capability, methods := range want {
		if capability.NumMethod() != len(methods) {
			t.Errorf("%v has %d methods, want %d", capability, capability.NumMethod(), len(methods))
			continue
		}
		for index, method := range methods {
			if got := capability.Method(index).Name; got != method {
				t.Errorf("%v method %d = %s, want %s", capability, index, got, method)
			}
		}
	}
}

func TestCoreModuleHasNoReplaceDirectives(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(packageRoot(t), "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	if strings.Contains(contents, "\nreplace ") || strings.Contains(contents, "\nreplace(") {
		t.Fatal("core module must not contain replace directives")
	}
}

func packageRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
