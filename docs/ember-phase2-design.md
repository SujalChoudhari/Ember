# Ember Phase 2 design — declarative deployment engine

This document describes the Phase 2 implementation in this repository. It adds a
small, versioned `ember/v1` declarative contract for `ember.yaml` and `ember.json`,
a deterministic dependency graph, previewable plan entries, and an apply path over
the existing in-memory and PostgreSQL authorities. The engine owns desired-state
comparison; the existing resource, operation, audit, and authorization boundaries
remain authoritative for mutations.

The format is Ember-owned and intentionally not a Bicep or Azure compatibility
claim. Phase 2 supports `resourceGroup` and `Ember.Blob/bucket` resources, parameters,
tags, outputs, lifecycle policy, and secret references. Secret values are never
part of the document model or plan output.

## HLD — high-level design

```mermaid
flowchart LR
    operator["Operator or CI\nember.yaml / ember.json"] --> engine["internal/ember.DeclarativeEngine"]
    engine --> contract["ParseDocument\nDocument.Validate"]
    contract --> graph["BuildDependencyGraph\ndeterministic topological order"]
    graph --> plan["Plan\ncreate / update / no-op / delete"]
    plan --> gate["ApplyOptions\nexplicit destructive approval"]
    gate --> authority{ "DeclarativeAuthority" }
    authority -.-> memory["internal/ember.Store\nexplicit test/dev adapter"]
    authority -.-> postgres["internal/persistence/postgres.Store\nPostgreSQL adapter"]
    memory --> memstate["in-memory desired state\noperations + audit"]
    postgres --> db[("PostgreSQL\nresources / operations / audit_events / declarative_states")]
```

The engine reads the current managed state through `DeclarativeAuthority`, computes
an order-stable plan, and refuses malformed documents, unknown dependencies, and
cycles before any authority mutation. Apply skips converged entries, executes
creates and updates in dependency order, and executes stale deletes from children
to parents. Destructive updates and deletes require `ApproveDestructive`; a blocked
apply returns its preview and performs no destructive mutation.

## LLD — low-level execution flow

```mermaid
flowchart TB
    source["ember.yaml or ember.json bytes"] --> parse["ParseDocument\nYAML KnownFields / JSON DisallowUnknownFields"]
    parse --> validate["Document.Validate\nember/v1, schema, references, tags, lifecycle"]
    validate --> dependency["BuildDependencyGraph\nparent + dependsOn edges"]
    dependency --> current["ListDeclarativeResources\nread managed state for scope"]
    current --> hash["Canonical resource JSON\nSHA-256 spec hash"]
    hash --> diff["diffResourceSpecs\npath-level PlanChange entries"]
    diff --> preview["Plan\nentries + order + blocked edges"]
    preview --> approval{ "Destructive entry approved?" }
    approval -->|"no"| blocked["Mark blocked\nreturn ErrDestructiveApprovalRequired\nno mutation"]
    approval -->|"yes"| apply["Apply entries in plan order"]
    apply --> createUpdate["ApplyDeclarativeResource\ncreate or update"]
    createUpdate --> save["SaveDeclarativeState\nlogical ID + resource ID + hash"]
    apply --> delete["ApplyDeclarativeResource delete\nstale children before parents"]
    delete --> cleanup["DeleteDeclarativeState\nidempotent after resource FK cascade"]
    save --> result["ApplyResult\nplan + operation IDs"]
    cleanup --> result
```

`Plan.Order` and every `PlanEntry.Order` expose the execution order. Parent and
explicit dependency edges are normalized and sorted before the topological pass.
For stale resources, persisted `ParentID` values provide a deterministic
child-before-parent deletion order. The PostgreSQL declarative row is linked to the
resource by a foreign key; cleanup therefore treats a row already removed by the
successful resource deletion as converged.

## Class/interface — authority and plan contracts

