package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func runOperatorCLI(t *testing.T, operator *Operator, args ...string) OperatorResponse {
	t.Helper()
	var output bytes.Buffer
	if err := RunCLI(context.Background(), operator, args, &output); err != nil {
		t.Fatalf("RunCLI(%v) error = %v", args, err)
	}
	var response OperatorResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode RunCLI(%v) output %q error = %v", args, output.String(), err)
	}
	return response
}

func runOperatorCLIResult(t *testing.T, operator *Operator, args ...string) (OperatorResponse, error) {
	t.Helper()
	var output bytes.Buffer
	err := RunCLI(context.Background(), operator, args, &output)
	if err != nil {
		return OperatorResponse{}, err
	}
	var response OperatorResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode RunCLI(%v) output %q error = %v", args, output.String(), err)
	}
	return response, nil
}

func TestCLIUsesSharedScopedResourceAndOperationContract(t *testing.T) {
	operator := newTestOperator(t)

	created := runOperatorCLI(t, operator, "resource", "create",
		"--type", "group",
		"--name", "platform",
		"--provider-namespace", "Ember.Storage",
		"--provider-type", "groups",
		"--provider-version", "v1",
		"--desired-state", "ready",
	)
	if created.Resource == nil || created.Resource.Spec.Provider.Namespace != "Ember.Storage" || created.Resource.Spec.DesiredState != "ready" || created.Resource.ObservedState != "unknown" {
		t.Fatalf("CLI create response = %#v, want provider and desired/observed state", created)
	}

	fetched := runOperatorCLI(t, operator, "resource", "get", "--id", created.Resource.ID)
	if fetched.Resource == nil || fetched.Resource.ID != created.Resource.ID || fetched.Resource.Spec.Provider != created.Resource.Spec.Provider {
		t.Fatalf("CLI get response = %#v, want same resource contract", fetched)
	}

	updated := runOperatorCLI(t, operator, "resource", "update-tags",
		"--id", created.Resource.ID,
		"--tags", "tier=test",
		"--request-id", "request-cli-1",
		"--correlation-id", "correlation-cli-1",
	)
	if updated.Resource == nil || updated.Operation == nil || updated.Replayed || updated.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("CLI update response = %#v, want resource and operation", updated)
	}

	replayed := runOperatorCLI(t, operator, "resource", "update-tags",
		"--id", created.Resource.ID,
		"--tags", "tier=different",
		"--request-id", "request-cli-1",
		"--correlation-id", "correlation-cli-2",
	)
	if replayed.Resource == nil || replayed.Operation == nil || !replayed.Replayed || replayed.Operation.ID != updated.Operation.ID || replayed.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("CLI replay response = %#v, want original operation and state", replayed)
	}
}

func TestCLIExposesBlobInspectionAndResetContract(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	group := runOperatorCLI(t, operator, "resource", "create", "--type", "group", "--name", "platform")
	bucket := runOperatorCLI(t, operator, "resource", "create", "--scope", group.Resource.ID, "--parent", group.Resource.ID, "--type", "bucket", "--name", "assets")
	other := runOperatorCLI(t, operator, "resource", "create", "--type", "group", "--name", "other")

	put := runOperatorCLI(t, operator, "blob", "put", "--scope", group.Resource.ID, "--bucket", bucket.Resource.ID, "--key", "nested/file.txt", "--data", "hello world")
	if put.Object == nil || put.Object.Size != 11 {
		t.Fatalf("CLI blob put response = %#v, want bounded object metadata", put)
	}
	got := runOperatorCLI(t, operator, "blob", "get", "--scope", group.Resource.ID, "--bucket", bucket.Resource.ID, "--key", "nested/file.txt")
	if got.Object == nil || string(got.Content) != "hello world" {
		t.Fatalf("CLI blob get response = %#v, want object and payload", got)
	}
	rangeResult := runOperatorCLI(t, operator, "blob", "get", "--scope", group.Resource.ID, "--bucket", bucket.Resource.ID, "--key", "nested/file.txt", "--start", "6", "--end", "11")
	if string(rangeResult.Content) != "world" {
		t.Fatalf("CLI blob range content = %q, want world", rangeResult.Content)
	}

	updated := runOperatorCLI(t, operator, "resource", "update-tags", "--scope", group.Resource.ID, "--id", bucket.Resource.ID, "--tags", "tier=test", "--request-id", "request-cli-inspect", "--correlation-id", "correlation-cli-inspect")
	if _, err := runOperatorCLIResult(t, operator, "resource", "update-tags", "--scope", other.Resource.ID, "--id", bucket.Resource.ID, "--tags", "tier=foreign", "--request-id", "request-cli-foreign", "--correlation-id", "correlation-cli-foreign"); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("CLI cross-scope mutation error = %v, want ErrOperatorScopeDenied", err)
	}
	operation := runOperatorCLI(t, operator, "operation", "get", "--scope", group.Resource.ID, "--id", updated.Operation.ID)
	if operation.Operation == nil || operation.Operation.ID != updated.Operation.ID {
		t.Fatalf("CLI operation response = %#v, want linked operation", operation)
	}
	audit := runOperatorCLI(t, operator, "audit", "list", "--scope", group.Resource.ID, "--resource", bucket.Resource.ID, "--limit", "10")
	if len(audit.Audit) != 1 || audit.Audit[0].OperationID != updated.Operation.ID {
		t.Fatalf("CLI audit response = %#v, want one linked entry", audit)
	}

	if _, err := runOperatorCLIResult(t, operator, "reset", "--scope", group.Resource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("CLI scoped reset error = %v, want ErrOperatorScopeDenied", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "reset"); err != nil {
		t.Fatalf("CLI root reset error = %v", err)
	}
}

