# docs

Repository-wide documentation. Anything that belongs to one module lives in
that module's GoDoc and checked examples instead.

| Path | Contents |
|---|---|
| [`agent-capabilities.md`](agent-capabilities.md) | General agent and harness architecture: composition, extension boundaries, lifecycle guarantees, and acceptance criteria |
| [`bug-reports/`](bug-reports/README.md) | Reproducible defect diagnoses and their current resolution |
| [`comparative-analysis/`](comparative-analysis/README.md) | Framework-level comparison: how Scope and its peers trade off contracts, state, effects, recovery, and dependency boundaries |

Point-in-time audit reports do not live here. A finding either becomes a code
change, an executable gate, or a recorded ruling in the document that owns the
rule. A report that only describes a past state goes stale and starts
misleading. The evidence for what changed is in the git history.
