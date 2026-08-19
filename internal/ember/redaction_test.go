package ember

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactDeclarativeSpecJSONRedactsNestedSecretLikeNames(t *testing.T) {
	raw := []byte(`{"properties":{"authorizationHeader":"fixture-auth","keyMaterial":"fixture-key","password":"fixture-password","sessionToken":"fixture-token","privateKeyPem":"fixture-private-key","safe":"visible","nested":[{"accessKey":"fixture-access","safe":"nested-visible"}]},"secretRefs":[{"name":"fixture/ref","key":"fixture-key"}]}`)

	redacted, err := RedactDeclarativeSpecJSON(raw)
	if err != nil {
		t.Fatalf("redact declarative spec: %v", err)
	}
	for _, forbidden := range []string{"fixture-auth", "fixture-key", "fixture-password", "fixture-token", "fixture-private-key", "fixture-access", "fixture/ref"} {
		if strings.Contains(string(redacted), forbidden) {
			t.Fatalf("redacted spec exposed %q: %s", forbidden, redacted)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(redacted, &document); err != nil {
		t.Fatalf("decode redacted spec: %v", err)
	}
	properties := document["properties"].(map[string]any)
	if properties["authorizationHeader"] != declarativeRedactionMarker || properties["keyMaterial"] != declarativeRedactionMarker || properties["password"] != declarativeRedactionMarker || properties["sessionToken"] != declarativeRedactionMarker || properties["privateKeyPem"] != declarativeRedactionMarker {
		t.Fatalf("authorization/key/password/token/private-key values were not redacted: %#v", properties)
	}
	if properties["safe"] != "visible" {
		t.Fatalf("safe property was not retained: %#v", properties["safe"])
	}
	nested := properties["nested"].([]any)[0].(map[string]any)
	if nested["accessKey"] != declarativeRedactionMarker || nested["safe"] != "nested-visible" {
		t.Fatalf("nested redaction changed shape or safe values: %#v", nested)
	}
	secretRefs := document["secretRefs"].([]any)[0].(map[string]any)
	if secretRefs["name"] != declarativeRedactionMarker || secretRefs["key"] != declarativeRedactionMarker {
		t.Fatalf("secret reference identifiers were not redacted: %#v", secretRefs)
	}
}

func TestDeclarativeStateReadRedactsLegacyRawState(t *testing.T) {
	store := declarativeStore(t)
	store.declarative[declarativeStateKey(declarativeScope, "group")] = DeclarativeResourceState{
		LogicalID: "group",
		Scope:     declarativeScope,
		SpecJSON:  []byte(`{"properties":{"password":"legacy-fixture-secret","safe":"visible"},"secretRefs":[{"name":"legacy/ref","key":"password"}]}`),
	}

	states, err := store.ListDeclarativeResources(Principal{Name: "local-owner", Role: "owner", Scope: "*"}, declarativeScope)
	if err != nil {
		t.Fatalf("list declarative state: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("states=%d, want 1", len(states))
	}
	if strings.Contains(string(states[0].SpecJSON), "legacy-fixture-secret") || strings.Contains(string(states[0].SpecJSON), "legacy/ref") {
		t.Fatalf("legacy raw state was returned without redaction: %s", states[0].SpecJSON)
	}
	if !strings.Contains(string(states[0].SpecJSON), "visible") {
		t.Fatalf("safe state value was lost: %s", states[0].SpecJSON)
	}
}

func TestRedactDeclarativeSpecJSONMalformedStateDoesNotEchoInput(t *testing.T) {
	const fixtureSecret = "malformed-fixture-secret"
	_, err := RedactDeclarativeSpecJSON([]byte(`{"properties":{"password":"` + fixtureSecret + `",`))
	if err == nil {
		t.Fatal("malformed state unexpectedly redacted successfully")
	}
	if strings.Contains(err.Error(), fixtureSecret) {
		t.Fatalf("malformed-state error echoed secret fixture: %v", err)
	}
}
