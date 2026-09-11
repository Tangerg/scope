package chat_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/rag"
	ragchat "github.com/Tangerg/scope/rag/chat"
)

func ExamplePreparer() {
	preparer, err := ragchat.NewPreparer(ragchat.PreparerConfig{
		Retriever: rag.RetrieverFunc(func(context.Context, rag.Query) (rag.Candidates, error) {
			return rag.Candidates{{Document: &document.Document{Text: "Scope provides AI infrastructure."}}}, nil
		}),
		Augmenter: rag.IdentityAugmenter(),
	})
	if err != nil {
		panic(err)
	}
	ctx := context.Background()
	request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("What is Scope?")))
	if err != nil {
		panic(err)
	}
	prepared, err := preparer.Prepare(ctx, request)
	if err != nil {
		panic(err)
	}
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		return textResponse("AI infrastructure"), nil
	})
	response, err := prepared.Call(ctx, model)
	if err != nil {
		panic(err)
	}
	candidates, _, err := ragchat.CandidatesFromMetadata(response.Metadata)
	if err != nil {
		panic(err)
	}
	fmt.Println(response.Text(), len(candidates))
	// Output: AI infrastructure 1
}

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
