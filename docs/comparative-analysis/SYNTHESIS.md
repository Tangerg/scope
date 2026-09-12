# Agent framework synthesis

## The conclusion first

Scope is not a general agent framework that leads on every dimension. It is
strong at a narrower and harder goal: **letting multi-step, interruptible agent
work with child executions resume after a process restart with an explicit
meaning**. To get there it separates decision from external work and leaves
product sessions, history, and infrastructure to the host.

Pi represents the excellent answer at the other end: **a high-productivity
model-tool loop behind a very small embedding surface**. Its low-level agent
runtime is direct, flexible, and easy to modify, but the loop itself provides
neither Scope-style side-effect identity nor complete recovery. Pi's newer
harness types already describe an operation log, lanes, replay, and snapshots;
at this baseline, however, the main run methods still return
`HarnessNotImplemented`, so they cannot count as delivered capability.

The rest are not points on a scale between those two. Eino centers on typed
graph composition, ADK on sessions, agent trees, and a workflow graph,
Microsoft Agent Framework on streaming agents and workflow executors, Spring AI
on model composition inside the Spring ecosystem, Embabel on goal-driven
dynamic planning, and tRPC-Agent-Go on a wider surface spanning agents, graphs,
and productized components.

This round therefore produces no overall ranking. The right question is: **which
execution semantics fit the target system, and what complexity is accepted for
them.**

## Boundaries come before feature lists

### The application layer does not score the framework

Flame's correct role is an external consumer of Scope:

```text
Flame CLI / Desktop
        ↓
Flame runtime
        ↓
Scope framework modules
```

`flame/runtime/go.mod` depends directly on Scope's `agent`, `core`, `mcp`,
`otel`, `skills`, `tools`, `a2a`, and model leaf modules; Scope does not depend
on Flame. That direction shows application and framework are genuinely layered,
which is more convincing than putting a demo application in the framework
repository.

But Flame only proves Scope's complex execution model has a real consumer. It
does not prove every agent application needs the same execution model.
Likewise, the Pi coding agent's terminal experience, tRPC OpenClaw's tool
count, ADK's web and CLI, and the Embabel shell must not convert into kernel
scores.

### GitNexus no longer poses as a peer

GitNexus's core is code indexing, a knowledge graph, and a retrieval interface.
It has no agent execution contract, tool loop, or recovery model comparable to
the others. Putting it in the primary matrix would conflate retrieval product
capability with agent framework semantics.

It keeps one adjacent value: a reminder that an agent framework's tool protocol
must not pass off "context we found" as "verified fact". That is a question of
expressing evidence, not a framework ranking.

## Framework shapes

| Project | Core abstraction | Primary state owner | Where external work happens | Design center |
| --- | --- | --- | --- | --- |
| Scope | `Definition` + `Execution` | The Execution snapshot; product data belongs to the host | `Step` describes an `Effect`; the runtime executes it | Recoverable managed execution |
| Pi | `StreamFn`, `agentLoop`, `Agent` | A mutable `AgentState` and message history | The loop calls models and tools directly | A compact embeddable tool loop |
| Eino | `Runnable[I,O]`, Graph/Workflow | Graph state, context, and node data | Nodes or components execute directly | Typed component and graph composition |
| tRPC-Agent-Go | `Agent`, `Invocation`, Graph | Invocation, Session, graph state | Agents, runners, or nodes execute directly | A broad-surface agent runtime |
| ADK Go | Sealed `Agent`; workflow `Node`/`Edge` | Session, InvocationContext, and workflow run state | Agents, nodes, tools, and callbacks execute directly | Session-driven agent trees and graph scheduling |
| Microsoft AF Go | `RunFunc`, Agent, Executor | Sessions, workflow state, a checkpoint store | Agents or executors execute directly | Streaming agents and workflows |
| Spring AI | Model, ChatClient, Advisor | Application sessions, Memory, Spring beans | Clients, advisors, and tool callbacks execute directly | Spring-ecosystem model composition |
| Embabel | Blackboard, Action, AgentProcess | The blackboard and AgentProcess | Actions execute directly | GOAP dynamic planning |

The table reveals an important correction: Scope's difference is not that it
"also has workflows", but that **a decision step does not perform external work
directly**. Most peers choose a more direct calling model, which is lighter on
the simple path and needs extra constraints for exact recovery.

## The eight framework dimensions

### 1. Protocol and provider boundary

Scope's `core.Message`, `Part`, tool, and model contracts depend on no specific
SDK, and model implementations are split into leaf modules. That boundary is
the cleanest, and it leaves conversion cost explicitly with the adapter.

