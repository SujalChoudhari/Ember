package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
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
