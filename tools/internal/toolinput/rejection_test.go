package toolinput_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolinput"
)

func TestPureRuleDiagnosticsRequirePublishableText(t *testing.T) {
	if err := toolinput.Rejection(nil); err != nil {
		t.Fatal("valid input was rejected", err)
	}
	cause := errors.New("path must be relative")
	rejection, public := tool.ClassifyInputRejection(toolinput.Rejection(cause))
	if !public || rejection.Message() != cause.Error() || !errors.Is(rejection.Cause(), cause) || errors.Is(rejection, cause) {
		t.Fatal("public correction and diagnostic cause were conflated")
	}
	err := toolinput.Rejection(errors.New(string([]byte{0xff})))
	if _, public := tool.ClassifyInputRejection(err); public || !errors.Is(err, tool.ErrInvalidInputRejection) {
		t.Fatal("unpublishable text escaped the input boundary", err)
	}
}
