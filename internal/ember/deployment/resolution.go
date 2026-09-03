package deployment

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	// RedactedValue is the only value used by public resolution views for a
	// secure parameter or a secure interpolation.
	RedactedValue = "[REDACTED]"
)

var (
	// ErrInvalidResolution identifies missing, unknown, ambiguous, or unsafe
	// deployment inputs and references.
	ErrInvalidResolution = errors.New("invalid deployment resolution")
	// ErrSecretParameterUnavailable prevents callers from treating an ordinary
	// resolved view as a secret store.
	ErrSecretParameterUnavailable = errors.New("secret parameter unavailable")
)

// ResolutionError contains deterministic diagnostics without echoing supplied
// parameter values or resolved resource values.
type ResolutionError struct {
	Diagnostics []Diagnostic
}

func (err *ResolutionError) Error() string {
	if err == nil || len(err.Diagnostics) == 0 {
		return ErrInvalidResolution.Error()
	}
	parts := make([]string, 0, len(err.Diagnostics))
	for _, diagnostic := range err.Diagnostics {
		parts = append(parts, diagnostic.Path+": "+diagnostic.Message)
	}
	return ErrInvalidResolution.Error() + ": " + strings.Join(parts, "; ")
}

func (err *ResolutionError) Unwrap() error {
	return ErrInvalidResolution
}

// ResolvedParameter is the safe display form of one resolved parameter.
// Secure values always contain RedactedValue rather than the supplied secret.
type ResolvedParameter struct {
	Type  ParameterType `json:"type"`
	Value string        `json:"value"`
}

// ResolvedResource is the safe display form of a resource declaration. ID is a
// deterministic logical ID for references; the resource manager may assign a
// different persisted ID when a plan is applied.
type ResolvedResource struct {
	ID   string              `json:"id"`
	Spec models.ResourceSpec `json:"spec"`
}

// ResolvedDocument is safe to persist or return to an operator. It contains
// redaction markers for secure values and never contains the protected secret
// map held by Resolution.
type ResolvedDocument struct {
	Version    string                       `json:"version"`
	Parameters map[string]ResolvedParameter `json:"parameters,omitempty"`
	Resources  []ResolvedResource           `json:"resources"`
}

// Resolution holds a safe resolved document and a private set of secure input
// values. Use Secret only at an explicit protected boundary; all display and
// JSON methods remain redacted.
type Resolution struct {
	document ResolvedDocument
	secrets  map[string]string
}

// Document returns a defensive copy of the redacted resolved document.
func (resolution Resolution) Document() ResolvedDocument {
	return cloneResolvedDocument(resolution.document)
}

// Secret returns one secure parameter for an explicit protected consumer. It
// does not make the value part of the resolved document or JSON representation.
func (resolution Resolution) Secret(name string) (string, error) {
	if !validParameterName(name) {
		return "", ErrSecretParameterUnavailable
	}
	value, ok := resolution.secrets[name]
	if !ok {
		return "", ErrSecretParameterUnavailable
	}
	return value, nil
}

// Redact removes every secure input from an arbitrary display string. Longer
// values are replaced first so one secret cannot reveal a suffix of another.
func (resolution Resolution) Redact(value string) string {
	type secretValue struct {
		name  string
		value string
	}
	values := make([]secretValue, 0, len(resolution.secrets))
	for name, secret := range resolution.secrets {
		if secret != "" {
			values = append(values, secretValue{name: name, value: secret})
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i].value) == len(values[j].value) {
			return values[i].name < values[j].name
		}
		return len(values[i].value) > len(values[j].value)
	})
	for _, secret := range values {
		value = strings.ReplaceAll(value, secret.value, RedactedValue)
	}
	return value
}

// RedactError creates a display-only error whose message cannot reveal a
// secure input through Error or Unwrap.
func (resolution Resolution) RedactError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(resolution.Redact(err.Error()))
}

// MarshalJSON persists only the safe resolved document, never the protected
// secret map.
func (resolution Resolution) MarshalJSON() ([]byte, error) {
	return json.Marshal(resolution.document)
}

type resolvedParameterValue struct {
	value  string
	secret bool
}

type resourceSymbol struct {
	id           string
	name         string
	resourceType string
}

