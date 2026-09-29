// Package rag provides small interfaces and combinators for
// Retrieval-Augmented Generation.
//
// The root package owns queries, candidates, citations, generation input,
// stage contracts ([Transformer], [Expander], [Retriever], [Refiner], and
// [Augmenter]), and deterministic retrieval composition. It depends on document
// values rather than model, storage, or tool protocols. Adapters in this module
// build on the same contracts:
//   - [github.com/Tangerg/scope/rag/chat] owns model-backed query transforms,
//     expansion, reranking, contextual prompts, and chat request preparation.
//   - [github.com/Tangerg/scope/rag/vectorstore] adapts vector search to retrieval.
//   - [github.com/Tangerg/scope/rag/rerank] adapts a dedicated rerank model to refinement.
//   - [github.com/Tangerg/scope/rag/tool] exposes retrieval as a model-visible tool.
//
// Composition is explicit. Wrap a retriever with the stages you need:
//
//	r, err := rag.WithTransformers(base, rewrite, translate)
//	r, err = rag.WithExpander(rag.ExpansionConfig{Retriever: r, Expander: multiQuery})
//	top, err := rag.TopK(8)
//	r, err = rag.WithRefiners(r, top)
//	docs, err := r.Retrieve(ctx, q)
//
// Function adapters such as [RetrieverFunc] forward calls without adding policy.
// A composed retriever validates its query at entry and each external stage's
// output before the next stage consumes it; built-in stages also validate their
// own inputs so they can be used outside a composition.
//
// # Parallel retriever fan-out
//
// [ReciprocalRankFusion] combines independent rankings without comparing their
// raw scores, and [WithExpander] applies the same fusion to expanded queries:
//
//	combined, err := rag.ReciprocalRankFusion(rag.FusionRetrieverConfig{}, vectorR1, vectorR2)
//
// [TopK] only sorts and caps a comparable result. [Dedup] keeps the best
// candidate for each known document identity; apply it before TopK when a
// single source can return duplicate identities. Fusion already merges them.
//
// # Routing and agentic retrieval
//
// There is no router stage. Route a query by implementing [Retriever] and
// switching on a value stored under a [ValueKey]:
//
//	func (r *routingRetriever) Retrieve(ctx context.Context, q rag.Query) (rag.Candidates, error) {
//	    route, _, err := q.Value(r.routeKey)
//	    if err != nil {
//	        return nil, err
//	    }
//	    if route == "logs" {
//	        return r.logs.Retrieve(ctx, q)
//	    }
//	    return r.docs.Retrieve(ctx, q)
//	}
//
// [github.com/Tangerg/scope/rag/tool.NewRetrieval] adapts any composed Retriever
// to the ordinary core tool contract, so agents need no RAG-specific API.
package rag
