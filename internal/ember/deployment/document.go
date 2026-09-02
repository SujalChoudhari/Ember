package deployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	// CurrentVersion is the only deployment document schema currently supported.
	CurrentVersion = "v1"
	// MaxDocumentBytes bounds parser input before JSON decoding begins.
	MaxDocumentBytes = 64 << 10
	// MaxResourceDeclarations bounds the resource collection in one document.
	MaxResourceDeclarations = 100
)

var (
	// ErrInvalidDocument identifies schema validation failures.
	ErrInvalidDocument = errors.New("invalid deployment document")
	// ErrMalformedDocument identifies invalid JSON syntax or trailing data.
	ErrMalformedDocument = errors.New("malformed deployment document")
	// ErrDocumentTooLarge identifies input above MaxDocumentBytes.
	ErrDocumentTooLarge = errors.New("deployment document exceeds size limit")
)

// Diagnostic identifies a schema problem without including input values.
type Diagnostic struct {
	Path    string
	Code    string
	Message string
}

// ValidationError contains deterministic, document-located schema diagnostics.
type ValidationError struct {
	Diagnostics []Diagnostic
}

func (err *ValidationError) Error() string {
	if err == nil || len(err.Diagnostics) == 0 {
		return ErrInvalidDocument.Error()
	}
	parts := make([]string, 0, len(err.Diagnostics))
	for _, diagnostic := range err.Diagnostics {
		parts = append(parts, diagnostic.Path+": "+diagnostic.Message)
	}
	return ErrInvalidDocument.Error() + ": " + strings.Join(parts, "; ")
}

func (err *ValidationError) Unwrap() error {
	return ErrInvalidDocument
}

// Document is a validated v1 deployment document ready for a later planner.
type Document struct {
	Version   string
	Resources []models.ResourceSpec
}

// ParseAndValidate parses one bounded JSON deployment document and validates
// the supported v1 schema without mutating any Ember state.
func ParseAndValidate(data []byte) (Document, error) {
	if len(data) > MaxDocumentBytes {
		return Document{}, ErrDocumentTooLarge
	}

	object, err := decodeDocumentObject(data)
	if err != nil {
		return Document{}, err
	}

	diagnostics := make([]Diagnostic, 0)
	appendUnknownFieldDiagnostics(&diagnostics, object, "$", map[string]struct{}{
		"version":   {},
		"resources": {},
	})

	version, versionOK := parseStringField(object, "$", "version", true, &diagnostics)
	if versionOK && version != CurrentVersion {
		diagnostics = append(diagnostics, Diagnostic{
			Path:    "$.version",
			Code:    "unsupported_version",
			Message: "document version is not supported",
		})
	}

	resourceValues, resourcesOK := parseResourceValues(object, "$.resources", &diagnostics)
	resourceSpecs := make([]models.ResourceSpec, 0, len(resourceValues))
	if resourcesOK {
		if len(resourceValues) > MaxResourceDeclarations {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    "$.resources",
				Code:    "too_many_items",
				Message: "resources exceeds the configured limit",
			})
		} else {
			for index, rawResource := range resourceValues {
				resourceSpecs = append(resourceSpecs, parseResource(rawResource, index, &diagnostics))
			}
		}
	}

	if len(diagnostics) > 0 {
		return Document{}, &ValidationError{Diagnostics: diagnostics}
	}
	return Document{Version: version, Resources: resourceSpecs}, nil
}

func decodeDocumentObject(data []byte) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrMalformedDocument
	}
	if trimmed[0] != '{' {
		return nil, &ValidationError{Diagnostics: []Diagnostic{{
			Path:    "$",
			Code:    "invalid_type",
			Message: "document must be an object",
		}}}
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
		return nil, ErrMalformedDocument
	}
	return object, nil
}