type resolutionContext struct {
	parameters        map[string]resolvedParameterValue
	declarations      map[string]ParameterDeclaration
	resources         map[string]resourceSymbol
	ambiguousResource map[string]struct{}
}

// Resolve applies supplied values over ordinary defaults, resolves bounded
// parameter/resource templates, and returns only a redacted public view.
// Supported templates are ${parameters.name}, ${resources.name.id},
// ${resources.name.name}, and ${resources.name.type}. Resource names are the
// symbolic names used by resource references and must be unique.
func Resolve(document Document, supplied map[string]string) (Resolution, error) {
	diagnostics := make([]Diagnostic, 0)
	if document.Version != CurrentVersion {
		appendResolutionDiagnostic(&diagnostics, "$.version", "unsupported_version", "document version is not supported")
	}
	if len(document.Parameters) > MaxParameterDeclarations {
		appendResolutionDiagnostic(&diagnostics, "$.parameters", "too_many_items", "parameters exceeds the configured limit")
	}
	if len(document.Resources) > MaxResourceDeclarations {
		appendResolutionDiagnostic(&diagnostics, "$.resources", "too_many_items", "resources exceeds the configured limit")
	}
	if len(supplied) > MaxParameterDeclarations {
		appendResolutionDiagnostic(&diagnostics, "$.parameters", "too_many_items", "supplied parameters exceeds the configured limit")
	}
	if len(diagnostics) > 0 {
		return Resolution{}, &ResolutionError{Diagnostics: diagnostics}
	}

	parameterNames := make([]string, 0, len(document.Parameters))
	for name := range document.Parameters {
		parameterNames = append(parameterNames, name)
	}
	sort.Strings(parameterNames)

	values := make(map[string]resolvedParameterValue, len(document.Parameters))
	secrets := make(map[string]string)
	resolvedParameters := make(map[string]ResolvedParameter, len(document.Parameters))
	for _, name := range parameterNames {
		declaration := document.Parameters[name]
		parameterPath := "$.parameters." + name
		if !validParameterName(name) {
			appendResolutionDiagnostic(&diagnostics, parameterPath, "invalid_parameter_name", "parameter name is invalid")
		}
		if declaration.Type != ParameterTypeString && declaration.Type != ParameterTypeSecureString {
			appendResolutionDiagnostic(&diagnostics, parameterPath+".type", "invalid_parameter_type", "parameter type is not supported")
			continue
		}
		if declaration.HasDefault && len(declaration.DefaultValue) > MaxParameterValueLength {
			appendResolutionDiagnostic(&diagnostics, parameterPath+".defaultValue", "value_too_large", "parameter value exceeds the configured limit")
		}
		if declaration.Type == ParameterTypeSecureString && declaration.HasDefault {
			appendResolutionDiagnostic(&diagnostics, parameterPath+".defaultValue", "secret_default_forbidden", "secure parameters cannot define a default value")
		}

		value, exists := supplied[name]
		if !exists && declaration.HasDefault {
			value, exists = declaration.DefaultValue, true
		}
		if !exists {
			appendResolutionDiagnostic(&diagnostics, parameterPath, "missing_parameter", "required parameter is not supplied")
			continue
		}
		if len(value) > MaxParameterValueLength {
			appendResolutionDiagnostic(&diagnostics, parameterPath, "value_too_large", "parameter value exceeds the configured limit")
			continue
		}
		if declaration.Type == ParameterTypeSecureString {
			values[name] = resolvedParameterValue{value: value, secret: true}
			secrets[name] = value
			resolvedParameters[name] = ResolvedParameter{Type: declaration.Type, Value: RedactedValue}
			continue
		}
		values[name] = resolvedParameterValue{value: value}
		resolvedParameters[name] = ResolvedParameter{Type: declaration.Type, Value: value}
	}

	suppliedNames := make([]string, 0, len(supplied))
	for name := range supplied {
		suppliedNames = append(suppliedNames, name)
	}
	sort.Strings(suppliedNames)
	for _, name := range suppliedNames {
		if _, declared := document.Parameters[name]; !declared {
			appendResolutionDiagnostic(&diagnostics, "$.parameters", "unknown_parameter", "supplied parameter is not declared")
		}
	}

	context := resolutionContext{
		parameters:   values,
		declarations: document.Parameters,
	}
	resolvedNames := make([]string, len(document.Resources))
	resourceSymbols := make(map[string]resourceSymbol, len(document.Resources))
	ambiguousResources := make(map[string]struct{})
	for index, spec := range document.Resources {
		path := "$.resources[" + strconv.Itoa(index) + "]"
		name, usesSecret := resolveTemplate(spec.Name, path+".name", context, false, &diagnostics)
		if usesSecret {
			appendResolutionDiagnostic(&diagnostics, path+".name", "secret_in_identity", "secure parameters cannot resolve resource identity")
		}
		resolvedNames[index] = name
		if name == "" {
			continue
		}
		symbol := resourceSymbol{
			id:           logicalResourceID(spec.Type, name),
			name:         name,
			resourceType: string(spec.Type),
		}
		if _, alreadyAmbiguous := ambiguousResources[name]; alreadyAmbiguous {
			continue
		}
		if _, exists := resourceSymbols[name]; exists {
			delete(resourceSymbols, name)
			ambiguousResources[name] = struct{}{}
			appendResolutionDiagnostic(&diagnostics, "$.resources", "ambiguous_resource_name", "resource names must be unique for references")
			continue
		}
		resourceSymbols[name] = symbol
	}
	context.resources = resourceSymbols
	context.ambiguousResource = ambiguousResources

	resolvedResources := make([]ResolvedResource, len(document.Resources))
	for index, spec := range document.Resources {
		path := "$.resources[" + strconv.Itoa(index) + "]"
		resolvedSpec := spec
		resolvedSpec.Name = resolvedNames[index]

		parentID, usesSecret := resolveTemplate(spec.ParentID, path+".parentId", context, true, &diagnostics)
		if usesSecret {
			appendResolutionDiagnostic(&diagnostics, path+".parentId", "secret_in_identity", "secure parameters cannot resolve resource identity")
		}
		resolvedSpec.ParentID = parentID

		if spec.Tags != nil {
			resolvedSpec.Tags = make(map[string]string, len(spec.Tags))
			tagKeys := make([]string, 0, len(spec.Tags))
			for key := range spec.Tags {
				tagKeys = append(tagKeys, key)
			}
			sort.Strings(tagKeys)
			for _, key := range tagKeys {
				resolvedTag, _ := resolveTemplate(spec.Tags[key], path+".tags."+key, context, true, &diagnostics)
				resolvedSpec.Tags[key] = resolvedTag
			}
		}

		resolvedSpec.Provider.Namespace, _ = resolveTemplate(spec.Provider.Namespace, path+".provider.namespace", context, true, &diagnostics)
		resolvedSpec.Provider.Type, _ = resolveTemplate(spec.Provider.Type, path+".provider.type", context, true, &diagnostics)
		resolvedSpec.Provider.Version, _ = resolveTemplate(spec.Provider.Version, path+".provider.version", context, true, &diagnostics)
		resolvedState, stateUsesSecret := resolveTemplate(string(spec.DesiredState), path+".desiredState", context, true, &diagnostics)
		if stateUsesSecret {
			appendResolutionDiagnostic(&diagnostics, path+".desiredState", "secret_in_state", "secure parameters cannot resolve desired state")
		}
		resolvedSpec.DesiredState = models.ResourceState(resolvedState)

		if err := resolvedSpec.Validate(); err != nil {
			appendResolutionDiagnostic(&diagnostics, path, "invalid_resolved_resource", "resolved resource is not valid")
		}
		resolvedResources[index] = ResolvedResource{
			ID:   logicalResourceID(spec.Type, resolvedSpec.Name),
			Spec: resolvedSpec,
		}
	}

	if len(diagnostics) > 0 {
		return Resolution{}, &ResolutionError{Diagnostics: diagnostics}
	}
	return Resolution{
		document: ResolvedDocument{
			Version:    document.Version,
			Parameters: resolvedParameters,
			Resources:  resolvedResources,
		},
		secrets: secrets,
	}, nil
}

