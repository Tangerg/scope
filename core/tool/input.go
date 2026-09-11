package tool

import "fmt"

// InputValidatingTool supplies the input admission rules that JSON Schema
// cannot express, such as Go numeric syntax or a custom JSON decoder's domain
// constraints. Bind freezes the declaration through the wrapping chain.
type InputValidatingTool interface {
	// InputValidator returns an independent, immutable validator. Neither the
	// returned function nor anything it captures may retain an executable Tool
	// or execution backend. It must be deterministic, bounded, side-effect-free,
	// safe for concurrent use, and must not mutate or retain its argument.
	// Nil means the schema is the complete input admission contract.
	InputValidator() func([]byte) error
}

func inputValidator(executable Tool) (validator func([]byte) error, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			validator = nil
			err = fmt.Errorf("input validation declaration panicked: %v", recovered)
		}
	}()
	capability, found, err := Capability[InputValidatingTool](executable)
	if err != nil || !found {
		return nil, err
	}
	return capability.InputValidator(), nil
}

func (c Contract) validateInput(arguments []byte) (err error) {
	if schemaErr := c.state.input.Validate(arguments); schemaErr != nil {
		return schemaErr
	}
	if c.state.validate == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("input validator panicked: %v", recovered)
		}
	}()
	return c.state.validate(arguments)
}
