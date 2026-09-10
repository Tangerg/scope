# Agent framework comparison

This document set compares **agent frameworks**, not the applications built with
them.

That distinction changes the conclusions directly. A built-in terminal, a code
editor, search tools, a web UI, a CLI, and a count of ready-made agents show how
complete an application or a distribution is; they do not prove the framework
kernel is more general. What this round actually compares is protocol
abstraction, execution contracts, state ownership, external side effects,
recovery semantics, and dependency direction.

## What is compared

### The primary group

| Project | The framework center identified this round |
| --- | --- |
| Scope | Recoverable managed execution, explicit side effects, Host and framework separated |
| Pi | A compact model-tool loop, one multi-provider interface, an embeddable agent runtime |
| Eino | Typed Runnables and graph orchestration |
| tRPC-Agent-Go | A composite framework of agent runtime, graph execution, and productized components |
| Google ADK Go | Session-driven agents, workflow graph scheduling, and a callback system |
| Microsoft Agent Framework Go | Streaming agent invocation, workflow executors, and a human-request port |
| Spring AI | Spring-style models, ChatClient, advisors, tool calling, and RAG composition |
| Embabel Agent | Blackboard, actions, and GOAP dynamic planning |

### Excluded from the primary scoring

- **GitNexus** is a code knowledge graph and retrieval system, not an agent
  framework. It appears only as adjacent-system evidence for "how a tool result
  expresses facts, evidence, and uncertainty".
- **Flame** is Scope's real application-layer consumer, used to verify dependency
  direction and framework boundaries. It is not scored against frameworks.
- `pi-coding-agent`, tRPC OpenClaw, the ADK CLI and web UI, and the Embabel shell
  are likewise application or product layers, and none is converted into a
  framework-kernel advantage.

## The shared comparison frame

This round uses eight framework-level dimensions:

1. **Protocol and provider boundary**: whether the domain protocol is polluted by
   a specific SDK, transport, or product concept.
2. **The minimal execution contract**: how wide the core interface a framework
   requires is, and whether it carries too many responsibilities at once.
3. **State ownership**: who holds and persists messages, sessions, execution
   state, and orchestration state.
4. **Side-effect boundary**: whether model calls, tool calls, and I/O happen
   directly, or are described first and executed by the runtime.
5. **Orchestration and sub-execution lifecycle**: whether graphs, workflows, and
   dynamic planning have their own identity, budget, cancellation, and recovery
   semantics.
6. **Persistence and recovery**: distinguishing a session record, a graph
   checkpoint, a complete execution snapshot, and something declared but not yet
   implemented.
7. **Extension and observability**: whether middleware, callbacks, events, and
   telemetry form one composable extension surface.
8. **Dependency and application boundary**: whether the framework can be consumed
   by an independent application, and whether application capability seeps back
   into the kernel.

No overall score is given. Different frameworks have different design centers,
and compressing every trade-off into a single ranking would reintroduce the bias
this comparison exists to avoid.

## Evidence rules

- Local repository source and module manifests are authoritative; a homepage
  feature list never substitutes for an implementation.
- **Implemented**, **interface declared**, and **provided by the application
  layer** are distinguished explicitly.
- Another framework's trade-offs are explained by its own design goals; Scope's
  architecture is not treated as the only correct answer.
- A conclusion must land on a specific interface, type, dependency, or execution
  path.
- Size is used only to explain the cost of a boundary, never to judge design
  quality.

## Evidence baseline

Analysis date: 2026-08-30.

| Repository | Local commit |
| --- | --- |
| Scope | `3d0b2ba51c3ba09915b56aa1ebcacd0c7eb749fc` |
| Pi | `853a80d26c90a14c1886f0ebb8ffaae133ca2185` |
| Eino | `0e01b2a4e3050c4027bd61f2c2e2a519aa1e237c` |
| tRPC-Agent-Go | `91bde85eb243333b2b33fe89061f2218ede00c99` |
| Google ADK Go | `0da17d5183cc7affd4bdb7b4075f9e264bb598be` |
| Microsoft Agent Framework Go | `6aabdc7ea2d2af7ac5f673dd693a01d5e61bed35` |
| Spring AI | `e5e277fd08a017c5a3efc57aef377d2a067e91dd` |
| Embabel Agent | `6988f286544bb792bed35d8ae45812c446be082d` |
| GitNexus | `b059ab3541ea68c2ce292955fc367a5de04b39ea` |
| Flame | `a2e937181e111ca1c4e29d492605ee3838929002` |

Flame's working tree had many uncommitted changes, so only its stable module
dependency direction is used as evidence here; its volatile implementation
details are not cited.

## Document index

- [Synthesis](SYNTHESIS.md)
- [The eval support-layer boundary](EVAL.md)
- [Source evidence index](EVIDENCE.md)
- [Pi](pi.md)
- [Eino](eino.md)
- [tRPC-Agent-Go](trpc-agent-go.md)
- [Google ADK Go](adk-go.md)
- [Microsoft Agent Framework Go](agent-framework-go.md)
- [Spring AI](spring-ai.md)
- [Embabel Agent](embabel-agent.md)
- [GitNexus: adjacent-system evidence](gitnexus.md)

## How to read it

The main document first answers what each framework is actually optimizing for,
and then discusses Scope's real advantages and its structural costs. Each
project document keeps verifiable source landmarks and lists what is worth
learning from and what should not be copied.

The per-project documents in this directory are research notes rather than
Scope's own contract. They describe the peer projects as of the baseline
commits above and are not updated as those projects move.

For current Scope behavior and implemented composition boundaries, use the
[Agent package contract](../../agent/doc.go) and
[agent architecture](../agent-capabilities.md). The baseline comparison does
not override those contracts.
