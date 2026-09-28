package eval

import "errors"

var (
	ErrInvalidEvaluatorConfig = errors.New("eval: evaluator configuration is invalid")
	ErrInvalidAssessment      = errors.New("eval: invalid assessment")
	ErrInvalidMetric          = errors.New("eval: invalid metric")
	ErrInvalidScore           = errors.New("eval: invalid score")
	ErrInvalidReport          = errors.New("eval: invalid report")
	ErrInvalidCase            = errors.New("eval: invalid case")
	ErrInvalidDataset         = errors.New("eval: invalid dataset")
	ErrInvalidExperiment      = errors.New("eval: invalid experiment")
	ErrInvalidComparison      = errors.New("eval: invalid comparison")
	ErrNotEvaluated           = errors.New("eval: assessment was not evaluated")
)
