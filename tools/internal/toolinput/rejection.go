// Package toolinput publishes errors from the built-in requests' pure input
// rules. Backend and configuration diagnostics must never enter this boundary.
package toolinput

import "github.com/Tangerg/scope/core/tool"

func Rejection(cause error) error {
	if cause == nil {
		return nil
	}
	rejection, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: cause.Error(), Cause: cause})
	if err != nil {
		return err
	}
	return rejection
}
