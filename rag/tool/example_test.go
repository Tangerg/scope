package tool_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/rag"
	ragtool "github.com/Tangerg/scope/rag/tool"
)

func ExampleRetrieval() {
	retriever := rag.RetrieverFunc(func(_ context.Context, query rag.Query) (rag.Candidates, error) {
		doc, err := document.NewDocument(query.Text(), nil)
		if err != nil {
			return nil, err
		}
		return rag.Candidates{{Document: doc, Score: 1}}, nil
	})
	retrieval, err := ragtool.NewRetrieval(ragtool.RetrievalConfig{
		Name: "search", Description: "Retrieve evidence.", Retriever: retriever,
	})
	if err != nil {
		panic(err)
	}
	binding, err := tool.Bind(retrieval)
	if err != nil {
		panic(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "search-1", Name: "search", Arguments: `{"query":"Go design"}`,
	})
	if err != nil {
		panic(err)
	}
	result, err := binding.Call(context.Background(), invocation)
	if err != nil {
		panic(err)
	}
	var output ragtool.RetrievalOutput
	if decodeErr := json.Unmarshal(result.Details, &output); decodeErr != nil {
		panic(decodeErr)
	}
	fmt.Println(output.Candidates[0].Document.Text)
	// Output: Go design
}
