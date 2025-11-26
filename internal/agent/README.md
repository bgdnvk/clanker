# Agent Package

The `internal/agent` package powers Clanker's investigative assistant. It accepts a natural-language query, semantically analyzes the request, spawns specialized workers, and merges AWS telemetry into a response-focused context block.

## Architecture

```
agent/
├── agent.go          # High-level orchestrator and public entry points
├── coordinator/      # Dependency-aware parallel execution driver
├── decisiontree/     # Intent rules that map queries to agent types
├── memory/           # Rolling knowledge of previous investigations
├── model/            # Disabled for now - Shared structs and type aliases
└── semantic/         # Lightweight NLP classifier for intents
```

### `agent`

The core `Agent` type wires everything together:

-   Runs semantic analysis and consults the `memory` package for similar incidents.
-   Traverses the `decisiontree` to decide which specialist agents to spawn.
-   Uses the `coordinator` to execute AWS operations (via `internal/aws`) in parallel with dependency ordering.
-   Aggregates results and produces a final context string for downstream LLM prompts.

### `model`

Defines common data structures—`AgentContext`, `AgentDecision`, `AWSFunctionCall`, and type aliases for shared maps. This keeps cross-package dependencies stable and avoids circular imports.

### `memory`

Stores prior `QueryContext` entries, tracks service health, and surfaces similar investigations. The agent consults it before acting so we can short-circuit repeated incidents.

### `semantic`

Provides `Analyzer`, a lightweight keyword/intent classifier. It extracts urgency, target services, desired data types, and sets the stage for decision-tree traversal without requiring heavyweight NLP calls.

### `decisiontree`

Encodes rule-based agents selection. Each node specifies:

-   A condition (e.g., keyword presence) evaluated against the query.
-   A priority and agent types to execute.
-   Optional parameters forwarded to the coordinator.
    The tree keeps logic declarative and easy to extend without touching orchestration code.

### `coordinator`

Handles dependency-aware, parallel execution of specialized agents:

-   Groups agents by dependency order and ensures required data exists before execution.
-   Shares results across agents through a synchronized map.
-   Wraps AWS client calls (`ExecuteOperation`, `ExecuteOperations`, `ExecCLI`).

## Development Notes

-   Keep shared structs inside `model` to avoid circular imports.
-   When adding a new agent type, update both the decision tree and the coordinator's `lookupAgentType`/`getOperationsForAgentType` helpers.
-   `agent.go` is intentionally lean; push complex logic into subpackages so orchestration remains readable.
-   Run `gofmt` after edits and ensure `go build ./...` stays green.
