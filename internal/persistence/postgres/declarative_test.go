package postgres_test

import (
	"errors"
	"testing"

	"ember.local/ember/internal/ember"
)

const postgresDeclarativeScope = "i/t/s/postgres-declarative"

func postgresDeclarativeDocument() string {
	return `apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: i/t/s/postgres-declarative
    tags:
      environment: dev
  - id: bucket
    type: Ember.Blob/bucket
    name: assets
    scope: i/t/s/postgres-declarative
    parent: group
    dependsOn: [group]
    tags:
      purpose: test
`
}

func TestPostgresDeclarativeApplyPersistsStateAndNoOpReplay(t *testing.T) {
	store, _ := integrationStore(t)
	engine := ember.NewDeclarativeEngine(store)
	principal := ember.Principal{Name: "local-editor", Role: "editor", Scope: "*"}

	first, err := engine.ApplyDocument(principal, []byte(postgresDeclarativeDocument()), "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, CorrelationID: "pg-first", ApproveDestructive: true})
	if err != nil {
		t.Fatalf("first PostgreSQL declarative apply: %v", err)
	}
	if len(first.Operations) != 2 {
		t.Fatalf("first operations=%d, want 2", len(first.Operations))
	}
	for _, entry := range first.Plan.Entries {
		resource, getErr := store.GetResource(principal, entry.ResourceID, "pg-read", "pg-read")
		if getErr != nil {
			t.Fatalf("read applied resource %s: %v", entry.LogicalID, getErr)
		}
		if len(resource.Tags) == 0 {
			t.Fatalf("resource %s lost declarative tags: %#v", entry.LogicalID, resource)
		}
	}

	second, err := engine.ApplyDocument(principal, []byte(postgresDeclarativeDocument()), "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, CorrelationID: "pg-second", ApproveDestructive: true})
	if err != nil {
		t.Fatalf("repeated PostgreSQL declarative apply: %v", err)
	}
	if len(second.Operations) != 0 {
		t.Fatalf("repeated apply created operations: %#v", second.Operations)
	}
	for _, entry := range second.Plan.Entries {
		if entry.Action != ember.PlanNoop {
			t.Fatalf("repeated apply entry=%#v, want no-op", entry)
		}
	}

	auditEvents, err := store.Audit(principal, postgresDeclarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range first.Operations {
		found := false
		for _, event := range auditEvents {
			if event.Target == operation.ResourceID && event.CorrelationID == operation.CorrelationID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("operation %s has no matching audit event: %#v", operation.ID, auditEvents)
		}
	}
}

func postgresDeclarativeCascadeDocument() string {
	return `apiVersion: ember/v1
resources:
  - id: a-group
    type: resourceGroup
    name: demo
    scope: i/t/s/postgres-declarative
  - id: z-bucket
    type: Ember.Blob/bucket
    name: assets
    scope: i/t/s/postgres-declarative
    parent: a-group
`
}

func TestPostgresDeclarativeApprovedDeleteOrdersChildrenBeforeParents(t *testing.T) {
	store, _ := integrationStore(t)
	engine := ember.NewDeclarativeEngine(store)
	principal := ember.Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	if _, err := engine.ApplyDocument(principal, []byte(postgresDeclarativeCascadeDocument()), "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.ApplyDocument(principal, []byte("apiVersion: ember/v1\nresources: []\n"), "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, ApproveDestructive: true})
	if err != nil {
		t.Fatalf("approved PostgreSQL delete should remove children before parents: %v", err)
	}
	if result == nil || len(result.Operations) != 2 {
		t.Fatalf("approved PostgreSQL delete result=%#v, want two operations", result)
	}
	states, err := store.ListDeclarativeResources(principal, postgresDeclarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("approved PostgreSQL delete left declarative state: %#v", states)
	}
}

func TestPostgresDeclarativeApplyBlocksUnapprovedDelete(t *testing.T) {
	store, _ := integrationStore(t)
	engine := ember.NewDeclarativeEngine(store)
	principal := ember.Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	if _, err := engine.ApplyDocument(principal, []byte(postgresDeclarativeDocument()), "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	empty := []byte("apiVersion: ember/v1\nresources: []\n")
	result, err := engine.ApplyDocument(principal, empty, "ember.yaml", ember.ApplyOptions{Scope: postgresDeclarativeScope, CorrelationID: "pg-delete-blocked"})
	if !errors.Is(err, ember.ErrDestructiveApprovalRequired) {
		t.Fatalf("unapproved delete error=%v, want approval gate", err)
	}
	if result == nil || len(result.Operations) != 0 {
		t.Fatalf("unapproved delete result=%#v", result)
	}
	for _, entry := range result.Plan.Entries {
		if entry.Action != ember.PlanDelete {
			t.Fatalf("entry=%#v, want delete preview", entry)
		}
		if _, getErr := store.GetResource(principal, entry.ResourceID, "pg-after-block", "pg-after-block"); getErr != nil {
			t.Fatalf("unapproved delete removed %s: %v", entry.LogicalID, getErr)
		}
	}
}
