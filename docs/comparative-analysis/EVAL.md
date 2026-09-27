# Eval: a general evaluation runtime, not an experiment platform

Eval is Scope's independent support layer. It evaluates subjects of any type
and can assess a host-owned target execution. It does not own agent recovery or
external harness scheduling, and it depends on neither RAG, chat, nor any
concrete model.

## The public waist

An atomic assessment uses this behavior:

```go
type Evaluator[T any] interface {
    Evaluate(context.Context, T) (Report, error)
}
```

Around that interface, `eval` provides:

- `Dataset[T]`, immutable, stably ordered, and unique by ID;
- `Assessment[T]` and `Suite[T]`, which preserve the identity and outcome of
  each independent evaluation, including failures;
- `Experiment[T]`, with bounded case concurrency and explicit collect or
  fail-fast semantics;
- `Target[I, O]` and `Trial[I, O]`, which connect an execution receipt to its
  assessments without owning the target's runtime;
- `CompositeEvaluator`, with explicit weights, required components, and a pass
  policy;
- summaries and quantile distributions aggregated by complete metric identity;
- baseline-to-candidate comparison over the same fixture and case IDs, pairing
  observations by case, assessment, and complete metric identity;
- `ProjectionEvaluator`, which maps an aggregate subject onto a narrow input.

`Report` does not force every evaluator to produce the same kind of conclusion.
An optional `Decision` owns the acceptance policy and verdict; a normalized
higher-is-better `Score`, a finite raw measurement, feedback, metadata, and
details are separate. The unit and optimization direction of a raw measurement
belong to the structured metric identity. Details are supporting evidence, not
additional experiment samples, and are bounded by `MaxReportDepth` at every
public trust boundary.

## Where the vocabulary lives

- `eval/judge` adapts a structured model judgment to the general report, with
  repeated sampling and median aggregation.
- `eval/text` owns text subjects such as answer relevance, correctness, and
  groundedness.
- `eval/ranking` owns ranking subjects such as precision, recall, MRR, and
  NDCG.

These leaf packages implement the root protocol only. They cannot promote their
own sample, prompt, or metric vocabulary into the root model. When RAG needs to
evaluate retrieval results, the caller constructs the corresponding typed
subject rather than making `eval` depend on RAG again.

## Explicit boundaries

Eval owns in-memory datasets, trials, experiments, aggregation, comparison, and
reports. It does not own:

- a persistence service for datasets or results;
- traces, run artifacts, project catalogs, or remote scheduling;
- dashboards, experiment UIs, permissions, tenancy, or release workflows;
- significance claims without a statistical model behind them;
- a policy that merges different units or different metric identities into one
  overall score.

Those capabilities need a host lifecycle and a product data model, so they
compose in Flame or in a separate experiment harness. `otel/eval` binds a
configured assessment identity before evaluation and records its outcome and
latency without observing subject content.

## Conclusion

`eval` is the evaluation runtime of a general AI infrastructure library — not a
collection of RAG evaluators, and not a complete experiment product. It should
keep growing around reusable protocols and honest result semantics. Storage,
artifacts, servers, and UIs stay out of this repository.