func appendResolutionDiagnostic(diagnostics *[]Diagnostic, path, code, message string) {
	*diagnostics = append(*diagnostics, Diagnostic{Path: path, Code: code, Message: message})
}

func resolveTemplate(value, path string, context resolutionContext, allowResourceReferences bool, diagnostics *[]Diagnostic) (string, bool) {
	if !strings.Contains(value, "${") {
		return value, false
	}

	var builder strings.Builder
	builder.Grow(len(value))
	usesSecret := false
	cursor := 0
	for cursor < len(value) {
		relativeStart := strings.Index(value[cursor:], "${")
		if relativeStart < 0 {
			builder.WriteString(value[cursor:])
			break
		}
		start := cursor + relativeStart
		builder.WriteString(value[cursor:start])
		relativeEnd := strings.IndexByte(value[start+2:], '}')
		if relativeEnd < 0 {
			appendResolutionDiagnostic(diagnostics, path, "invalid_reference", "reference is not complete")
			builder.WriteString(value[start:])
			break
		}
		end := start + 2 + relativeEnd
		token := value[start+2 : end]
		kind, name, attribute, ok := referenceParts(token)
		replacement := ""
		secret := false
		if !ok {
			appendResolutionDiagnostic(diagnostics, path, "invalid_reference", "reference syntax is not supported")
			cursor = end + 1
			continue
		}
		if kind == "resource" && !allowResourceReferences {
			appendResolutionDiagnostic(diagnostics, path, "resource_reference_in_identity", "resource references cannot resolve resource identity")
			cursor = end + 1
			continue
		}

		switch kind {
		case "parameter":
			parameter, exists := context.parameters[name]
			if !exists {
				if _, declared := context.declarations[name]; declared {
					appendResolutionDiagnostic(diagnostics, path, "missing_parameter", "referenced parameter is not supplied")
				} else {
					appendResolutionDiagnostic(diagnostics, path, "unknown_parameter", "referenced parameter is not declared")
				}
				cursor = end + 1
				continue
			}
			replacement = parameter.value
			secret = parameter.secret
		case "resource":
			if _, ambiguous := context.ambiguousResource[name]; ambiguous {
				appendResolutionDiagnostic(diagnostics, path, "ambiguous_resource_reference", "resource reference matches multiple declarations")
				cursor = end + 1
				continue
			}
			resource, exists := context.resources[name]
			if !exists {
				appendResolutionDiagnostic(diagnostics, path, "missing_resource_reference", "resource reference does not match a declaration")
				cursor = end + 1
				continue
			}
			switch attribute {
			case "id":
				replacement = resource.id
			case "name":
				replacement = resource.name
			case "type":
				replacement = resource.resourceType
			}
		}
		if secret {
			builder.WriteString(RedactedValue)
			usesSecret = true
		} else {
			builder.WriteString(replacement)
		}
		cursor = end + 1
	}
	return builder.String(), usesSecret
}