func decodeObjectField(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

func appendUnknownFieldDiagnostics(diagnostics *[]Diagnostic, object map[string]json.RawMessage, path string, allowed map[string]struct{}) {
	keys := make([]string, 0, len(object))
	for key := range object {
		if _, ok := allowed[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for range keys {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    path + ".[unknown]",
			Code:    "unknown_field",
			Message: "field is not supported",
		})
	}
}

func parseStringField(object map[string]json.RawMessage, objectPath, fieldName string, required bool, diagnostics *[]Diagnostic) (string, bool) {
	raw, exists := object[fieldName]
	if !exists {
		if required {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    objectPath + "." + fieldName,
				Code:    "required_field",
				Message: fieldName + " is required",
			})
		}
		return "", false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    objectPath + "." + fieldName,
			Code:    "invalid_type",
			Message: fieldName + " must be a string",
		})
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    objectPath + "." + fieldName,
			Code:    "invalid_type",
			Message: fieldName + " must be a string",
		})
		return "", false
	}
	return value, true
}

func parseResourceValues(object map[string]json.RawMessage, path string, diagnostics *[]Diagnostic) ([]json.RawMessage, bool) {
	raw, exists := object["resources"]
	if !exists {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    path,
			Code:    "required_field",
			Message: "resources is required",
		})
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    path,
			Code:    "invalid_type",
			Message: "resources must be an array",
		})
		return nil, false
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    path,
			Code:    "invalid_type",
			Message: "resources must be an array",
		})
		return nil, false
	}
	return values, true
}

func parseResource(raw json.RawMessage, index int, diagnostics *[]Diagnostic) models.ResourceSpec {
	path := "$.resources[" + strconv.Itoa(index) + "]"
	object, ok := decodeObjectField(raw)
	if !ok {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    path,
			Code:    "invalid_type",
			Message: "resource must be an object",
		})
		return models.ResourceSpec{}
	}
	appendUnknownFieldDiagnostics(diagnostics, object, path, map[string]struct{}{
		"type":         {},
		"name":         {},
		"parentId":     {},
		"tags":         {},
		"provider":     {},
		"desiredState": {},
	})

	var spec models.ResourceSpec
	resourceType, typeOK := parseStringField(object, path, "type", true, diagnostics)
	if typeOK {
		spec.Type = models.ResourceType(resourceType)
		if !validResourceType(spec.Type) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    path + ".type",
				Code:    "invalid_resource_type",
				Message: "resource type is not supported",
			})
		}
	}

	name, nameOK := parseStringField(object, path, "name", true, diagnostics)
	if nameOK {
		spec.Name = name
		if strings.TrimSpace(name) == "" || len(name) > models.MaxResourceNameLength {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    path + ".name",
				Code:    "invalid_resource_name",
				Message: "resource name is empty or exceeds the size limit",
			})
		}
	}

	if _, exists := object["parentId"]; exists {
		parentID, parentOK := parseStringField(object, path, "parentId", false, diagnostics)
		if parentOK {
			spec.ParentID = parentID
			if (parentID != "" && strings.TrimSpace(parentID) == "") || len(parentID) > models.MaxParentIDLength {
				*diagnostics = append(*diagnostics, Diagnostic{
					Path:    path + ".parentId",
					Code:    "invalid_parent_id",
					Message: "parentId is blank or exceeds the size limit",
				})
			}
		}
	}

	if tags, tagsOK := parseTags(object, path, diagnostics); tagsOK {
		spec.Tags = tags
	}
	if provider, providerOK := parseProvider(object, path, diagnostics); providerOK {
		spec.Provider = provider
	}
	if desiredState, desiredOK := parseDesiredState(object, path, diagnostics); desiredOK {
		spec.DesiredState = desiredState
	}
	return spec
}

