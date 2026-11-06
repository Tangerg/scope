package jina

import "fmt"

type embeddingTask string

const (
	embeddingTaskRetrievalQuery          embeddingTask = "retrieval.query"
	embeddingTaskRetrievalPassage        embeddingTask = "retrieval.passage"
	embeddingTaskTextMatching            embeddingTask = "text-matching"
	embeddingTaskClassification          embeddingTask = "classification"
	embeddingTaskSeparation              embeddingTask = "separation"
	embeddingTaskClustering              embeddingTask = "clustering"
	embeddingTaskCodeQuery               embeddingTask = "code.query"
	embeddingTaskCodePassage             embeddingTask = "code.passage"
	embeddingTaskNLToCodeQuery           embeddingTask = "nl2code.query"
	embeddingTaskNLToCodePassage         embeddingTask = "nl2code.passage"
	embeddingTaskQAQuery                 embeddingTask = "qa.query"
	embeddingTaskQAPassage               embeddingTask = "qa.passage"
	embeddingTaskCodeToCodeQuery         embeddingTask = "code2code.query"
	embeddingTaskCodeToCodePassage       embeddingTask = "code2code.passage"
	embeddingTaskCodeToNLQuery           embeddingTask = "code2nl.query"
	embeddingTaskCodeToNLPassage         embeddingTask = "code2nl.passage"
	embeddingTaskCodeToCompletionQuery   embeddingTask = "code2completion.query"
	embeddingTaskCodeToCompletionPassage embeddingTask = "code2completion.passage"
)

// Task vocabularies belong to the model schemas; a task's presence requires a
// known schema, while omitting it leaves model selection open to the provider.
func (e embeddingTask) validate(model string) error {
	switch model {
	case ModelEmbeddingsV3:
		switch e {
		case embeddingTaskRetrievalQuery, embeddingTaskRetrievalPassage, embeddingTaskTextMatching, embeddingTaskClassification, embeddingTaskSeparation:
			return nil
		}
	case ModelEmbeddingsV4:
		switch e {
		case embeddingTaskRetrievalQuery, embeddingTaskRetrievalPassage, embeddingTaskTextMatching, embeddingTaskCodeQuery, embeddingTaskCodePassage:
			return nil
		}
	case ModelEmbeddingsV5TextNano, ModelEmbeddingsV5TextSmall, ModelEmbeddingsV5OmniNano, ModelEmbeddingsV5OmniSmall:
		switch e {
		case embeddingTaskRetrievalQuery, embeddingTaskRetrievalPassage, embeddingTaskTextMatching, embeddingTaskClassification, embeddingTaskClustering:
			return nil
		}
	case ModelClipV2:
		if e == embeddingTaskRetrievalQuery {
			return nil
		}
	case ModelCodeEmbeddings500M, ModelCodeEmbeddings1500M:
		switch e {
		case embeddingTaskNLToCodeQuery, embeddingTaskNLToCodePassage, embeddingTaskQAQuery, embeddingTaskQAPassage,
			embeddingTaskCodeToCodeQuery, embeddingTaskCodeToCodePassage, embeddingTaskCodeToNLQuery, embeddingTaskCodeToNLPassage,
			embeddingTaskCodeToCompletionQuery, embeddingTaskCodeToCompletionPassage:
			return nil
		}
	}
	return fmt.Errorf("jina: task %q is unsupported for model %q", e, model)
}