Pi's `pi-ai` offers a unified multi-provider interface with consistent stream
events and is very pleasant in practice; but its public types retain
provider-specific fields such as the Google thought signature and the OpenAI
namespace, and the package itself depends on several SDKs directly. It pursues
compatibility efficiency, not the purest domain kernel.

Spring AI, ADK, Microsoft Agent Framework, and tRPC-Agent-Go all offer a
unified model surface, but each to some degree lets framework types, an
ecosystem container, or a concrete SDK into the same dependency closure. Eino
separates its interfaces from its provider extension repository fairly well.
Embabel focuses on the planning model, so protocol purity is not its primary
design center.

The conclusion is not "purer is better". A pure kernel lowers transitive
dependencies and vendor coupling, but it adds adaptation work and faces a
lowest-common-denominator risk. Scope must keep proving its public protocol is
both stable and able to express provider features — not merely that it has no
SDK dependency.

### 2. The minimal execution contract

Scope's current waist is five methods:

- `Definition`: `Descriptor`, `Start`, `Restore`
- `Execution`: `Step`, `Snapshot`

An earlier draft called this a "four-method waist", which was factually wrong.
The split expresses the definition, instance, and recovery boundaries very
clearly, but every implementation faces the recovery protocol from day one.

Pi's minimum embedding surface is smaller: inject a `StreamFn` and tools and
`agentLoop` runs; the stateful `Agent` then adds events and control on top.
Microsoft Agent Framework's `RunFunc`, Eino's `Runnable`, and Spring's
`ChatClient` also have a lighter ordinary calling path than Scope.

Scope's waist is therefore "semantically narrow", not "cheapest to start with".
When a caller only needs one model call or a short tool loop, managed execution
must not be the forced entry point. The current architecture allows direct
`chatclient` use; that two-layer path is necessary and must not be merged back
together.

### 3. State ownership

Scope draws the clearest state boundary: the execution snapshot belongs to
framework execution semantics, while product sessions, chat history,
credentials, and storage belong to the host. That lowers the risk of the
framework becoming an application container.

Pi's low-level `AgentState` holds messages and tools together with runtime and
UI-adjacent state such as `isStreaming`, `streamingMessage`, and pending tool
calls. That makes state subscription and interactive applications very direct,
but the persistence boundary is not naturally layered the way Scope's is.

ADK and tRPC pass state centrally through an invocation or session context,
which is convenient but tends toward a wide context. Eino's graph state and
Embabel's blackboard are natural for complex composition and rely more on
runtime conventions or dynamic data constraints. Spring AI usually leaves state
ownership to the application and to Spring components.

### 4. The side-effect boundary

Scope's `Step` makes only a deterministic decision and emits an `Effect`;
external I/O is executed by the runtime, and the result returns as a Signal on
the next step. That gives a side effect a recordable identity and lets retry,
idempotency, cancellation, and recovery share one set of semantics.

Pi, ADK, Eino, Spring AI, Embabel, and most tRPC and MAF paths call models and
tools directly inside a loop, action, node, advisor, or executor. Direct calls
are not a smell: they remove intermediate representation and boilerplate, and
debugging an ordinary call matches language intuition better. The cost is that
reaching Scope's level of recovery precision requires separately recording call
identity, arguments, completion result, and replay policy.

Scope has the clearest structural advantage here and pays the highest authoring
cost. Any flow that cannot show a recovery, audit, or strict-lifecycle benefit
should not be forced into an Effect for the sake of uniformity.

### 5. Orchestration and child execution lifecycle

What each framework calls a "workflow" is not the same capability:

- Scope's managed Workflow is only for child execution with its own Process, so
  each item has identity, budget, cancellation, snapshot, and tree recovery.
  Ordinary synchronous data flow can use the separate `flow` library.
- Eino's and tRPC's graphs are closer to node data flow and execution graphs,
  and express arbitrary topology more naturally.
- Beyond Sequential, Parallel, and Loop agents, ADK also implements an
  independent workflow graph: nodes and edges, static and dynamic scheduling, a
  concurrency ceiling, schema validation, HITL, and resume cooperating through
  session and run state. Two workflow surfaces currently coexist.
- Microsoft Agent Framework's executors and workflows support edges, events,
  checkpoints, and RequestPort, which suits workflow-driven human interaction.
- Embabel's actions and goals are selected dynamically by GOAP, with no
  requirement to fix the whole graph in advance.
