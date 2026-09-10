package chat_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func ExampleContextualAugmenter() {
	augmenter, err := ragchat.NewContextualAugmenter(ragchat.ContextualAugmenterConfig{})
	if err != nil {
		panic(err)
	}
	query, err := rag.NewQuery("What does Scope provide?")
	if err != nil {
		panic(err)
	}
	doc, err := document.NewDocument("Scope provides AI infrastructure.", nil)
	if err != nil {
		panic(err)
	}
	doc.ID = "scope"
	result, err := augmenter.Augment(context.Background(), query, rag.Candidates{{Document: doc, Score: 1}})
	if err != nil {
		panic(err)
	}
	citation := result.Citations()[0]
	fmt.Println(citation.Marker(), citation.Candidate.Document.ID)
	// Output: [1] scope
}
