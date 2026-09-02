package deployment

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestParseAndValidateAcceptsV1ResourceDocument(t *testing.T) {
	input := []byte(`{
		"version": "v1",
		"resources": [{
			"type": "group",
			"name": "platform",
			"tags": {"environment": "test"},
			"provider": {"namespace": "Ember.Core", "type": "groups", "version": "v1"},
			"desiredState": "ready"
		}]
	}`)

	document, err := ParseAndValidate(input)
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v", err)
	}
	if document.Version != CurrentVersion {
		t.Fatalf("document version = %q, want %q", document.Version, CurrentVersion)
	}
	if len(document.Resources) != 1 {
		t.Fatalf("document resources = %d, want 1", len(document.Resources))
	}
	got := document.Resources[0]
	wantProvider := models.ProviderMetadata{Namespace: "Ember.Core", Type: "groups", Version: "v1"}
	if got.Type != models.ResourceTypeGroup || got.Name != "platform" || got.Provider != wantProvider || got.DesiredState != models.ResourceStateReady || got.Tags["environment"] != "test" {
		t.Fatalf("resource = %#v, want parsed resource spec", got)
	}
}

func TestParseAndValidateRejectsUnknownVersionWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v2","resources":[]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want unsupported version diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("ParseAndValidate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostic count = %d, want 1", len(validationErr.Diagnostics))
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.version" || diagnostic.Code != "unsupported_version" {
		t.Fatalf("diagnostic = %#v, want version path and unsupported_version code", diagnostic)
	}
}

func TestParseAndValidateRejectsMalformedJSON(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[}`))
	if !errors.Is(err, ErrMalformedDocument) {
		t.Fatalf("ParseAndValidate() error = %v, want ErrMalformedDocument", err)
	}
}

func TestParseAndValidateRejectsMissingResourcesWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1"}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want missing resources diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("ParseAndValidate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Path != "$.resources" || validationErr.Diagnostics[0].Code != "required_field" {
		t.Fatalf("diagnostics = %#v, want required resources diagnostic", validationErr.Diagnostics)
	}
}

func TestParseAndValidateRejectsMissingVersionWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"resources":[]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want missing version diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one missing version diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.version" || diagnostic.Code != "required_field" {
		t.Fatalf("diagnostic = %#v, want missing version diagnostic", diagnostic)
	}
}

func TestParseAndValidateRejectsInvalidResourcesTypeWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":{}}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want invalid resources type diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one invalid resources type diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.resources" || diagnostic.Code != "invalid_type" {
		t.Fatalf("diagnostic = %#v, want invalid resources type diagnostic", diagnostic)
	}
}

func TestParseAndValidateRejectsUnsupportedResourceKindWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[{"type":"unknown","name":"bad"}]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want unsupported resource kind diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("ParseAndValidate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Path != "$.resources[0].type" || validationErr.Diagnostics[0].Code != "invalid_resource_type" {
		t.Fatalf("diagnostics = %#v, want unsupported resource kind diagnostic", validationErr.Diagnostics)
	}
}

func TestParseAndValidateRejectsInvalidFieldTypeWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[{"type":"group","name":42}]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want invalid field type diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("ParseAndValidate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Path != "$.resources[0].name" || validationErr.Diagnostics[0].Code != "invalid_type" {
		t.Fatalf("diagnostics = %#v, want invalid name type diagnostic", validationErr.Diagnostics)
	}
}

func TestParseAndValidateRejectsNonObjectRootWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`[]`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want root type diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("ParseAndValidate() error type = %T, want *ValidationError", err)
	}
	if len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Path != "$" || validationErr.Diagnostics[0].Code != "invalid_type" {
		t.Fatalf("diagnostics = %#v, want invalid root type diagnostic", validationErr.Diagnostics)
	}
}

func TestParseAndValidateRejectsUnknownFieldWithoutEchoingItsValue(t *testing.T) {
	secretValue := "not-a-secret-value"
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[],"unexpected":"` + secretValue + `"}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want unknown field diagnostic")
	}
	if !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("ParseAndValidate() error = %v, want ErrInvalidDocument", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("ParseAndValidate() error contains input value %q: %v", secretValue, err)
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 || validationErr.Diagnostics[0].Code != "unknown_field" {
		t.Fatalf("diagnostics = %#v, want one unknown field diagnostic", validationErr.Diagnostics)
	}
}

func TestParseAndValidateRejectsTooManyResourceDeclarations(t *testing.T) {
	resource := `{"type":"group","name":"platform"}`
	resources := strings.TrimSuffix(strings.Repeat(resource+",", MaxResourceDeclarations+1), ",")
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[` + resources + `]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want resource count diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one resource count diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.resources" || diagnostic.Code != "too_many_items" {
		t.Fatalf("diagnostic = %#v, want bounded resource count diagnostic", diagnostic)
	}
}

func TestParseAndValidateRejectsOversizedDocument(t *testing.T) {
	_, err := ParseAndValidate([]byte(strings.Repeat("x", MaxDocumentBytes+1)))
	if !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("ParseAndValidate() error = %v, want ErrDocumentTooLarge", err)
	}
}

func TestParseAndValidateRejectsTooManyTags(t *testing.T) {
	entries := make([]string, 0, models.MaxResourceTagCount+1)
	for index := 0; index <= models.MaxResourceTagCount; index++ {
		entries = append(entries, `"tag-`+strconv.Itoa(index)+`":"value"`)
	}
	input := `{"version":"v1","resources":[{"type":"group","name":"platform","tags":{` + strings.Join(entries, ",") + `}}]}`
	_, err := ParseAndValidate([]byte(input))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want tag count diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one tag count diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.resources[0].tags" || diagnostic.Code != "too_many_items" {
		t.Fatalf("diagnostic = %#v, want bounded tag count diagnostic", diagnostic)
	}
}

func TestParseAndValidateRejectsMissingResourceNameWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[{"type":"group"}]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want missing resource name diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one missing name diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.resources[0].name" || diagnostic.Code != "required_field" {
		t.Fatalf("diagnostic = %#v, want missing resource name diagnostic", diagnostic)
	}
}

func TestParseAndValidateRejectsInvalidDesiredStateWithDocumentPath(t *testing.T) {
	_, err := ParseAndValidate([]byte(`{"version":"v1","resources":[{"type":"group","name":"platform","desiredState":"unknown-state"}]}`))
	if err == nil {
		t.Fatal("ParseAndValidate() error = nil, want invalid desired state diagnostic")
	}
	validationErr, ok := err.(*ValidationError)
	if !ok || len(validationErr.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one desired state diagnostic", validationErr.Diagnostics)
	}
	diagnostic := validationErr.Diagnostics[0]
	if diagnostic.Path != "$.resources[0].desiredState" || diagnostic.Code != "invalid_resource_state" {
		t.Fatalf("diagnostic = %#v, want invalid desired state diagnostic", diagnostic)
	}
}

func TestParseAndValidateAllowsExplicitEmptyParentID(t *testing.T) {
	document, err := ParseAndValidate([]byte(`{"version":"v1","resources":[{"type":"group","name":"platform","parentId":""}]}`))
	if err != nil {
		t.Fatalf("ParseAndValidate() error = %v, want explicit empty parentId to mean root scope", err)
	}
	if len(document.Resources) != 1 || document.Resources[0].ParentID != "" {
		t.Fatalf("resources = %#v, want one root-scoped resource", document.Resources)
	}
}
