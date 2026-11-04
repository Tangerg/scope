package parser

import (
	"errors"
	"fmt"
)

func recoverTo(err *error, reset func()) {
	if r := recover(); r != nil {
		reset()
		if cause, ok := r.(error); ok {
			*err = cause
		} else {
			*err = errors.New(fmt.Sprint(r))
		}
	}
}