func parseTags(object map[string]json.RawMessage, resourcePath string, diagnostics *[]Diagnostic) (map[string]string, bool) {
	raw, exists := object["tags"]
	if !exists {
		return nil, true
	}
	tagsPath := resourcePath + ".tags"
	tagObject, ok := decodeObjectField(raw)
	if !ok {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    tagsPath,
			Code:    "invalid_type",
			Message: "tags must be an object of string values",
		})
		return nil, false
	}
	if len(tagObject) > models.MaxResourceTagCount {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    tagsPath,
			Code:    "too_many_items",
			Message: "tags exceeds the configured limit",
		})
		return nil, false
	}

	keys := make([]string, 0, len(tagObject))
	for key := range tagObject {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tags := make(map[string]string, len(tagObject))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" || len(key) > models.MaxResourceTagKeyLength {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    tagsPath,
				Code:    "invalid_tag_key",
				Message: "tag key is empty or exceeds the size limit",
			})
			continue
		}
		value, valueOK := parseRawString(tagObject[key])
		if !valueOK {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    tagsPath,
				Code:    "invalid_type",
				Message: "tag values must be strings",
			})
			continue
		}
		if len(value) > models.MaxResourceTagValueLength {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    tagsPath,
				Code:    "invalid_tag_value",
				Message: "tag value exceeds the size limit",
			})
			continue
		}
		tags[key] = value
	}
	return tags, true
}

func parseProvider(object map[string]json.RawMessage, resourcePath string, diagnostics *[]Diagnostic) (models.ProviderMetadata, bool) {
	raw, exists := object["provider"]
	if !exists {
		return models.ProviderMetadata{}, true
	}
	providerPath := resourcePath + ".provider"
	providerObject, ok := decodeObjectField(raw)
	if !ok {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    providerPath,
			Code:    "invalid_type",
			Message: "provider must be an object",
		})
		return models.ProviderMetadata{}, false
	}
	appendUnknownFieldDiagnostics(diagnostics, providerObject, providerPath, map[string]struct{}{
		"namespace": {},
		"type":      {},
		"version":   {},
	})

	var provider models.ProviderMetadata
	if namespace, namespaceOK := parseStringField(providerObject, providerPath, "namespace", false, diagnostics); namespaceOK {
		provider.Namespace = namespace
		if !validOptionalBoundedText(namespace, models.MaxProviderNamespaceLength) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    providerPath + ".namespace",
				Code:    "invalid_provider_namespace",
				Message: "provider namespace is empty or exceeds the size limit",
			})
		}
	}
	if providerType, typeOK := parseStringField(providerObject, providerPath, "type", false, diagnostics); typeOK {
		provider.Type = providerType
		if !validOptionalBoundedText(providerType, models.MaxProviderTypeLength) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    providerPath + ".type",
				Code:    "invalid_provider_type",
				Message: "provider type is empty or exceeds the size limit",
			})
		}
	}
	if version, versionOK := parseStringField(providerObject, providerPath, "version", false, diagnostics); versionOK {
		provider.Version = version
		if !validOptionalBoundedText(version, models.MaxProviderVersionLength) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Path:    providerPath + ".version",
				Code:    "invalid_provider_version",
				Message: "provider version is empty or exceeds the size limit",
			})
		}
	}
	return provider, true
}

func parseDesiredState(object map[string]json.RawMessage, resourcePath string, diagnostics *[]Diagnostic) (models.ResourceState, bool) {
	if _, exists := object["desiredState"]; !exists {
		return "", true
	}
	state, stateOK := parseStringField(object, resourcePath, "desiredState", false, diagnostics)
	if !stateOK {
		return "", false
	}
	resourceState := models.ResourceState(state)
	if !validResourceState(resourceState) {
		*diagnostics = append(*diagnostics, Diagnostic{
			Path:    resourcePath + ".desiredState",
			Code:    "invalid_resource_state",
			Message: "desiredState is not supported",
		})
		return "", false
	}
	return resourceState, true
}

func parseRawString(raw json.RawMessage) (string, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func validResourceType(resourceType models.ResourceType) bool {
	switch resourceType {
	case models.ResourceTypeGroup, models.ResourceTypeBucket:
		return true
	default:
		return false
	}
}

func validResourceState(state models.ResourceState) bool {
	switch state {
	case "", models.ResourceStateUnknown, models.ResourceStatePending, models.ResourceStateReady, models.ResourceStateFailed, models.ResourceStateDeleting:
		return true
	default:
		return false
	}
}

func validOptionalBoundedText(value string, maxLength int) bool {
	return value == "" || (strings.TrimSpace(value) != "" && len(value) <= maxLength)
}
