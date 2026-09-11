package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember"
)

func TestRunExecutesSharedCLIAgainstFileOperator(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"--state-dir", stateDir,
		"resource", "create",
		"--type", "group",
		"--name", "platform",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(create) code = %d, stderr = %q", code, stderr.String())
	}
	var created ember.OperatorResponse
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("decode create output %q error = %v", stdout.String(), err)
	}
	if created.Resource == nil || created.Resource.ID == "" {
		t.Fatalf("create output = %#v, want resource", created)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{
		"--state-dir", stateDir,
		"resource", "get",
		"--id", created.Resource.ID,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(get) code = %d, stderr = %q", code, stderr.String())
	}
	var fetched ember.OperatorResponse
	if err := json.Unmarshal(stdout.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get output %q error = %v", stdout.String(), err)
	}
	if fetched.Resource == nil || fetched.Resource.ID != created.Resource.ID {
		t.Fatalf("get output = %#v, want resource %q", fetched, created.Resource.ID)
	}
}

func TestRunSupportsWorkloadHealthLogsRecoveryAndConfirmationJourney(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	call := func(args ...string) ember.OperatorResponse {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := run(append([]string{"--state-dir", stateDir}, args...), &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) code = %d, stderr = %q", args, code, stderr.String())
		}
		var response ember.OperatorResponse
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatalf("decode %v output %q error = %v", args, stdout.String(), err)
		}
		return response
	}

	group := call("resource", "create", "--type", "group", "--name", "platform")
	workload := call("workload", "create", "--scope", group.Resource.ID, "--name", "api", "--provider-namespace", "Ember.Compute", "--provider-type", "workloads", "--provider-version", "v1", "--desired-state", "ready")
	if workload.Workload == nil || workload.Workload.Status.Health != "healthy" || workload.Workload.Status.Readiness != "ready" {
		t.Fatalf("workload create = %#v, want healthy ready workload", workload)
	}

	inspected := call("workload", "inspect", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--limit", "10")
	if inspected.Observability == nil || inspected.Observability.Status.Health != "healthy" || inspected.Observability.Status.Readiness != "ready" {
		t.Fatalf("workload inspection = %#v, want bounded health report", inspected)
	}
	restarted := call("workload", "restart", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID)
	if restarted.Workload == nil || restarted.Workload.Status.ExecutionID == workload.Workload.Status.ExecutionID {
		t.Fatalf("workload restart = %#v, want new execution correlation", restarted)
	}
	inspected = call("workload", "inspect", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--limit", "1")
	if inspected.Observability == nil || len(inspected.Observability.Logs) != 1 || inspected.Observability.Logs[0].ExecutionID != restarted.Workload.Status.ExecutionID {
		t.Fatalf("workload logs = %#v, want one bounded restart log", inspected)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--state-dir", stateDir, "workload", "delete", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "explicit confirmation") {
		t.Fatalf("workload delete without confirmation code = %d, stderr = %q, want explicit confirmation", code, stderr.String())
	}
	if code := run([]string{"--state-dir", stateDir, "workload", "delete", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--confirm"}, &stdout, &stderr); code != 0 {
		t.Fatalf("confirmed workload delete code = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunSupportsWorkloadVolumeAndLimitJourney(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	call := func(args ...string) ember.OperatorResponse {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := run(append([]string{"--state-dir", stateDir}, args...), &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) code = %d, stderr = %q", args, code, stderr.String())
		}
		var response ember.OperatorResponse
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatalf("decode %v output %q error = %v", args, stdout.String(), err)
		}
		return response
	}

	group := call("resource", "create", "--type", "group", "--name", "platform")
	workload := call("workload", "create", "--scope", group.Resource.ID, "--name", "api", "--provider-namespace", "Ember.Compute", "--provider-type", "workloads", "--provider-version", "v1", "--desired-state", "ready")
	if workload.Workload == nil || workload.Workload.Status.ExecutionID == "" {
		t.Fatalf("workload create = %#v, want execution correlation", workload)
	}

	attached := call("workload", "volume", "attach", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--name", "cache", "--max-bytes", "1024")
	if len(attached.Volumes) != 1 || attached.Volumes[0].WorkloadID != workload.Workload.Resource.ID || attached.Volumes[0].Name != "cache" {
		t.Fatalf("volume attach = %#v, want bounded owned volume", attached)
	}
	listed := call("workload", "volume", "list", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--limit", "10")
	if len(listed.Volumes) != 1 || listed.Volumes[0].ID != attached.Volumes[0].ID {
		t.Fatalf("volume list = %#v, want attached volume", listed)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--state-dir", stateDir, "workload", "volume", "cleanup", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "explicit confirmation") {
		t.Fatalf("volume cleanup without confirmation code = %d, stderr = %q, want explicit confirmation", code, stderr.String())
	}
	call("workload", "volume", "cleanup", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--confirm")
	listed = call("workload", "volume", "list", "--scope", group.Resource.ID, "--id", workload.Workload.Resource.ID, "--limit", "10")
	if len(listed.Volumes) != 0 {
		t.Fatalf("volume list after cleanup = %#v, want no owned volume residue", listed)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--state-dir", stateDir, "workload", "create", "--scope", group.Resource.ID, "--name", "oversized", "--provider-namespace", "Ember.Compute", "--provider-type", "workloads", "--provider-version", "v1", "--cpu-millis", "64001"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "invalid workload spec") {
		t.Fatalf("over-bound workload code = %d, stderr = %q, want deterministic limit error", code, stderr.String())
	}
}

func TestRunSupportsDeploymentInspectionJourney(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	document := `{"version":"v1","resources":[{"type":"group","name":"platform","desiredState":"ready"}]}`
	var stdout, stderr bytes.Buffer
	call := func(args ...string) ember.OperatorResponse {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := run(append([]string{"--state-dir", stateDir}, args...), &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) code = %d, stderr = %q", args, code, stderr.String())
		}
		var response ember.OperatorResponse
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			t.Fatalf("decode %v output %q error = %v", args, stdout.String(), err)
		}
		return response
	}

	planned := call("deployment", "plan", "--document", document)
	if planned.Plan == nil || planned.Plan.Summary.Create != 1 {
		t.Fatalf("deployment plan = %#v, want one create", planned)
	}
	applied := call("deployment", "apply", "--document", document, "--request-id", "request-journey", "--correlation-id", "correlation-journey")
	if applied.Apply == nil || len(applied.Apply.Operations) != 1 || applied.Apply.Operations[0].ID == "" {
		t.Fatalf("deployment apply = %#v, want one inspectable operation", applied)
	}

	resource := call("resource", "get", "--id", "resource-00000001")
	if resource.Resource == nil || resource.Resource.Spec.Name != "platform" {
		t.Fatalf("resource inspection = %#v, want applied platform resource", resource)
	}
	operation := call("operation", "get", "--id", applied.Apply.Operations[0].ID)
	if operation.Operation == nil || operation.Operation.ID != applied.Apply.Operations[0].ID || operation.Operation.RequestID != "request-journey" || operation.Operation.CorrelationID != "correlation-journey" {
		t.Fatalf("operation inspection = %#v, want applied operation identifiers", operation)
	}
}
