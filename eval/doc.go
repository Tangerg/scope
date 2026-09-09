// Package eval defines a subject-agnostic quality-evaluation kernel.
// Evaluator is generic over the subject, Metric carries structured identity and
// measurement semantics, and Report independently represents an optional
// verdict, normalized quality score, raw numeric measurement, feedback, and
// child reports. SuiteEvaluator preserves heterogeneous results while
// CompositeEvaluator explicitly aggregates comparable scored verdicts. Dataset
// owns case identity, Experiment executes it with bounded concurrency, and
// ExperimentReport.Compare reports exact aggregate deltas without inventing
// statistical claims. ProjectionEvaluator adapts aggregate subjects to narrow
// evaluator inputs.
// JSON decoding rejects unknown report and metric members, including nested
// details, while metadata and metric parameters retain their open JSON values.
//
// Domain vocabularies live outside the kernel: judge supplies generic
// model-backed evaluation, text owns generated-text metrics, ranking owns
// provider-neutral ranking metrics, and trajectory owns deterministic Agent
// execution evaluation. New domains implement Evaluator directly and do not
// depend on another domain's vocabulary.
package eval
