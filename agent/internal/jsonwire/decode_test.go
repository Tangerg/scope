package jsonwire

import "testing"

func TestDecodeStrictObject(t *testing.T) {
	type record struct {
		Count int `json:"count" jsonwire:"required"`
	}
	for _, data := range []string{
		``, `{}`, `[1]`, `{"count":null}`, `{"count":1,"extra":2}`,
		`{"count":1,"count":2}`, `{"count":1} {"count":2}`, `null`,
		"{\"count\":\"\xff\"}",
	} {
		if _, err := Decode[record]([]byte(data)); err == nil {
			t.Errorf("accepted invalid required object %q", data)
		}
	}
	got, err := Decode[record]([]byte(`{"count":0}`))
	if err != nil || got.Count != 0 {
		t.Fatalf("explicit zero = %+v, %v", got, err)
	}
	if _, err := Decode[[]int]([]byte(`[1]`)); err != nil {
		t.Fatalf("array without required members rejected: %v", err)
	}
}

func TestDecodeRequiresMembersOfEmbeddedStructs(t *testing.T) {
	type inner struct {
		Name string `json:"name" jsonwire:"required"`
	}
	type outer struct {
		inner
		Note string `json:"note,omitempty"`
	}
	if _, err := Decode[outer]([]byte(`{"note":"x"}`)); err == nil {
		t.Fatal("embedded required member was not required")
	}
	if got, err := Decode[outer]([]byte(`{"name":""}`)); err != nil || got.Name != "" {
		t.Fatalf("explicit empty name = %+v, %v", got, err)
	}
}