```mermaid
classDiagram
    class DeclarativeEngine {
        -authority DeclarativeAuthority
        +PlanDocument(principal, data, filename, scope) Plan
        +Plan(principal, document, scope) Plan
        +ApplyDocument(principal, data, filename, options) ApplyResult
        +Apply(principal, document, options) ApplyResult
    }
    class DeclarativeAuthority {
        <<interface>>
        +ListDeclarativeResources(principal, scope) DeclarativeResourceState[]
        +ApplyDeclarativeResource(principal, spec, parentID, action, requestID, correlationID) Resource, Operation
        +SaveDeclarativeState(principal, state, requestID, correlationID) error
        +DeleteDeclarativeState(principal, scope, logicalID, requestID, correlationID) error
    }
    class Store {
        <<internal/ember.Store>>
        +declarative map
        +resources map
        +operations map
        +audit []AuditEvent
    }
    class PostgresStore {
        <<internal/persistence/postgres.Store>>
        +DB() sql.DB
        +ListDeclarativeResources()
        +ApplyDeclarativeResource()
        +SaveDeclarativeState()
        +DeleteDeclarativeState()
    }
    class Document {
        +APIVersion string
        +Parameters map
        +Resources ResourceSpec[]
        +Outputs map
        +Tags map
        +Validate() error
    }
    class ResourceSpec {
        +ID string
        +Type string
        +Name string
        +Scope string
        +Parent string
        +DependsOn string[]
        +Properties map
        +Tags map
        +Lifecycle LifecyclePolicy
        +Secrets SecretReference[]
    }
    class Plan {
        +DocumentHash string
        +Order string[]
        +Edges DependencyEdge[]
        +BlockedEdges DependencyEdge[]
        +Entries PlanEntry[]
    }
    class PlanEntry {
        +LogicalID string
        +ResourceID string
        +Action PlanAction
        +Order int
        +Changes PlanChange[]
        +BlockedBy string[]
        +OperationID string
    }

    DeclarativeEngine --> DeclarativeAuthority : reads and applies
    DeclarativeEngine --> Document : parses and validates
    DeclarativeEngine --> Plan : produces
    Document "1" *-- "0..*" ResourceSpec : contains
    Plan "1" *-- "1..*" PlanEntry : contains
    Store ..|> DeclarativeAuthority : in-memory adapter
    PostgresStore ..|> DeclarativeAuthority : PostgreSQL adapter
```

The interface keeps desired-state orchestration independent from persistence. Both
adapters return the existing `Resource` and `Operation` contracts, while the
engine records each managed logical ID with its canonical spec hash. Operation IDs
and correlation IDs returned by an apply are the linkage used to inspect the
existing operation and audit records.

## Sequence — PostgreSQL-backed plan/apply

```mermaid
sequenceDiagram
    actor Operator
    participant Engine as internal/ember.DeclarativeEngine
    participant Parser as ParseDocument
    participant Authority as DeclarativeAuthority
    participant PG as postgres.Store
    participant DB as PostgreSQL

    Operator->>Engine: ApplyDocument(principal, bytes, filename, options)
    Engine->>Parser: ParseDocument + Document.Validate
    Parser-->>Engine: normalized Document
    Engine->>Engine: BuildDependencyGraph
    Engine->>Authority: ListDeclarativeResources(principal, scope)
    Authority->>PG: SELECT declarative_states for scope
    PG->>DB: Read managed state
    DB-->>PG: state rows and resource IDs
    PG-->>Authority: DeclarativeResourceState list
    Authority-->>Engine: current managed state
    Engine->>Engine: hash specs and build Plan entries

    alt malformed document or dependency cycle
        Engine-->>Operator: error; no authority mutation
    else destructive entry without approval
        Engine-->>Operator: Plan + ErrDestructiveApprovalRequired
    else approved create or update
        loop each ordered create/update entry
            Engine->>Authority: ApplyDeclarativeResource(spec, action, request, correlation)
            Authority->>PG: resource transaction
            PG->>DB: resource + operation + audit rows
            DB-->>PG: committed operation and audit linkage
            PG-->>Authority: Resource + Operation
            Engine->>Authority: SaveDeclarativeState(canonical hash)
            Authority->>PG: upsert declarative_states
            PG->>DB: commit desired-state record
        end
        Engine-->>Operator: ApplyResult with operation IDs
    else approved stale deletes
        loop child before parent
            Engine->>Authority: ApplyDeclarativeResource(stale spec, delete, request, correlation)
            Authority->>PG: delete resource and record operation/audit
            PG->>DB: delete resource; declarative state cascades
            DB-->>PG: committed delete
            Engine->>Authority: DeleteDeclarativeState cleanup
            Authority-->>Engine: already converged or deleted
        end
        Engine-->>Operator: ApplyResult with delete operation IDs
    end
```

## Scope and non-goals

Phase 2 does not add a new HTTP or CLI surface, a new provider, automatic secret
resolution, rollback orchestration, or implicit approval. It provides the
versioned engine and persistence contract used by the existing Ember control-plane
code. Public deployment, paid resources, and Azure/Bicep compatibility remain
outside this issue.
