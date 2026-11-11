package errscope

import (
	"errors"
	"fmt"
	"testing"
)

type scopedError struct{ name string }

func (s *scopedError) Error() string { return s.name }

func TestFindFollowsOneScope(t *testing.T) {
	target := &scopedError{name: "target"}
	other := errors.New("other")
	for name, test := range map[string]struct {
		err   error
		found bool
	}{
		"nil":                    {err: nil},
		"typed nil":              {err: (*scopedError)(nil)},
		"direct":                 {err: target, found: true},
		"wrapped":                {err: fmt.Errorf("outer: %w", target), found: true},
		"join with one cause":    {err: errors.Join(nil, fmt.Errorf("outer: %w", target)), found: true},
		"join of two causes":     {err: errors.Join(target, other)},
		"multi-wrap two causes":  {err: fmt.Errorf("%w and %w", other, target)},
		"unrelated":              {err: other},
		"wrapped unrelated join": {err: fmt.Errorf("outer: %w", errors.Join(other, target))},
	} {
		t.Run(name, func(t *testing.T) {
			got, found := Find[*scopedError](test.err)
			if found != test.found || (found && got != target) {
				t.Fatalf("Find() = %v, %t; want found %t", got, found, test.found)
			}
		})
	}
}
