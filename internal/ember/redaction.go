package ember

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// declarativeRedactionMarker is intentionally stable so callers can identify a
// redacted value without learning anything about the original value.
const declarativeRedactionMarker = "[REDACTED]"

// RedactDeclarativeSpecJSON applies the bounded Phase 2 redaction policy before
// declarative state is returned or persisted. It is not a general secret store:
// values under names the policy does not recognize remain caller-managed data.
func RedactDeclarativeSpecJSON(specJSON []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(specJSON, &value); err != nil {
		return nil, fmt.Errorf("decode declarative spec for redaction: %w", err)
	}
	redacted, err := json.Marshal(redactDeclarativeJSON(value))
	if err != nil {
		return nil, fmt.Errorf("encode redacted declarative spec: %w", err)
	}
	return redacted, nil
}

func redactedResourceCanonicalJSON(resource ResourceSpec) ([]byte, error) {
	canonical, err := resourceCanonicalJSON(resource)
	if err != nil {
		return nil, err
	}
	return RedactDeclarativeSpecJSON(canonical)
}

func redactDeclarativeJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for key, child := range typed {
			switch {
			case isSecretReferenceField(key):
				redacted[key] = redactSecretReferences(child)
			case sensitiveDeclarativeName(key):
				redacted[key] = declarativeRedactionMarker
			default:
				redacted[key] = redactDeclarativeJSON(child)
			}
		}
		return redacted
	case []any:
		redacted := make([]any, len(typed))
		for index, child := range typed {
			redacted[index] = redactDeclarativeJSON(child)
		}
		return redacted
	default:
		return value
	}
}

// Secret references retain their array/object shape so a redacted state can
// still be decoded for child-before-parent deletion. Their identifying values
// are never persisted or emitted.
func redactSecretReferences(value any) any {
	references, ok := value.([]any)
	if !ok {
		return declarativeRedactionMarker
	}
	redacted := make([]any, len(references))
	for index, reference := range references {
		object, ok := reference.(map[string]any)
		if !ok {
			redacted[index] = declarativeRedactionMarker
			continue
		}
		redactedObject := make(map[string]any, len(object))
		for key, child := range object {
			if strings.EqualFold(key, "name") || strings.EqualFold(key, "key") {
				redactedObject[key] = declarativeRedactionMarker
				continue
			}
			redactedObject[key] = redactDeclarativeJSON(child)
		}
		redacted[index] = redactedObject
	}
	return redacted
}

func isSecretReferenceField(name string) bool {
	compact := compactDeclarativeName(name)
	return compact == "secretref" || compact == "secretrefs"
}

func sensitiveDeclarativePath(path string) bool {
	for _, segment := range strings.Split(path, ".") {
		if segment == "" || isArrayIndex(segment) {
			continue
		}
		if isSecretReferenceField(segment) || sensitiveDeclarativeName(segment) {
			return true
		}
	}
	return false
}

func isArrayIndex(segment string) bool {
	if segment == "" {
		return false
	}
	for _, character := range segment {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func redactPlanValue(path string, value any) any {
	segments := strings.Split(path, ".")
	if len(segments) > 0 && isSecretReferenceField(segments[len(segments)-1]) {
		return redactSecretReferences(value)
	}
	if sensitiveDeclarativePath(path) {
		return declarativeRedactionMarker
	}
	return redactDeclarativeJSON(value)
}

func compactDeclarativeName(name string) string {
	var compact strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			compact.WriteRune(character)
		}
	}
	return compact.String()
}

func sensitiveDeclarativeName(name string) bool {
	compact := compactDeclarativeName(name)
	if compact == "" {
		return false
	}
	for _, fragment := range []string{
		"secret", "password", "passwd", "pwd", "token", "credential", "authorization", "bearer", "connectionstring", "key", "privatekey", "apikey", "accesskey", "clientsecret", "clientkey", "encryptionkey", "signingkey", "secretkey",
	} {
		if strings.Contains(compact, fragment) {
			return true
		}
	}
	return false
}
