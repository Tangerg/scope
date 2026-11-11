package run_test

import (
	"testing"

	"github.com/Tangerg/scope/agent/run"
)

func TestDeriveChildKeySeparatesEveryPartList(t *testing.T) {
	derive := func(parts ...string) run.ChildKey {
		t.Helper()
		key, err := run.DeriveChildKey("test.child", parts...)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	first, replayed := derive("stage", "member"), derive("stage", "member")
	if first != replayed {
		t.Fatal("equal parts derived different keys")
	}
	for _, pair := range [][2][]string{
		{{"ab", "c"}, {"a", "bc"}},
		{{"a\x00", "b"}, {"a", "\x00b"}},
		{{"1:a"}, {"1", "a"}},
		{{""}, {}},
	} {
		if derive(pair[0]...) == derive(pair[1]...) {
			t.Fatalf("parts %q and %q derived one key", pair[0], pair[1])
		}
	}
	if _, err := run.DeriveChildKey("bad namespace", "part"); err == nil {
		t.Fatal("an invalid namespace derived a key")
	}
}
