package ember

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const declarativeScope = "i/t/s/declarative"

func declarativeStore(t *testing.T) *Store {
	t.Helper()
	files, err := NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(files)
}

func validDeclarativeYAML() string {
	return `apiVersion: ember/v1
parameters:
  environment:
    type: string
    default: dev
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: i/t/s/declarative
    tags:
      environment: dev
  - id: bucket
    type: Ember.Blob/bucket
    name: assets
    scope: i/t/s/declarative
    parent: group
    dependsOn: [group]
    tags:
      purpose: test
    lifecycle:
      preventDestroy: true
outputs:
  groupId:
    value: ${group.id}
tags:
  owner: ember
`
}

func TestParseDeclarativeDocumentContract(t *testing.T) {
	document, err := ParseDocument([]byte(validDeclarativeYAML()), "ember.yaml")
	if err != nil {
		t.Fatalf("parse valid document: %v", err)
	}
	if document.APIVersion != DeclarativeAPIVersion || len(document.Resources) != 2 || document.Parameters["environment"].Type != "string" {
		t.Fatalf("unexpected parsed document: %#v", document)
	}
	if _, err := ParseDocument([]byte(`{"apiVersion":"ember/v1","unknown":true}`), "ember.json"); !errors.Is(err, ErrMalformedDocument) || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected actionable unknown-field error, got %v", err)
	}
	if _, err := ParseDocument([]byte(`{"apiVersion":"ember/v1","resources":[]} trailing`), "ember.json"); !errors.Is(err, ErrMalformedDocument) || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("expected trailing JSON data to be rejected, got %v", err)
	}
	if _, err := ParseDocument([]byte("apiVersion: ember/v1\nresources: []\n---\n: trailing"), "ember.yaml"); !errors.Is(err, ErrMalformedDocument) || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("expected trailing YAML data to be rejected, got %v", err)
	}
	if _, err := ParseDocument([]byte(`apiVersion: ember/v99
resources: []`), "ember.yaml"); !errors.Is(err, ErrUnsupportedDocumentVersion) || !strings.Contains(err.Error(), "ember/v99") {
		t.Fatalf("expected unsupported-version error, got %v", err)
	}
}

func TestDeclarativeApplyWorksForEditorAndPersistsState(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-editor", Role: "editor", Scope: "*"}
	result, err := engine.ApplyDocument(principal, []byte(validDeclarativeYAML()), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true})
	if err != nil {
		t.Fatalf("editor apply: %v", err)
	}
	if len(result.Operations) != 2 {
		t.Fatalf("editor apply operations=%d, want 2", len(result.Operations))
	}
	states, err := store.ListDeclarativeResources(principal, declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("editor declarative states=%d, want 2", len(states))
	}
}

func TestDeclarativeLogicalIDsAreScoped(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	for _, scope := range []string{"i/t/s/one", "i/t/s/two"} {
		document := `apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: ` + scope + "\n"
		if _, err := engine.ApplyDocument(principal, []byte(document), "ember.yaml", ApplyOptions{Scope: scope, ApproveDestructive: true}); err != nil {
			t.Fatalf("apply scope %s: %v", scope, err)
		}
	}
	for _, scope := range []string{"i/t/s/one", "i/t/s/two"} {
		plan, err := engine.PlanDocument(principal, []byte(`apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: `+scope+"\n"), "ember.yaml", scope)
		if err != nil {
			t.Fatalf("plan scope %s: %v", scope, err)
		}
		if len(plan.Entries) != 1 || plan.Entries[0].Action != PlanNoop {
			t.Fatalf("scope %s plan=%#v, want one no-op", scope, plan.Entries)
		}
	}
}

func TestDeclarativeDependencyCycleRejectedBeforeMutation(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	document := `apiVersion: ember/v1
resources:
  - id: a
    type: resourceGroup
    name: a
    scope: i/t/s/declarative
    dependsOn: [b]
  - id: b
    type: resourceGroup
    name: b
    scope: i/t/s/declarative
    dependsOn: [a]
`
	_, err := engine.ApplyDocument(Principal{Name: "local-owner", Role: "owner", Scope: "*"}, []byte(document), "ember.yaml", ApplyOptions{Scope: declarativeScope, RequestID: "req-cycle", CorrelationID: "corr-cycle"})
	if !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("expected cycle rejection, got %v", err)
	}
	resources, err := store.ListDeclarativeResources(Principal{Name: "local-owner", Role: "owner", Scope: "*"}, declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("cycle mutated authority: %#v", resources)
	}
}

