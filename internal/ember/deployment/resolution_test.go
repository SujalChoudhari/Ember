package deployment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestParseAndResolveParametersAndResourceReferencesSafely(t *testing.T) {
	input := []byte(`{
		"version": "v1",
		"parameters": {
			"environment": {"type": "string", "defaultValue": "dev"},
			"credential": {"type": "secureString"}
		},
		"resources": [
			{"type": "group", "name": "platform"},
			{"type": "bucket", "name": "assets", "parentId": "${resources.platform.id}", "tags": {"environment": "${parameters.environment}", "credential": "${parameters.credential}", "parent": "${resources.platform.name}", "kind": "${resources.platform.type}"}}
		]
	}`)
	document, err := ParseAndValidate(input)
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}

	secretValue := "runtime-secret-" + t.Name()
	resolution, err := Resolve(document, map[string]string{"environment": "test", "credential": secretValue})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, err := resolution.Secret("credential"); err != nil || got != secretValue {
		t.Fatalf("Secret(credential) = %q, %v, want protected input", got, err)
	}

	safe := resolution.Document()
	if got := safe.Parameters["environment"].Value; got != "test" {
		t.Fatalf("resolved environment = %q, want override", got)
	}
	if got := safe.Parameters["credential"].Value; got != RedactedValue {
		t.Fatalf("resolved credential = %q, want redacted marker", got)
	}
	if got := safe.Resources[1].Spec.ParentID; got != safe.Resources[0].ID {
		t.Fatalf("resolved parent ID = %q, want %q", got, safe.Resources[0].ID)
	}
	if got := safe.Resources[1].Spec.Tags["credential"]; got != RedactedValue {
		t.Fatalf("resolved credential tag = %q, want redacted marker", got)
	}
	if got := safe.Resources[1].Spec.Tags["parent"]; got != "platform" {
		t.Fatalf("resolved parent tag = %q, want resource name", got)
	}
	if got := safe.Resources[1].Spec.Tags["kind"]; got != "group" {
		t.Fatalf("resolved kind tag = %q, want resource type", got)
	}
	if safe.Resources[1].Spec.Type != models.ResourceTypeBucket {
		t.Fatalf("resolved resource type = %q, want bucket", safe.Resources[1].Spec.Type)
	}

	encoded, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("Marshal(resolution) error = %v", err)
	}
	if strings.Contains(string(encoded), secretValue) {
		t.Fatalf("safe resolution JSON contains secret value %q: %s", secretValue, encoded)
	}
	if got := resolution.Redact("credential=" + secretValue); got != "credential="+RedactedValue {
		t.Fatalf("Redact() = %q, want marker", got)
	}
	displayError := resolution.RedactError(errors.New("provider rejected " + secretValue))
	if strings.Contains(displayError.Error(), secretValue) || !strings.Contains(displayError.Error(), RedactedValue) {
		t.Fatalf("RedactError() = %v, want redacted marker", displayError)
	}

	defaulted, err := Resolve(document, map[string]string{"credential": secretValue})
	if err != nil {
		t.Fatalf("Resolve() with ordinary default error = %v", err)
	}
	if got := defaulted.Document().Parameters["environment"].Value; got != "dev" {
		t.Fatalf("defaulted environment = %q, want document default", got)
	}
}

func TestResolveRejectsMissingAndUnknownReferencesWithoutEchoingValues(t *testing.T) {
	document, err := ParseAndValidate([]byte(`{
		"version": "v1",
		"parameters": {"required": {"type": "string"}},
		"resources": [{"type": "group", "name": "platform", "tags": {"ref": "${resources.missing.id}"}}]
	}`))
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}

	_, err = Resolve(document, map[string]string{"unexpected": "runtime-value"})
	if err == nil || !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("Resolve() error = %v, want invalid resolution", err)
	}
	if strings.Contains(err.Error(), "runtime-value") {
		t.Fatalf("Resolve() error echoed supplied value: %v", err)
	}
	var resolutionErr *ResolutionError
	if !errors.As(err, &resolutionErr) || len(resolutionErr.Diagnostics) < 2 {
		t.Fatalf("Resolve() error = %#v, want missing parameter and reference diagnostics", err)
	}
}

func TestParseAndValidateRejectsSecureParameterDefault(t *testing.T) {
	secretValue := "runtime-secret-" + t.Name()
	_, err := ParseAndValidate([]byte(`{"version":"v1","parameters":{"credential":{"type":"secureString","defaultValue":"` + secretValue + `"}},"resources":[]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want secure default rejection")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("ParseAndValidate() error echoed secret value: %v", err)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Code != "secret_default_forbidden" {
		t.Fatalf("ParseAndValidate() error = %#v, want secret_default_forbidden", err)
	}
}

func TestResolveRejectsSecureIdentityReferences(t *testing.T) {
	document, err := ParseAndValidate([]byte(`{
		"version": "v1",
		"parameters": {"credential": {"type": "secureString"}},
		"resources": [{"type": "group", "name": "${parameters.credential}"}]
	}`))
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
	_, err = Resolve(document, map[string]string{"credential": "runtime-value"})
	if err == nil || !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("Resolve() error = %v, want secure identity rejection", err)
	}
	if strings.Contains(err.Error(), "runtime-value") {
		t.Fatalf("Resolve() error echoed secret value: %v", err)
	}
}

func TestResolveRejectsAmbiguousResourceReferences(t *testing.T) {
	document, err := ParseAndValidate([]byte(`{
		"version": "v1",
		"resources": [
			{"type": "group", "name": "platform"},
			{"type": "group", "name": "platform"},
			{"type": "bucket", "name": "assets", "parentId": "${resources.platform.id}"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
	_, err = Resolve(document, nil)
	if err == nil || !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("Resolve() error = %v, want ambiguous reference rejection", err)
	}
	var resolutionErr *ResolutionError
	if !errors.As(err, &resolutionErr) {
		t.Fatalf("Resolve() error type = %T, want *ResolutionError", err)
	}
	var foundAmbiguous bool
	for _, diagnostic := range resolutionErr.Diagnostics {
		if diagnostic.Code == "ambiguous_resource_reference" {
			foundAmbiguous = true
			break
		}
	}
	if !foundAmbiguous {
		t.Fatalf("diagnostics = %#v, want ambiguous_resource_reference", resolutionErr.Diagnostics)
	}
}