- Spring AI and Pi's low-level runtime do not treat general workflow as core;
  orchestration usually belongs to the application or a higher layer.

Scope's closed stage vocabulary buys a stable schema and recovery semantics at
the price of arbitrary graph composition. That trade-off suits managed child
processes and must not be advertised as a replacement for every orchestration
form.

### 6. Persistence and recovery

| Project | Confirmed this round | What must not be conflated |
| --- | --- | --- |
| Scope | Execution and tree snapshots, effect settlement feedback, child execution recovery | Host product data is not held on its behalf by a framework snapshot |
| Pi | The low-level agent keeps in-memory state; harness types declare an operation log, lanes, snapshots, and replay | The harness's `prompt`, `resume`, `compact`, and tree navigation paths are still unimplemented |
| Eino | Graph checkpoints and state serialization | Arbitrary external side effects inside a node gain no effect identity automatically |
| tRPC | Session and graph checkpoints, parent checkpoints | Not every agent side effect in a wide runtime is uniformly replayable |
| ADK | Workflow run state can be written into the session, and a paused state can be rebuilt from event history and resumed | External calls in a node have no unified effect identity, and workflow construction still explicitly lacks graph fingerprint validation |
| Microsoft AF | A checkpoint store, workflow state, RequestPort | Direct external calls inside an agent still need their own idempotency boundary |
| Spring AI | Chat memory and external storage integration | No unified execution snapshot semantics |
| Embabel | AgentProcess and blackboard persistence abstractions | External work in an action does not automatically become a replayable effect |

One of the earlier draft's biggest evidence problems was equating "has
checkpoint or session types" with "complete recoverable execution". The current
standard requires answering four questions together: what was saved, whether
the external call has an identity, where recovery resumes, and whether a result
can be submitted twice.

### 7. Extension and observability

Scope keeps the kernel neutral with middleware, listeners, and a separate
OpenTelemetry module. That boundary is clear, but it requires maintaining
correlation rules across executions, effects, and recovery.

Pi's agent events and named lifecycle callbacks embed easily into an
application, and `pi-telemetry` defines a typed, vendor-neutral telemetry
schema. It avoids making the agent depend on OpenTelemetry directly, at the
price of its own protocol that has to be adapted to external telemetry systems.

Eino's callbacks, ADK's callbacks and plugins, Spring's advisors, and Microsoft
AF's middleware and context providers each carry their own ecosystem advantage.
tRPC has several callback and extension surfaces at once — broad coverage, but
a higher cost to unify the mental model. Embabel leans on Spring events and
annotations for high ecosystem consistency and lower framework independence.

The number of extension points is not a quality metric. What matters is whether
one event has exactly one authoritative lifecycle, whether an extension failure
changes main execution semantics, and whether correlation survives recovery.

### 8. Dependencies and the application boundary

Scope's multi-module leaf structure gives consumers fine dependency control and
carries a real cost: version coordination, discoverability, cross-module
testing, and release management all get harder. The earlier draft treated many
modules purely as an advantage, which was incomplete.

Pi separates `pi-ai`, `pi-agent-core`, and `pi-coding-agent` inside a monorepo,
so its application-layer separation holds. But `pi-agent-core`'s export surface
also includes harness, session, search, and code tooling that leans toward a
coding agent, so the framework boundary is widening. What should be judged here
is the dependency and export surface, not a denial of Pi's application layering.

Eino's core-plus-extension repositories, Spring AI's starter and module system,
and the module organization of MAF, ADK, and tRPC each have ecosystem context.
Go module count cannot score across languages directly; the question is whether
the minimum consumer is forced to pull in protocols, SDKs, or product
capabilities it does not need.

## What each framework fits best

| Target problem | The more natural candidate | Why |
| --- | --- | --- |
| Interruptible, recoverable long-running tasks with child executions | Scope | Side effects, snapshots, and child lifecycle are one kernel semantics |
| Quickly embedding a high-quality model-tool loop | Pi | A small StreamFn and Agent surface, with direct message and tool events |
| Typed components and complex graph composition | Eino | Runnable and Graph are first-class abstractions |
| A broad one-stop agent surface in Go | tRPC-Agent-Go | Agent, Runner, Graph, and Session cover a lot |
| Session-driven agent hierarchies, graph workflows, and the Google ecosystem | ADK Go | Invocation, Session, agent trees, and the workflow scheduler cooperate |
| Streaming agents combined with explicit workflow executors | Microsoft Agent Framework Go | RunFunc, Executor, checkpoints, RequestPort |
| Model calls, advisors, tools, and RAG in a Spring application | Spring AI | Natural integration with the Spring container and ecosystem |
| Runtime goal-driven dynamic task planning | Embabel Agent | The blackboard and GOAP action model |