func TestCLIExposesCompleteResourceLifecycleAndLockSurface(t *testing.T) {
	operator := newTestOperator(t)

	root := runOperatorCLI(t, operator, "resource", "create", "--type", "group", "--name", "root").Resource
	other := runOperatorCLI(t, operator, "resource", "create", "--type", "group", "--name", "other").Resource
	child := runOperatorCLI(t, operator, "resource", "create", "--scope", root.ID, "--parent", root.ID, "--type", "bucket", "--name", "assets").Resource

	listed := runOperatorCLI(t, operator, "resource", "list", "--limit", "10")
	if len(listed.Resources) != 2 || listed.Resources[0].ID != root.ID || listed.Resources[1].ID != other.ID {
		t.Fatalf("CLI root list = %#v, want deterministic root resources", listed)
	}
	scoped := runOperatorCLI(t, operator, "resource", "list", "--scope", root.ID, "--limit", "10")
	if len(scoped.Resources) != 1 || scoped.Resources[0].ID != child.ID {
		t.Fatalf("CLI scoped list = %#v, want child resource", scoped)
	}

	updated := runOperatorCLI(t, operator, "resource", "update-tags", "--scope", root.ID, "--id", child.ID, "--tags", "tier=test", "--request-id", "request-cli-lifecycle", "--correlation-id", "correlation-cli-lifecycle")
	if updated.Resource == nil || updated.Resource.ID != child.ID || updated.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("CLI tag update = %#v, want immutable identity and updated tags", updated)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "delete", "--id", root.ID); !errors.Is(err, persistence.ErrResourceHasDependents) {
		t.Fatalf("CLI delete dependent root error = %v, want dependent refusal", err)
	}

	acquired := runOperatorCLI(t, operator, "resource", "lock", "acquire", "--id", root.ID, "--owner", "operator", "--token", "lock-token")
	if acquired.Lock == nil || acquired.Lock.Owner != "operator" || acquired.Lock.Token != "lock-token" {
		t.Fatalf("CLI lock acquire = %#v, want lock response", acquired)
	}
	inspected := runOperatorCLI(t, operator, "resource", "lock", "inspect", "--id", root.ID)
	if inspected.Lock == nil || *inspected.Lock != *acquired.Lock {
		t.Fatalf("CLI lock inspect = %#v, want acquired lock", inspected)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "get", "--scope", root.ID, "--id", child.ID); err != nil {
		t.Fatalf("CLI same-scope get under lock error = %v", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "update-tags", "--scope", root.ID, "--id", child.ID, "--tags", "tier=blocked", "--request-id", "request-cli-locked", "--correlation-id", "correlation-cli-locked"); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("CLI locked update error = %v, want resource lock", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "create", "--scope", root.ID, "--parent", root.ID, "--type", "group", "--name", "blocked"); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("CLI locked create error = %v, want resource lock", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "delete", "--scope", root.ID, "--id", child.ID); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("CLI locked delete error = %v, want resource lock", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "lock", "inspect", "--scope", other.ID, "--id", root.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("CLI cross-scope lock inspect error = %v, want scope denial", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "lock", "release", "--id", root.ID, "--owner", "other", "--token", "wrong"); !errors.Is(err, persistence.ErrResourceLockNotOwner) {
		t.Fatalf("CLI wrong lock release error = %v, want owner error", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "lock", "release", "--id", root.ID, "--owner", "operator", "--token", "lock-token"); err != nil {
		t.Fatalf("CLI lock release error = %v", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "delete", "--scope", root.ID, "--id", child.ID); err != nil {
		t.Fatalf("CLI leaf delete error = %v", err)
	}
	if _, err := runOperatorCLIResult(t, operator, "resource", "delete", "--id", root.ID); err != nil {
		t.Fatalf("CLI root delete after leaf error = %v", err)
	}
}
