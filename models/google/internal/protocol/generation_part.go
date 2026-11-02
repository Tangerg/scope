package protocol

import (
	"errors"

	"google.golang.org/genai"
)

func validateGenerationPart(part *genai.Part) error {
	switch {
	case part == nil:
		return errors.New("nil generation part")
	case part.ExecutableCode != nil:
		return errors.New("executableCode output and replay cannot be represented")
	case part.CodeExecutionResult != nil:
		return errors.New("codeExecutionResult output and replay cannot be represented")
	default:
		return nil
	}
}
