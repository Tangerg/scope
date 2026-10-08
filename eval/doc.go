// Package eval defines subject-agnostic execution and quality evaluation.
// Evaluator is the atomic scoring contract: a non-nil error makes its Report
// unusable. Metric identifies the calculation and measurement semantics;
// Decision independently identifies the policy behind a categorical verdict.
// Score, Measurement, Decision, and qualitative feedback are optional outcomes.
// Changing a threshold changes Decision identity without changing the measured
// quantity. A summary Report is defined by its Details: observations are
// grouped and paired by the Metric together with every Detail's Metric, and
// Decision identity also covers every Detail's rule. A summary Metric and
// Decision name only the summary's own calculation and rule. ProjectionEvaluator adapts aggregate subjects to narrow evaluators.
//
// # Independent assessments
//
// Assessment declares an ID before its Evaluator runs. Suite.Run preserves one
// AssessmentResult for every declaration, including failures, cancellation,
// and work not started. Under ErrorCollect, a child error does not cancel other
// assessments. Under ErrorFailFast, pending work stops and completed results
// remain available. Suite is a collection lifecycle rather than an Evaluator;
// callers inspect SuiteResult.Err and individual statuses even when Run returns
// nil. The operation error reports caller cancellation or a fail-fast stop.
//
// CompositeEvaluator is an atomic weighted score calculation. It accepts
// score-only components by default. An explicit PassPolicy additionally requires
// decided components. Supporting Report.Details preserve evidence but do not
// become extra statistical observations. To summarize a child independently,
// declare it as a separate Assessment with its own ID.
//
// # Fixed inputs and generated executions
//
// Dataset owns fixed Case IDs and context identity. Its FixtureID changes when
// inputs, references, evaluation context, or selected cases change. Generated
// output is not part of that identity. Subject may be any borrowed immutable
// aggregate, including an offline input/output/reference sample.
//
// Target.Run performs generation and returns an Execution receipt whose status
// is independent of quality. A nil Output means no candidate was collected; an
// existing empty candidate remains present. Trial.Run composes one fixed Case,
// one Target invocation, and one Suite over TrialSample. It preserves a valid
// Execution even when assessment fails. A non-nil Target error means no valid
// execution receipt, so assessments remain not evaluated. Reassessment of a
// retained execution calls Suite.Run directly without invoking the Target again.
// The Host owns environment sessions, persistence, repeats, and safe resumption.
//
// # Experiments and comparison
//
// Experiment evaluates an offline Dataset through Suite.Run with bounded case
// concurrency. The default case limit is four and the default Suite limit is
// one; explicitly increasing both limits multiplies direct assessment calls.
// NewExperimentReport validates and snapshots already collected CaseResult facts
// without executing work. ExperimentSummary counts complete cases separately
// from partial cases, preserves assessment coverage even when no Metric exists,
// and aggregates only completed, explicitly declared assessments.
//
// ExperimentReport.Compare requires the same FixtureID and set of Case IDs.
// Numeric differences pair case, AssessmentID, and full Metric identity rather
// than subtracting means over different successful subsets. Matched and missing
// observation counts remain explicit. Decision differences additionally require
// identical policy identity; no significance or repeat-at-k estimate is implied.
// JSON decoding rejects unknown Report, Decision, and Metric members, including
// nested details, while metadata and identity parameters retain open JSON values.
// Owned identities, feedback, policies, and execution reasons must be valid
// UTF-8 at admission; borrowed generic subjects and outputs keep their own contracts.
//
// Domain vocabularies live outside the kernel: judge supplies generic
// model-backed evaluation, text owns generated-text metrics, ranking owns
// provider-neutral ranking metrics, and trajectory owns deterministic Agent
// execution evaluation. The swebench package exchanges typed prediction and
// report files with the official harness; it does not reproduce its grader.
// New domains implement Evaluator directly and do not
// depend on another domain's vocabulary.
package eval
