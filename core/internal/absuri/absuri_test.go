package absuri

import "testing"

func TestValid(t *testing.T) {
	for name, test := range map[string]struct {
		value string
		valid bool
	}{
		"web":             {value: "https://example.com/a", valid: true},
		"opaque":          {value: "mailto:someone@example.com", valid: true},
		"path only":       {value: "file:///tmp/a", valid: true},
		"relative":        {value: "/a/b", valid: false},
		"scheme only":     {value: "https:", valid: false},
		"inner space":     {value: "https://example.com/a b", valid: false},
		"opaque space":    {value: "mailto:a b", valid: false},
		"leading space":   {value: " https://example.com", valid: false},
		"unparsable host": {value: "https://exa mple.com", valid: false},
		"empty":           {value: "", valid: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Valid(test.value); got != test.valid {
				t.Fatalf("Valid(%q) = %t, want %t", test.value, got, test.valid)
			}
		})
	}
}
