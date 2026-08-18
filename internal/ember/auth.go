package ember

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Principal struct {
	Token string
	Name  string
	Role  string
	Scope string
}

type Auth struct {
	byToken map[string]Principal
}

func NewAuth(principalsByToken map[string]Principal) Auth {
	return Auth{byToken: principalsByToken}
}

func DefaultTestAuth() Auth {
	return NewAuth(map[string]Principal{
		"owner-test-token":  {Token: "owner-test-token", Name: "local-owner", Role: "owner", Scope: "*"},
		"editor-test-token": {Token: "editor-test-token", Name: "local-editor", Role: "editor", Scope: "*"},
		"reader-test-token": {Token: "reader-test-token", Name: "local-reader", Role: "reader", Scope: "*"},
	})
}

func LoadAuthFile(authFilePath string) (Auth, error) {
	fileInfo, err := os.Stat(authFilePath)
	if err != nil {
		return Auth{}, err
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		return Auth{}, fmt.Errorf("auth file must be mode 0600")
	}
	var rawPrincipals map[string]struct {
		Principal string `json:"principal"`
		Role      string `json:"role"`
		Scope     string `json:"scope"`
	}
	authFileBytes, err := os.ReadFile(authFilePath)
	if err != nil {
		return Auth{}, err
	}
	if err := json.Unmarshal(authFileBytes, &rawPrincipals); err != nil {
		return Auth{}, err
	}
	principalsByToken := make(map[string]Principal, len(rawPrincipals))
	for token, rawPrincipal := range rawPrincipals {
		if token == "" || rawPrincipal.Principal == "" || rawPrincipal.Scope == "" {
			return Auth{}, fmt.Errorf("invalid auth fixture")
		}
		if rawPrincipal.Role != "owner" && rawPrincipal.Role != "editor" && rawPrincipal.Role != "reader" {
			return Auth{}, fmt.Errorf("invalid auth role")
		}
		principalsByToken[token] = Principal{
			Token: token,
			Name:  rawPrincipal.Principal,
			Role:  rawPrincipal.Role,
			Scope: rawPrincipal.Scope,
		}
	}
	return NewAuth(principalsByToken), nil
}

func (auth Auth) Authenticate(authorizationHeader string) (Principal, error) {
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authorizationHeader, bearerPrefix) {
		return Principal{}, ErrUnauthorized
	}
	bearerToken := strings.TrimSpace(strings.TrimPrefix(authorizationHeader, bearerPrefix))
	if bearerToken == "" {
		return Principal{}, ErrUnauthorized
	}
	principal, found := auth.byToken[bearerToken]
	if !found {
		return Principal{}, ErrUnauthorized
	}
	return principal, nil
}

func allowed(role, action string) bool {
	if role == "owner" {
		return true
	}
	if role == "editor" {
		switch action {
		case "read", "group:create", "group:delete", "bucket:create", "bucket:delete", "object:put", "object:delete":
			return true
		}
	}
	if role == "reader" && action == "read" {
		return true
	}
	return false
}

func inScope(principal Principal, requestedScope string) bool {
	return principal.Scope == "*" || principal.Scope == requestedScope || strings.HasPrefix(requestedScope, principal.Scope+"/")
}

func scopeString(instanceID, tenantID, subscriptionID, groupName string) string {
	return strings.Join([]string{instanceID, tenantID, subscriptionID, groupName}, "/")
}

func Allowed(principal Principal, action, scope string) bool {
	return inScope(principal, scope) && allowed(principal.Role, action)
}
