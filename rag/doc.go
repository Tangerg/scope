// Package rag provides small interfaces and combinators for
// Retrieval-Augmented Generation.
//
// Quick start:
//
//	q, _ := rag.NewQuery("what is GOAP?")
//	docs, err := retriever.Retrieve(ctx, q)
//
// The package owns the stable contracts ([Transformer], [Expander],
// [Retriever], [Refiner], and [Augmenter]) as well as the small concrete
// adapters that make those contracts useful: vector-store retrieval,
// model-backed query transforms, dedicated and chat-backed reranking, citation-aware contextual
// augmentation, and chat middleware. Keeping them together follows the Go
// standard-library style:
// one discoverable package, small interfaces, explicit composition.
//
// Composition is explicit. Wrap a retriever with the stages you need:
//
//	r, err := rag.WithTransformers(base, rewrite, translate)
//	r, err = rag.WithExpander(r, multiQuery)
//	top, err := rag.TopK(8)
//	r, err = rag.WithRefiners(r, top)
//	docs, err := r.Retrieve(ctx, q)
//
// Function adapters forward calls without adding policy. A composed retriever
// validates its query at entry and each external stage's output before the next
// stage consumes it. Built-in stages also validate their own public inputs so
// they can be used directly outside a composition.
//
// [IdentityAugmenter] preserves the query when contextual augmentation is
// deliberately empty. Other optional stages are omitted from composition.
//
// # Parallel retriever fan-out
//
// Scope deliberately does not ship a separate "DocumentJoiner"
// abstraction. Use [Parallel] to run retrievers concurrently and union their
// result lists into a flat slice; use refiners to re-organize that slice.
// A typical "join overlapping retriever results" pattern is:
//
//	top, err := rag.TopK(topK)
//	combined, err := rag.Parallel(vectorR1, vectorR2)
//	r, err := rag.WithRefiners(combined, top)
//
// TopK keeps the highest-scoring candidate for each non-empty document ID
// before ranking and capping, so duplicate hits cannot consume result slots.
// Use [Dedup] separately only when unique documents are needed without score
// ordering or a result cap. Score-based refiners assume all retrievers use a
// comparable score scale. Use [ReciprocalRankFusion] before TopK when combining
// unlike ranking systems so fusion depends on result order instead of raw
// scores.
//
// # Agentic retrieval
//
// [NewRetrievalTool] adapts any composed Retriever to the ordinary core tool
// contract. Agent runtimes can advertise it immediately or keep it in their
// deferred tool set without introducing an agent-specific RAG API.
//
// # Per-query retriever routing
//
// Likewise, there is no "QueryRouter" stage. To route a query to a
// subset of retrievers (e.g. by topic, language, or metadata), wrap
// your retrievers in a custom [Retriever] that switches on the query
// internally:
//
//	type routingRetriever struct {
//	    routeKey rag.ValueKey[string]
//	    docsR, logsR rag.Retriever
//	}
//	func (r *routingRetriever) Retrieve(ctx context.Context, q rag.Query) (rag.Candidates, error) {
//	    route, _, err := q.Value(r.routeKey)
//	    if err != nil {
//	        return nil, err
//	    }
//	    if route == "logs" {
//	        return r.logsR.Retrieve(ctx, q)
//	    }
//	    return r.docsR.Retrieve(ctx, q)
//	}
//
// Create routeKey once with [NewValueKey] and retain it in the retriever.
// Callers stay oblivious; routing logic lives at the retriever boundary.
package rag