These are not exclusive choices. One product can use different layers for a
direct model call, a short tool loop, and managed long-running execution. What
matters is not compressing every path into the heaviest abstraction.

## Eval is a support layer, not a kernel bonus

Scope's `eval` root package uses `Evaluator[T]` as its waist and already has
datasets, experiments, suites, composites, comparison, quantile summaries, and
a structured report. The report separates a verdict, a normalized score, and a
raw measurement carrying its own unit and direction, so it no longer forces
every metric into a `[0,1]` scalar. Text, model-judge, and ranking vocabularies
live in `eval/text`, `eval/judge`, and `eval/ranking`.

It is still not an application-level experiment platform: persistent datasets,
artifacts, trace correlation, project catalogs, dashboards, and release
workflows belong to Flame or a separate harness. This dimension cannot decide
an agent runtime ranking in reverse, and it must not make `eval` depend on
`agent`. See [the eval support-layer boundary](EVAL.md) for details.

## The corrected judgment on Scope

### Advantages supported by source and a real consumer

1. **Recoverable execution is kernel semantics, not a storage plugin.** `Step`,
   Effect, Signal, Snapshot, and Restore jointly determine how recovery works.
2. **The host boundary genuinely exists.** Flame consumes Scope through module
   dependencies, and Scope holds no application session or UI in reverse.
3. **Managed orchestration and ordinary data flow are already separated
   conceptually.** That stops Workflow from degenerating into a second general
   DAG.
4. **Provider dependencies are isolated fairly thoroughly.** Consumers select
   leaf implementations as needed.

### Structural costs that can no longer be avoided

1. **The ordinary path is not prominent enough.** Scope allows direct
   `chatclient` use, but the overall narrative can make users think every call
   should enter managed execution.
2. **The Effect model raises the authoring bar.** It is worth it only when
   recovery, audit, cancellation, or idempotency benefits are clear.
3. **Module count raises governance cost.** Boundary purity has to be evaluated
   together with versioning, documentation, the test matrix, and
   discoverability.
4. **A closed workflow vocabulary limits expressive freedom.** That is the
   price of a stable protocol, not an unconditional advantage.
5. **Flame is currently the main validation.** One strong consumer proves the
   design is not theoretical; it does not yet prove generality across external
   scenarios.

### Where the verification items stand

- **The ordinary path**: the root README shows `chatclient` directly; the
  minimal tool loop comes from `chatclient.NewSingleBatchToolMiddleware`; checked examples
  in capability modules stop the documentation from collapsing back into a
  managed-execution-only narrative.
- **Custom Execution**: `agenttest.RunDefinitionConformance` verifies the
  descriptor, independent state, snapshot and restore, and hidden mutable
  input; the repository does not manufacture a second Execution API.
- **Cross-process causality**: the durable wire carries version, incarnation,
  and a stable effect identity; `otel/agent`'s restore tests verify restored
  activation, parent-child spans, and incarnation attribution.
- **Provider features**: Core's `metadata.Extensions` preserves namespaced,
  JSON-safe extensions without leaking a provider SDK or an arbitrary parameter
  map into the protocol.
- **Multi-module compatibility**: CI runs `GOWORK=off` tidy and compile per
  module, so the workspace can no longer mask a stale internal version.

"Find another real product consumer" remains ecosystem validation, not an
application feature Scope should fake inside the repository. The next phase
keeps prioritizing observation of a real host's composition cost and traces
over expanding built-in agent patterns, provider count, or capabilities that
belong to Flame.

## Specific biases corrected this round

- After removing GitNexus, the old set actually held 7 frameworks including
  Scope; adding Pi makes the primary comparison 8 frameworks.
- GitNexus moved out of the peer matrix and became adjacent-system evidence.
- Application capabilities such as Flame, the Pi coding agent, and OpenClaw
  were all stripped from the framework score.
- Scope's core execution contract was corrected from a wrong "four methods" to
  the five-method split.
- Scope's valid states are read as 9 per the current implementation, replacing
  the earlier draft's self-contradictory counts.
- Module count, built-in tool count, and repository size are no longer treated
  as maturity.
- An interface declaration, a checkpoint type, or a session record is no longer
  automatically treated as complete recovery capability.
- Overall scores and "leads across the board" conclusions were removed in
  favor of design center, fitting scenario, and structural cost.
