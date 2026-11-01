# Managed retrievers

These independently versioned Go modules bind a hosted retrieval service to
`rag.Retriever`. The service owns indexing and embeddings. Supply the host's
trusted corpus or knowledge base and any scope restrictions when constructing
the retriever. A caller can change the query text without changing that authority.

- [Bedrock Knowledge Bases](./bedrockkb/doc.go) reads ranked text evidence from one knowledge base.
- [Vectara](./vectara/doc.go) reads ranked passages and provides explicit document creation and deletion in one corpus.

Both leaves depend on RAG and Core. Neither requires a model encoder or pretends
to accept precomputed vectors. See each module's example and native contract tests.
