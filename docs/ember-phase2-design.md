# Ember Phase 2 design — declarative deployment engine

This document describes the Phase 2 implementation in this repository. It adds a
small, versioned `ember/v1` declarative contract for `ember.yaml` and `ember.json`,
a deterministic dependency graph, previewable plan entries, and an apply path over
the existing in-memory and PostgreSQL authorities. The engine owns desired-state
comparison; the existing resource, operation, audit, and authorization boundaries
remain authoritative for mutations.

The format is Ember-owned and intentionally not a Bicep or Azure compatibility
claim. Phase 2 supports `resourceGroup` and `Ember.Blob/bucket` resources, parameters,
tags, outputs, lifecycle policy, and secret references. Secret values are not
resolved by this engine. Plan changes and persisted per-resource `SpecJSON`
apply a bounded redaction policy: secret-reference names/keys and values below
secret-, key-, password-, token-, credential-, authorization-, and private-key-
like property names are replaced with `[REDACTED]`. This is not arbitrary secret
storage: values under names outside that policy can still be retained, so
callers must use `secretRefs` and avoid inline secrets.

## HLD — high-level design

```mermaid
flowchart LR
    operatorNode["Operator or CI\nember.yaml / ember.json"] --> engineNode["internal/ember.DeclarativeEngine"]
    engineNode --> contractNode["ParseDocument\nDocument.Validate"]
    contractNode --> dependencyGraph["BuildDependencyGraph\ndeterministic topological order"]
    dependencyGraph --> planNode["Plan\ncreate / update / no-op / delete"]
    planNode --> gateNode["ApplyOptions\nexplicit destructive approval"]
    gateNode --> authorityNode{"DeclarativeAuthority"}
    authorityNode -.-> memoryNode["internal/ember.Store\nexplicit test/dev adapter"]
    authorityNode -.-> postgresNode["internal/persistence/postgres.Store\nPostgreSQL adapter"]
    memoryNode --> memoryState["in-memory desired state\noperations + audit"]
    postgresNode --> databaseNode[("PostgreSQL\nresources / operations / audit_events / declarative_states")]
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
    sourceNode["ember.yaml or ember.json bytes"] --> parseNode["ParseDocument\nYAML KnownFields / JSON DisallowUnknownFields"]
    parseNode --> validateNode["Document.Validate\nember/v1, schema, references, tags, lifecycle"]
    validateNode --> dependencyGraph["BuildDependencyGraph\nparent + dependsOn edges"]
    dependencyGraph --> currentState["ListDeclarativeResources\nread managed state for scope"]
    currentState --> hashNode["Canonical resource JSON\nSHA-256 spec hash"]
    hashNode --> diffNode["diffResourceSpecs\npath-level PlanChange entries"]
    diffNode --> previewNode["Plan\nentries + order + blocked edges"]
    previewNode --> approvalGate{"Destructive entry approved?"}
    approvalGate -->|"no"| blockedNode["Mark blocked\nreturn ErrDestructiveApprovalRequired\nno mutation"]
    approvalGate -->|"yes"| applyNode["Apply entries in plan order"]
    applyNode --> createUpdateNode["ApplyDeclarativeResource\ncreate or update"]
    createUpdateNode --> saveNode["SaveDeclarativeState\nlogical ID + resource ID + hash"]
    applyNode --> deleteNode["ApplyDeclarativeResource delete\nstale children before parents"]
    deleteNode --> cleanupNode["DeleteDeclarativeState\nidempotent after resource FK cascade"]
    saveNode --> resultNode["ApplyResult\nplan + operation IDs"]
    cleanupNode --> resultNode
```

`Plan.Order` and every `PlanEntry.Order` expose the execution order. Parent and
explicit dependency edges are normalized and sorted before the topological pass.
For stale resources, persisted `ParentID` values provide a deterministic
child-before-parent deletion order. The PostgreSQL declarative row is linked to the
resource by a foreign key; cleanup therefore treats a row already removed by the
successful resource deletion as converged.

## Secret handling boundary

The declarative input can carry arbitrary `ResourceSpec.Properties` values for
provider-facing resource configuration, so the document model is not a general
secret vault. The engine does not resolve secret references or promise that an
arbitrary property name is safe. Before a plan change is emitted or declarative
state is persisted, the bounded policy below replaces values with `[REDACTED]`:

- `secretRefs` retain their array/object shape, but each reference `name` and
  `key` is redacted;
- property/path names containing or equal to `secret`, `key`, `password`,
  `token`, `credential`, `authorization`, `bearer`, `connectionString`, or
  private-/API-/access-key-like names are redacted, including nested values;
- malformed persisted state fails closed with an input-safe error instead of being
  copied into a plan change;

The full canonical resource JSON is used only to calculate the convergence hash;
the persisted `SpecJSON` is the redacted, shape-preserving form. Callers must use
`secretRefs` and avoid inline secrets; values under names outside this bounded
policy can still be retained.

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
        Engine-->>Operator: error and no authority mutation
    else destructive entry without approval
        Engine-->>Operator: Plan + ErrDestructiveApprovalRequired
    else approved create/update
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
            PG->>DB: delete resource and declarative state cascades
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