func TestDeclarativePlanIsDeterministicAndShowsBlockedEdges(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	first, err := engine.PlanDocument(principal, []byte(validDeclarativeYAML()), "ember.yaml", declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.PlanDocument(principal, []byte(validDeclarativeYAML()), "ember.yaml", declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := json.Marshal(first)
	secondBytes, _ := json.Marshal(second)
	if !reflect.DeepEqual(firstBytes, secondBytes) || !reflect.DeepEqual(first.Order, []string{"group", "bucket"}) {
		t.Fatalf("plans are not deterministic: first=%s second=%s", firstBytes, secondBytes)
	}
	if len(first.Edges) != 1 || first.Edges[0].From != "bucket" || first.Edges[0].To != "group" {
		t.Fatalf("dependency edge not visible: %#v", first.Edges)
	}
	if !reflect.DeepEqual(first.Entries[1].DependsOn, []string{"group"}) {
		t.Fatalf("plan dependency list contains duplicates: %#v", first.Entries[1].DependsOn)
	}
}

func TestDeclarativeApplyIsIdempotentAndLinksOperationAudit(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	first, err := engine.ApplyDocument(principal, []byte(validDeclarativeYAML()), "ember.yaml", ApplyOptions{Scope: declarativeScope, RequestID: "req-apply", CorrelationID: "corr-apply", ApproveDestructive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Operations) != 2 || first.Plan.Entries[0].Action != PlanCreate || first.Plan.Entries[1].Action != PlanCreate {
		t.Fatalf("unexpected first apply: %#v", first)
	}
	for _, operation := range first.Operations {
		if operation.ID == "" || operation.CorrelationID != "corr-apply" {
			t.Fatalf("operation linkage missing: %#v", operation)
		}
	}
	auditEvents, err := store.Audit(principal, declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(auditEvents) < 2 {
		t.Fatalf("expected apply audit records, got %#v", auditEvents)
	}
	for _, operation := range first.Operations {
		found := false
		for _, event := range auditEvents {
			if event.CorrelationID == operation.CorrelationID && event.Target == operation.ResourceID {
				found = true
			}
		}
		if !found {
			t.Fatalf("no audit linkage for operation %#v: %#v", operation, auditEvents)
		}
	}
	second, err := engine.ApplyDocument(principal, []byte(validDeclarativeYAML()), "ember.yaml", ApplyOptions{Scope: declarativeScope, RequestID: "req-reapply", CorrelationID: "corr-reapply", ApproveDestructive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Operations) != 0 {
		t.Fatalf("idempotent reapply mutated: %#v", second.Operations)
	}
	for _, entry := range second.Plan.Entries {
		if entry.Action != PlanNoop {
			t.Fatalf("expected no-op reapply, got %#v", second.Plan.Entries)
		}
	}
}

func TestDeclarativeUpdateShowsOnlyIntendedDelta(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	initial := `apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: i/t/s/declarative
    tags: {environment: dev, owner: ember}
`
	if _, err := engine.ApplyDocument(principal, []byte(initial), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(initial, "environment: dev", "environment: prod", 1)
	plan, err := engine.PlanDocument(principal, []byte(updated), "ember.yaml", declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Action != PlanUpdate || len(plan.Entries[0].Changes) != 1 || plan.Entries[0].Changes[0].Path != "tags.environment" {
		t.Fatalf("unexpected delta plan: %#v", plan.Entries)
	}
	if plan.Entries[0].Destructive {
		t.Fatalf("tag replacement should not be destructive: %#v", plan.Entries[0])
	}
	result, err := engine.ApplyDocument(principal, []byte(updated), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true})
	if err != nil {
		t.Fatalf("apply update: %v", err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("update operations=%d, want 1", len(result.Operations))
	}
	resource, err := store.GetResource(principal, plan.Entries[0].ResourceID, "req-update-read", "corr-update-read")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Tags["environment"] != "prod" {
		t.Fatalf("resource tags=%#v, want environment=prod", resource.Tags)
	}
}

func TestDeclarativeDestructiveUpdateRequiresApprovalAndApplies(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	initial := `apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: i/t/s/declarative
`
	renamed := strings.Replace(initial, "name: demo", "name: renamed", 1)
	if _, err := engine.ApplyDocument(principal, []byte(initial), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	blocked, err := engine.ApplyDocument(principal, []byte(renamed), "ember.yaml", ApplyOptions{Scope: declarativeScope})
	if !errors.Is(err, ErrDestructiveApprovalRequired) {
		t.Fatalf("unapproved rename error=%v, want approval gate", err)
	}
	if blocked == nil || len(blocked.Operations) != 0 {
		t.Fatalf("unapproved rename result=%#v", blocked)
	}
	resourceID := blocked.Plan.Entries[0].ResourceID
	resource, err := store.GetResource(principal, resourceID, "req-rename-before", "corr-rename-before")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Name != "demo" {
		t.Fatalf("unapproved rename changed resource: %#v", resource)
	}
	approved, err := engine.ApplyDocument(principal, []byte(renamed), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true})
	if err != nil || len(approved.Operations) != 1 {
		t.Fatalf("approved rename result=%#v err=%v", approved, err)
	}
	resource, err = store.GetResource(principal, resourceID, "req-rename-after", "corr-rename-after")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Name != "renamed" {
		t.Fatalf("approved rename did not update resource: %#v", resource)
	}
}

func TestDeclarativeApprovedDeleteOrdersChildrenBeforeParents(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	initial := `apiVersion: ember/v1
resources:
  - id: a-group
    type: resourceGroup
    name: demo
    scope: i/t/s/declarative
  - id: z-bucket
    type: Ember.Blob/bucket
    name: assets
    scope: i/t/s/declarative
    parent: a-group
`
	if _, err := engine.ApplyDocument(principal, []byte(initial), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.ApplyDocument(principal, []byte("apiVersion: ember/v1\nresources: []\n"), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true})
	if err != nil {
		t.Fatalf("approved delete should remove children before parents: %v", err)
	}
	if result == nil || len(result.Operations) != 2 {
		t.Fatalf("approved delete result=%#v, want two operations", result)
	}
	states, err := store.ListDeclarativeResources(principal, declarativeScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("approved delete left declarative state: %#v", states)
	}
}

func TestDeclarativeApplyRequiresDestructiveApprovalAndDoesNotDelete(t *testing.T) {
	store := declarativeStore(t)
	engine := NewDeclarativeEngine(store)
	principal := Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	initial := `apiVersion: ember/v1
resources:
  - id: group
    type: resourceGroup
    name: demo
    scope: i/t/s/declarative
`
	if _, err := engine.ApplyDocument(principal, []byte(initial), "ember.yaml", ApplyOptions{Scope: declarativeScope, ApproveDestructive: true}); err != nil {
		t.Fatal(err)
	}
	empty := `apiVersion: ember/v1
resources: []
`
	result, err := engine.ApplyDocument(principal, []byte(empty), "ember.yaml", ApplyOptions{Scope: declarativeScope, RequestID: "req-delete", CorrelationID: "corr-delete"})
	if !errors.Is(err, ErrDestructiveApprovalRequired) {
		t.Fatalf("expected destructive approval error, got %v", err)
	}
	if result == nil || len(result.Operations) != 0 || result.Plan.Entries[0].Action != PlanDelete {
		t.Fatalf("unexpected blocked apply result: %#v", result)
	}
	if _, err := store.GetResource(principal, result.Plan.Entries[0].ResourceID, "req-read", "corr-read"); err != nil {
		t.Fatalf("unapproved apply deleted resource: %v", err)
	}
	approved, err := engine.ApplyDocument(principal, []byte(empty), "ember.yaml", ApplyOptions{Scope: declarativeScope, RequestID: "req-delete-approved", CorrelationID: "corr-delete-approved", ApproveDestructive: true})
	if err != nil || len(approved.Operations) != 1 {
		t.Fatalf("approved delete failed: result=%#v err=%v", approved, err)
	}
	if _, err := store.GetResource(principal, result.Plan.Entries[0].ResourceID, "req-read-after", "corr-read-after"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approved delete did not remove resource: %v", err)
	}
}
