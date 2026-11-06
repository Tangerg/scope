package nomic

import "fmt"

type embeddingTask string

func (e embeddingTask) validate() error {
	switch e {
	case TaskTypeSearchQuery, TaskTypeSearchDocument, TaskTypeClassification, TaskTypeClustering:
		return nil
	default:
		return fmt.Errorf("nomic: unsupported task_type %q", e)
	}
}
