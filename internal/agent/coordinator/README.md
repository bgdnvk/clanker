# Coordinator Package

This package runs the parallel investigation workflow that follows decision tree outputs. The code is split into a few focused files so the main `Coordinator` type in `coordinator.go` stays small and testable.

## Files

| File             | Purpose                                                                                                                                                         |
| ---------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `coordinator.go` | Entry point. Turns decision tree nodes into `AgentConfig`s, runs the dependency scheduler, launches agents, and aggregates results.                             |
| `agent_types.go` | Declares each agent type (log, infrastructure, metrics, etc.) plus their dependency metadata (required data, provided data, execution order).                   |
| `scheduler.go`   | Groups agents by execution order and checks whether dependencies are satisfied via the shared data bus before launch.                                           |
| `state.go`       | Shared concurrency primitives: `SharedDataBus` (dependency payload store), `AgentRegistry` (thread-safe list + counters), and `CopyContextForAgent`.            |
| `operations.go`  | Maps agent types to the AWS commands/LLM operations they should run. Keeps the switchboard out of core logic.                                                   |
| `playbooks.go`   | AWS helpers (lightweight service discovery, log sampling, keyword helpers) plus factory helpers (`newParallelAgent`, `persistProvidedData`, `lookupAgentType`). |

## Flow Overview

1. Decision tree produces `[]*dt.Node` with agent names and parameters.
2. `Coordinator.SpawnAgents` builds a map of `AgentConfig`s, keeping the highest-priority entry per agent name.
3. `DependencyScheduler.Plan` sorts configs into `[]OrderGroup` by execution order.
4. Each order group launches agents whose dependencies are satisfied on the `SharedDataBus`. Every agent run is recorded in the `AgentRegistry`.
5. `runParallelAgent` executes the precomputed operations for that agent type. When it succeeds, `persistProvidedData` pushes any promised data (e.g., `logs`, `service_config`) onto the bus for downstream agents.
6. `AggregateResults` folds all completed agent outputs into a single `model.AWSData` blob and adds metadata (counts, decision path, timestamp).

## Extending

-   **New agent type**: add it to `agent_types.go`, mention it in the decision tree, and register its LLM operations inside `operations.go`.
-   **New dependency**: include the provided/required data string in the relevant `AgentType`. Downstream agents reference those string keys when checking readiness.
-   **New AWS playbook**: keep helpers in `playbooks.go` (or a subpackage) instead of `coordinator.go`; call them from the operation generator.

This layout keeps orchestration (scheduling, lifecycle, aggregation) isolated from service-specific details and makes it easier to test each layer independently.
