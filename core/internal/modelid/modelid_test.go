package modelid

import "testing"

func TestValidate(t *testing.T) {
	for name, test := range map[string]struct {
		id    string
		valid bool
	}{
		"empty":               {id: "", valid: true},
		"plain":               {id: "gpt-4o", valid: true},
		"inner space":         {id: "gpt 4o", valid: true},
		"leading whitespace":  {id: " gpt-4o", valid: false},
		"trailing whitespace": {id: "gpt-4o\n", valid: false},
		"invalid utf8":        {id: "gpt-\xff", valid: false},
	} {
		t.Run(name, func(t *testing.T) {
			err := Validate(test.id)
			if test.valid != (err == nil) {
				t.Fatalf("Validate(%q) error = %v, want valid = %t", test.id, err, test.valid)
			}
		})
	}
}
