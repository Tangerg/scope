# Bug reports

Living reports contain only unresolved findings within their stated scope.

| Document | Scope |
|---|---|
| [`agent-findings.md`](agent-findings.md) | `agent` — the execution kernel, its built-in strategies, and `agenttest` |

No confirmed open findings are currently recorded in the reports above.

## Why one document

A report that describes a past state goes stale and starts misleading. When a
finding lands as a code change or an executable gate, **delete its entry** — the
evidence for what changed is in the git history, not in a resolved-items
archive. A document that has to be read with "which of these is still true?" in
mind has stopped being useful.

## What an entry contains

- **Symbols** with `file:line`, verified against a named HEAD.
- **What is wrong**, with the input or path that shows it. A defect that is
  currently unreachable says so and says what prevents it.
- **Evidence** where the claim is quantitative: an A/B benchmark, a profile, or
  a reproduction, not an estimate.
- **Fix layer** — the layer that owns the cause, not the line that shows the
  symptom.

## Keeping it honest

Re-verify every retained entry against the current HEAD before adding to the
document, and record the HEAD at the top. An entry that turns out to be wrong is
deleted and the correction noted, not quietly edited away.
