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

func TestRequireResolved(t *testing.T) {
	if err := RequireResolved(""); err == nil {
		t.Fatal("RequireResolved accepted resolved options without a model")
	}
	if err := RequireResolved("gpt-4o"); err != nil {
		t.Fatalf("RequireResolved(gpt-4o) = %v", err)
	}
}