func referenceParts(token string) (kind, name, attribute string, ok bool) {
	if strings.HasPrefix(token, "parameters.") {
		name = strings.TrimPrefix(token, "parameters.")
		if validParameterName(name) {
			return "parameter", name, "", true
		}
		return "", "", "", false
	}
	if !strings.HasPrefix(token, "resources.") {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(token, "resources.")
	for _, candidate := range []string{"id", "name", "type"} {
		suffix := "." + candidate
		if strings.HasSuffix(rest, suffix) {
			name = strings.TrimSuffix(rest, suffix)
			if name != "" {
				return "resource", name, candidate, true
			}
		}
	}
	return "", "", "", false
}

func logicalResourceID(resourceType models.ResourceType, name string) string {
	return "/resources/" + url.PathEscape(string(resourceType)) + "/" + url.PathEscape(name)
}

func cloneResolvedDocument(document ResolvedDocument) ResolvedDocument {
	clone := ResolvedDocument{Version: document.Version}
	if document.Parameters != nil {
		clone.Parameters = make(map[string]ResolvedParameter, len(document.Parameters))
		for name, parameter := range document.Parameters {
			clone.Parameters[name] = parameter
		}
	}
	if document.Resources != nil {
		clone.Resources = make([]ResolvedResource, len(document.Resources))
		for index, resource := range document.Resources {
			clone.Resources[index] = resource
			if resource.Spec.Tags != nil {
				clone.Resources[index].Spec.Tags = make(map[string]string, len(resource.Spec.Tags))
				for key, value := range resource.Spec.Tags {
					clone.Resources[index].Spec.Tags[key] = value
				}
			}
		}
	}
	return clone
}
