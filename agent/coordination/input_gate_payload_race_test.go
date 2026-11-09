//go:build race

package coordination

// Race instrumentation exercises the same admission and completion path;
// the ordinary test owns the full payload boundary without instrumentation cost.
const inputGateTestAnswerBytes = 64 << 10
