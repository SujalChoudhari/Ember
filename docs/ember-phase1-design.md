# Ember Phase 1 design

This document describes the implementation currently in this repository. It is intentionally limited to the verified Phase 1 boundary: an HTTP API and CLI over a Go control-plane interface, PostgreSQL 16.14 for authoritative resource/operation state, and an Ember-owned local filesystem provider for Blob bytes.

The API is Azure-shaped rather than Azure-compatible. The diagrams show both the production PostgreSQL adapter and the explicit in-memory adapter used by deterministic local contract tests.

## 1. High-level design (HLD)

```mermaid
flowchart LR
    user[Local operator or client]
    cli[cmd/ember HTTP-only CLI]
    daemon[cmd/emberd HTTP server]
    api[internal/ember.Server\n/api/v1 routing and request IDs]
    auth[internal/ember.Auth\nmode-0600 bearer-token file]
    cp[ControlPlane interface]
    memory[internal/ember.Store\nexplicit test/dev adapter]
    pgstore[internal/persistence/postgres.Store\nproduction control-plane adapter]
    db[(PostgreSQL 16.14\nPhase 1 schema)]
    files[internal/ember.FileStore\nprivate filesystem provider]
    disk[(Blob root\nstaging / objects / quarantine)]

    user --> cli
    user --> daemon
    cli -->|HTTP| daemon
    daemon --> api
    api --> auth
    api --> cp
    cp -.-> memory
    cp -.-> pgstore
    memory --> files
    pgstore --> db
    pgstore --> files
    files --> disk
```

## 2. Low-level object write path (LLD)

```mermaid
flowchart TB
    request["HTTP PUT /api/v1/data/buckets/:bucket/objects/:key<br/>Content-Length + Content-Digest + Idempotency-Key"]
    server[Server.serveHTTP]
    principal["Auth.Authenticate<br/>Principal + role/scope"]
    method[ControlPlane.PutObjectStream]
    resource["Load bucket resource<br/>and authorize object:put"]
    replay["Read 24-hour idempotency record"]
    stage[FileStore.WriteReader]
    validate["Validate bucket/key/size/quota"]
    temp["Create provider-generated file<br/>under staging/"]
    hash["Stream exact bytes<br/>compute SHA-256 and enforce length"]
    sync1["Sync staged file and directory"]
    opaque["Create opaque object path<br/>objects/:bucket/:shard/:version.blob"]
    commitfs["Rename staged file and sync object directory"]
    tx[PostgreSQL transaction]
    op[Insert operation]
    object["Insert or replace blob_objects metadata"]
    idem["Insert idempotency record<br/>expires after 24 hours"]
    audit[Append redacted audit event]
    response["201 object + operation<br/>ETag + X-Ember-Version-ID"]

    request --> server --> principal --> method
    method --> resource --> replay
    replay -->|new request| stage
    replay -->|same hash| response
    stage --> validate --> temp --> hash --> sync1 --> opaque --> commitfs
    commitfs --> tx
    tx --> op
    tx --> object
    tx --> idem
    tx --> audit
    audit --> response
```

The filesystem provider commits bytes before the metadata transaction. A failed or invalid write is removed or recorded as a repair finding by the current implementation; reads verify the stored SHA-256 before returning bytes. PostgreSQL startup applies the embedded migration and refuses incompatible schema metadata.

## 3. Classes, interfaces, and dependencies

```mermaid
classDiagram
    class Server {
        +ControlPlane Store
        +Auth Auth
        +Handler() http.Handler
    }

    class ControlPlane {
        <<interface>>
        +CreateGroup()
        +CreateBucket()
        +GetResource()
        +GetOperation()
        +DeleteResource()
        +PutObjectStream()
        +GetObject()
        +DeleteObject()
        +AddLock()
        +RemoveLock()
        +Audit()
        +Findings()
        +Repair()
    }

    class InMemoryStore {
        <<internal/ember.Store>>
        +resources map
        +operations map
        +objects map
        +audit []AuditEvent
    }

    class PostgresStore {
        <<internal/persistence/postgres.Store>>
        +DB() *sql.DB
        +Close() error
    }

    class FileStore {
        <<internal/ember.FileStore>>
        +WriteReader()
        +Read()
        +Delete()
        +MoveToQuarantine()
        +Reset()
    }

    class Auth {
        +Authenticate(header) Principal
    }

    class Resource {
        +ID string
        +Type string
        +ParentID string
        +Scope string
        +DesiredState string
        +ObservedState string
    }

    class ObjectVersion {
        +BucketID string
        +Key string
        +VersionID string
        +SHA256 string
        +ETag string
        +Size int64
        +Path string
    }

    class DatabaseSQL {
        <<database/sql.DB>>
        +BeginTx()
        +QueryContext()
        +ExecContext()
    }

    Server --> ControlPlane : routes calls
    Server --> Auth : authenticates
    InMemoryStore ..|> ControlPlane
    PostgresStore ..|> ControlPlane
    InMemoryStore --> FileStore : Blob bytes
    PostgresStore --> FileStore : Blob bytes
    PostgresStore --> Resource : SQL rows
    PostgresStore --> ObjectVersion : SQL rows
    PostgresStore --> DatabaseSQL : lib/pq
```

## 4. Sequence: PostgreSQL-backed object PUT

```mermaid
sequenceDiagram
    actor Operator
    participant CLI as cmd/ember
    participant HTTP as ember.Server
    participant Auth as internal/ember.Auth
    participant CP as postgres.Store
    participant DB as PostgreSQL 16.14
    participant FS as ember.FileStore

    Operator->>CLI: ember object-put BUCKET KEY FILE
    CLI->>HTTP: PUT object with bearer, length, digest, idempotency key
    HTTP->>Auth: Authenticate Authorization header
    Auth-->>HTTP: Principal(editor or owner)
    HTTP->>CP: PutObjectStream(principal, bucket, key, body, metadata)
    CP->>DB: Load bucket, authorize scope, check replay
    DB-->>CP: Bucket state and no active replay
    CP->>FS: WriteReader(bucket, key, stream, length, SHA-256)
    FS->>FS: Validate key and quota
    FS->>FS: Write staging file, hash exact bytes, fsync
    FS->>FS: Rename to opaque objects path, fsync directories
    FS-->>CP: ObjectVersion(path, checksum, ETag, version)
    CP->>DB: Begin transaction
    CP->>DB: Insert operation, blob metadata, idempotency record, audit event
    DB-->>CP: Commit succeeded
    CP-->>HTTP: ObjectVersion and Operation
    HTTP-->>CLI: 201 Created with ETag and version ID
    CLI-->>Operator: JSON response
```

### Deliberate non-goals

This Phase 1 design does not include a distributed control plane, cloud deployment, asynchronous object transfer, public authentication, Azure wire compatibility, or later Ember services. Those would require a new reviewed boundary rather than being inferred from these diagrams.
