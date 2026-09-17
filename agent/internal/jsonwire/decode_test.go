package jsonwire

import "testing"

func TestDecodeStrictObject(t *testing.T) {
	type record struct {
		Count int `json:"count"`
	}
	for _, data := range []string{
		``, `{}`, `{"count":null}`, `{"count":1,"extra":2}`,
		`{"count":1,"count":2}`, `{"count":1} {"count":2}`, `null`,
		"{\"count\":\"\xff\"}",
	} {
		if _, err := Decode[record]([]byte(data), "count"); err == nil {
			t.Errorf("accepted invalid required object %q", data)
		}
	}
	got, err := Decode[record]([]byte(`{"count":0}`), "count")
	if err != nil || got.Count != 0 {
		t.Fatalf("explicit zero = %+v, %v", got, err)
	}
	if _, err := Decode[[]int]([]byte(`[1]`), "count"); err == nil {
		t.Fatal("required members accepted on an array")
	}
}
