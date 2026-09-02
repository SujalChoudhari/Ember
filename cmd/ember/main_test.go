package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
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
