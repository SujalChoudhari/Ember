package ember

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const DeclarativeAPIVersion = "ember/v1"

var (
	ErrMalformedDocument            = errors.New("malformed Ember declarative document")
	ErrUnsupportedDocumentVersion   = errors.New("unsupported Ember declarative document version")
	ErrDependencyCycle              = errors.New("declarative dependency cycle")
	ErrDestructiveApprovalRequired  = errors.New("explicit approval is required for destructive changes")
	ErrLifecyclePreventsDestruction = errors.New("lifecycle policy prevents destruction")
	declarativeIdentifierPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)
	declarativeParameterPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)
	declarativeTagPattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,62}$`)
)

// ContractError is intentionally safe to expose: it contains a document path and
// a validation reason, never a parameter value or a secret reference value.
type ContractError struct {
	Kind   error
	Path   string
	Detail string
}

func (err *ContractError) Error() string {
	if err.Path == "" {
		return fmt.Sprintf("%s: %s", err.Kind, err.Detail)
	}
	return fmt.Sprintf("%s at %s: %s", err.Kind, err.Path, err.Detail)
}

func (err *ContractError) Unwrap() error { return err.Kind }

func contractError(kind error, path, detail string) error {
	return &ContractError{Kind: kind, Path: path, Detail: detail}
}

type Parameter struct {
	Type        string `json:"type" yaml:"type"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Default     any    `json:"default,omitempty" yaml:"default,omitempty"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
}

type SecretReference struct {
	Name string `json:"name" yaml:"name"`
	Key  string `json:"key,omitempty" yaml:"key,omitempty"`
}

type LifecyclePolicy struct {
	PreventDestroy      bool `json:"preventDestroy,omitempty" yaml:"preventDestroy,omitempty"`
	DeleteBeforeReplace bool `json:"deleteBeforeReplace,omitempty" yaml:"deleteBeforeReplace,omitempty"`
}

type ResourceSpec struct {
	ID         string            `json:"id" yaml:"id"`
	Type       string            `json:"type" yaml:"type"`
	Name       string            `json:"name" yaml:"name"`
	Scope      string            `json:"scope" yaml:"scope"`
	Parent     string            `json:"parent,omitempty" yaml:"parent,omitempty"`
	DependsOn  []string          `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
	Properties map[string]any    `json:"properties,omitempty" yaml:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty" yaml:"tags,omitempty"`
	Lifecycle  LifecyclePolicy   `json:"lifecycle,omitempty" yaml:"lifecycle,omitempty"`
	Secrets    []SecretReference `json:"secretRefs,omitempty" yaml:"secretRefs,omitempty"`
}

type Output struct {
	Value       any    `json:"value" yaml:"value"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
}

type Document struct {
	APIVersion string               `json:"apiVersion" yaml:"apiVersion"`
	Parameters map[string]Parameter `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Resources  []ResourceSpec       `json:"resources" yaml:"resources"`
	Outputs    map[string]Output    `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Tags       map[string]string    `json:"tags,omitempty" yaml:"tags,omitempty"`
}

func ParseDocument(data []byte, filename string) (Document, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Document{}, contractError(ErrMalformedDocument, "document", "document is empty")
	}
	var document Document
	trimmed := bytes.TrimSpace(data)
	if strings.HasSuffix(strings.ToLower(filename), ".json") || trimmed[0] == '{' {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return Document{}, contractError(ErrMalformedDocument, "document", "invalid JSON syntax or field shape: "+err.Error())
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return Document{}, contractError(ErrMalformedDocument, "document", "multiple JSON values are not allowed")
			}
			return Document{}, contractError(ErrMalformedDocument, "document", "trailing JSON data: "+err.Error())
		}
	} else {
		decoder := yaml.NewDecoder(bytes.NewReader(trimmed))
		decoder.KnownFields(true)
		if err := decoder.Decode(&document); err != nil {
			return Document{}, contractError(ErrMalformedDocument, "document", "invalid YAML syntax or field shape: "+err.Error())
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				return Document{}, contractError(ErrMalformedDocument, "document", "multiple YAML documents are not allowed")
			}
			return Document{}, contractError(ErrMalformedDocument, "document", "trailing YAML data: "+err.Error())
		}
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return normalizeDocument(document), nil
}

func (document Document) Validate() error {
	if document.APIVersion != DeclarativeAPIVersion {
		if document.APIVersion == "" {
			return contractError(ErrMalformedDocument, "apiVersion", "apiVersion is required")
		}
		return contractError(ErrUnsupportedDocumentVersion, "apiVersion", fmt.Sprintf("received %q; supported version is ember/v1", document.APIVersion))
	}
	for name, parameter := range document.Parameters {
		if !declarativeParameterPattern.MatchString(name) {
			return contractError(ErrMalformedDocument, "parameters", "parameter names must match [A-Za-z][A-Za-z0-9_-]{0,62}")
		}
		switch parameter.Type {
		case "string", "integer", "boolean", "secret":
		default:
			return contractError(ErrMalformedDocument, "parameters."+name+".type", "type must be string, integer, boolean, or secret")
		}
		if parameter.Required && parameter.Default != nil {
			return contractError(ErrMalformedDocument, "parameters."+name, "required parameters cannot define a default")
		}
		if parameter.Type == "secret" && parameter.Default != nil {
			return contractError(ErrMalformedDocument, "parameters."+name+".default", "secret parameter defaults are not accepted")
		}
	}
	seen := map[string]struct{}{}
	for index, resource := range document.Resources {
		path := fmt.Sprintf("resources[%d]", index)
		if !declarativeIdentifierPattern.MatchString(resource.ID) {
			return contractError(ErrMalformedDocument, path+".id", "resource id must match [A-Za-z][A-Za-z0-9_-]{0,62}")
		}
		if _, exists := seen[resource.ID]; exists {
			return contractError(ErrMalformedDocument, path+".id", "resource id is duplicated")
		}
		seen[resource.ID] = struct{}{}
		if resource.Name == "" || strings.TrimSpace(resource.Name) != resource.Name {
			return contractError(ErrMalformedDocument, path+".name", "resource name is required and cannot have surrounding whitespace")
		}
		if resource.Scope == "" || strings.TrimSpace(resource.Scope) != resource.Scope {
			return contractError(ErrMalformedDocument, path+".scope", "resource scope is required and cannot have surrounding whitespace")
		}
		switch resource.Type {
		case "resourceGroup":
			if resource.Parent != "" {
				return contractError(ErrMalformedDocument, path+".parent", "resourceGroup cannot have a parent")
			}
		case "Ember.Blob/bucket":
			if resource.Parent == "" {
				return contractError(ErrMalformedDocument, path+".parent", "bucket requires a parent resourceGroup")
			}
		default:
			return contractError(ErrMalformedDocument, path+".type", "unsupported Phase 2 type; supported types are resourceGroup and Ember.Blob/bucket")
		}
		if err := validateTags(resource.Tags, path+".tags"); err != nil {
			return err
		}
		for dependencyIndex, dependency := range resource.DependsOn {
			if dependency == resource.ID {
				return contractError(ErrMalformedDocument, fmt.Sprintf("%s.dependsOn[%d]", path, dependencyIndex), "resource cannot depend on itself")
			}
			if !declarativeIdentifierPattern.MatchString(dependency) {
				return contractError(ErrMalformedDocument, fmt.Sprintf("%s.dependsOn[%d]", path, dependencyIndex), "dependency id is invalid")
			}
		}
		seenDependencies := map[string]struct{}{}
		for _, dependency := range resource.DependsOn {
			if _, exists := seenDependencies[dependency]; exists {
				return contractError(ErrMalformedDocument, path+".dependsOn", "dependency is duplicated")
			}
			seenDependencies[dependency] = struct{}{}
		}
		for secretIndex, secret := range resource.Secrets {
			if secret.Name == "" || strings.TrimSpace(secret.Name) != secret.Name {
				return contractError(ErrMalformedDocument, fmt.Sprintf("%s.secretRefs[%d].name", path, secretIndex), "secret reference name is required and cannot have surrounding whitespace")
			}
			if secret.Key != "" && strings.TrimSpace(secret.Key) != secret.Key {
				return contractError(ErrMalformedDocument, fmt.Sprintf("%s.secretRefs[%d].key", path, secretIndex), "secret reference key cannot have surrounding whitespace")
			}
		}
	}
	for index, resource := range document.Resources {
		if resource.Parent != "" {
			if _, exists := seen[resource.Parent]; !exists {
				return contractError(ErrMalformedDocument, fmt.Sprintf("resources[%d].parent", index), "parent resource does not exist")
			}
		}
		for dependencyIndex, dependency := range resource.DependsOn {
			if _, exists := seen[dependency]; !exists {
				return contractError(ErrMalformedDocument, fmt.Sprintf("resources[%d].dependsOn[%d]", index, dependencyIndex), "dependency resource does not exist")
			}
		}
	}
	if err := validateTags(document.Tags, "tags"); err != nil {
		return err
	}
	for name := range document.Outputs {
		if !declarativeIdentifierPattern.MatchString(name) {
			return contractError(ErrMalformedDocument, "outputs", "output names must match [A-Za-z][A-Za-z0-9_-]{0,62}")
		}
	}
	return nil
}

func validateTags(tags map[string]string, path string) error {
	for key, value := range tags {
		if !declarativeTagPattern.MatchString(key) {
			return contractError(ErrMalformedDocument, path, "tag keys must contain only letters, numbers, and .:/-_ characters")
		}
		if strings.TrimSpace(value) != value {
			return contractError(ErrMalformedDocument, path, "tag values cannot have surrounding whitespace")
		}
	}
	return nil
}

func normalizeDocument(document Document) Document {
	normalized := document
	normalized.Resources = append([]ResourceSpec(nil), document.Resources...)
	for index := range normalized.Resources {
		normalized.Resources[index].DependsOn = append([]string(nil), normalized.Resources[index].DependsOn...)
		sort.Strings(normalized.Resources[index].DependsOn)
		normalized.Resources[index].Secrets = append([]SecretReference(nil), normalized.Resources[index].Secrets...)
		sort.Slice(normalized.Resources[index].Secrets, func(left, right int) bool {
			if normalized.Resources[index].Secrets[left].Name == normalized.Resources[index].Secrets[right].Name {
				return normalized.Resources[index].Secrets[left].Key < normalized.Resources[index].Secrets[right].Key
			}
			return normalized.Resources[index].Secrets[left].Name < normalized.Resources[index].Secrets[right].Name
		})
	}
	sort.Slice(normalized.Resources, func(left, right int) bool { return normalized.Resources[left].ID < normalized.Resources[right].ID })
	return normalized
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonical declarative encoding failed: %w", err)
	}
	return encoded, nil
}

func resourceCanonicalJSON(resource ResourceSpec) ([]byte, error) {
	return canonicalJSON(normalizeDocument(Document{APIVersion: DeclarativeAPIVersion, Resources: []ResourceSpec{resource}}).Resources[0])
}

func resourceHash(resource ResourceSpec) (string, []byte, error) {
	canonical, err := resourceCanonicalJSON(resource)
	if err != nil {
		return "", nil, err
	}
	checksum := sha256.Sum256(canonical)
	return hex.EncodeToString(checksum[:]), canonical, nil
}

type DependencyEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

type DependencyGraph struct {
	Order []string         `json:"order"`
	Edges []DependencyEdge `json:"edges"`
}

func BuildDependencyGraph(document Document) (DependencyGraph, error) {
	document = normalizeDocument(document)
	dependencies := make(map[string]map[string]struct{}, len(document.Resources))
	indegree := make(map[string]int, len(document.Resources))
	adjacency := make(map[string][]string, len(document.Resources))
	for _, resource := range document.Resources {
		dependencies[resource.ID] = map[string]struct{}{}
		indegree[resource.ID] = 0
	}
	var edges []DependencyEdge
	for _, resource := range document.Resources {
		dependencySet := dependencies[resource.ID]
		for _, dependency := range resource.DependsOn {
			if _, exists := dependencies[dependency]; !exists {
				return DependencyGraph{}, contractError(ErrMalformedDocument, "resources."+resource.ID+".dependsOn", "dependency resource does not exist")
			}
			if _, exists := dependencySet[dependency]; exists {
				continue
			}
			dependencySet[dependency] = struct{}{}
			indegree[resource.ID]++
			adjacency[dependency] = append(adjacency[dependency], resource.ID)
			edges = append(edges, DependencyEdge{From: resource.ID, To: dependency, Reason: "dependsOn"})
		}
		if resource.Parent != "" {
			if _, exists := dependencies[resource.Parent]; !exists {
				return DependencyGraph{}, contractError(ErrMalformedDocument, "resources."+resource.ID+".parent", "parent resource does not exist")
			}
			if _, exists := dependencySet[resource.Parent]; !exists {
				dependencySet[resource.Parent] = struct{}{}
				indegree[resource.ID]++
				adjacency[resource.Parent] = append(adjacency[resource.Parent], resource.ID)
				edges = append(edges, DependencyEdge{From: resource.ID, To: resource.Parent, Reason: "parent"})
			}
		}
	}
	for key := range adjacency {
		sort.Strings(adjacency[key])
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].From == edges[right].From {
			return edges[left].To < edges[right].To
		}
		return edges[left].From < edges[right].From
	})
	ready := make([]string, 0)
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	order := make([]string, 0, len(indegree))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)
		for _, child := range adjacency[current] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(indegree) {
		blocked := make([]string, 0)
		for id, degree := range indegree {
			if degree > 0 {
				blocked = append(blocked, id)
			}
		}
		sort.Strings(blocked)
		return DependencyGraph{}, contractError(ErrDependencyCycle, "resources", "dependency cycle includes: "+strings.Join(blocked, ", "))
	}
	return DependencyGraph{Order: order, Edges: edges}, nil
}

type DeclarativeResourceState struct {
	LogicalID  string          `json:"logicalId"`
	ResourceID string          `json:"resourceId"`
	APIVersion string          `json:"apiVersion"`
	Type       string          `json:"type"`
	Scope      string          `json:"scope"`
	ParentID   string          `json:"parentId,omitempty"`
	SpecHash   string          `json:"specHash"`
	SpecJSON   json.RawMessage `json:"specJson"`
	Lifecycle  LifecyclePolicy `json:"lifecycle"`
}

type DeclarativeAuthority interface {
	ListDeclarativeResources(Principal, string) ([]DeclarativeResourceState, error)
	ApplyDeclarativeResource(Principal, ResourceSpec, string, string, string, string) (*Resource, *Operation, error)
	SaveDeclarativeState(Principal, DeclarativeResourceState, string, string) error
	DeleteDeclarativeState(Principal, string, string, string, string) error
}

type PlanAction string

const (
	PlanCreate  PlanAction = "create"
	PlanUpdate  PlanAction = "update"
	PlanNoop    PlanAction = "no-op"
	PlanDelete  PlanAction = "delete"
	PlanBlocked PlanAction = "blocked"
)

type PlanChange struct {
	Path        string `json:"path"`
	Before      any    `json:"before,omitempty"`
	After       any    `json:"after,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

type PlanEntry struct {
	LogicalID   string       `json:"logicalId"`
	ResourceID  string       `json:"resourceId,omitempty"`
	Type        string       `json:"type"`
	Action      PlanAction   `json:"action"`
	Order       int          `json:"order"`
	Changes     []PlanChange `json:"changes,omitempty"`
	DependsOn   []string     `json:"dependsOn,omitempty"`
	BlockedBy   []string     `json:"blockedBy,omitempty"`
	Destructive bool         `json:"destructive,omitempty"`
	Status      string       `json:"status"`
	OperationID string       `json:"operationId,omitempty"`
}

type Plan struct {
	APIVersion   string           `json:"apiVersion"`
	Scope        string           `json:"scope"`
	DocumentHash string           `json:"documentHash"`
	Order        []string         `json:"order"`
	Edges        []DependencyEdge `json:"edges"`
	BlockedEdges []DependencyEdge `json:"blockedEdges,omitempty"`
	Entries      []PlanEntry      `json:"entries"`
}

type ApplyOptions struct {
	Scope              string
	RequestID          string
	CorrelationID      string
	ApproveDestructive bool
}

type ApplyResult struct {
	Plan       Plan         `json:"plan"`
	Operations []*Operation `json:"operations,omitempty"`
}

func (options ApplyOptions) identifiers() (string, string) {
	requestID, correlationID := options.RequestID, options.CorrelationID
	if requestID == "" {
		requestID = NewRequestID()
	}
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}

type DeclarativeEngine struct {
	authority DeclarativeAuthority
}

func NewDeclarativeEngine(authority DeclarativeAuthority) *DeclarativeEngine {
	return &DeclarativeEngine{authority: authority}
}

func (engine *DeclarativeEngine) PlanDocument(principal Principal, data []byte, filename, scope string) (Plan, error) {
	document, err := ParseDocument(data, filename)
	if err != nil {
		return Plan{}, err
	}
	return engine.Plan(principal, document, scope)
}

func (engine *DeclarativeEngine) Plan(principal Principal, document Document, scope string) (Plan, error) {
	if engine == nil || engine.authority == nil {
		return Plan{}, fmt.Errorf("declarative authority is required")
	}
	if err := document.Validate(); err != nil {
		return Plan{}, err
	}
	if scope == "" {
		return Plan{}, contractError(ErrMalformedDocument, "scope", "apply scope is required")
	}
	graph, err := BuildDependencyGraph(document)
	if err != nil {
		return Plan{}, err
	}
	states, err := engine.authority.ListDeclarativeResources(principal, scope)
	if err != nil {
		return Plan{}, err
	}
	stateByLogicalID := make(map[string]DeclarativeResourceState, len(states))
	for _, state := range states {
		stateByLogicalID[state.LogicalID] = state
	}
	resourceByID := make(map[string]ResourceSpec, len(document.Resources))
	for _, resource := range document.Resources {
		if resource.Scope != scope {
			return Plan{}, contractError(ErrMalformedDocument, "resources."+resource.ID+".scope", "resource scope must equal apply scope")
		}
		resourceByID[resource.ID] = resource
	}
	documentJSON, err := canonicalJSON(normalizeDocument(document))
	if err != nil {
		return Plan{}, err
	}
	documentChecksum := sha256.Sum256(documentJSON)
	plan := Plan{APIVersion: document.APIVersion, Scope: scope, DocumentHash: hex.EncodeToString(documentChecksum[:]), Order: append([]string(nil), graph.Order...), Edges: append([]DependencyEdge(nil), graph.Edges...)}
	orderByID := make(map[string]int, len(graph.Order))
	for index, logicalID := range graph.Order {
		orderByID[logicalID] = index
	}
	for _, logicalID := range graph.Order {
		resource := resourceByID[logicalID]
		hash, canonical, hashErr := resourceHash(resource)
		if hashErr != nil {
			return Plan{}, hashErr
		}
		state, exists := stateByLogicalID[logicalID]
		entry := PlanEntry{LogicalID: logicalID, ResourceID: state.ResourceID, Type: resource.Type, Order: orderByID[logicalID], DependsOn: append([]string(nil), resource.DependsOn...)}
		if resource.Parent != "" && !contains(entry.DependsOn, resource.Parent) {
			entry.DependsOn = append(entry.DependsOn, resource.Parent)
		}
		sort.Strings(entry.DependsOn)
		switch {
		case !exists:
			entry.Action, entry.Status = PlanCreate, "pending"
		case state.SpecHash == hash:
			entry.Action, entry.Status = PlanNoop, "converged"
		default:
			entry.Action, entry.Status = PlanUpdate, "pending"
			entry.Changes, entry.Destructive = diffResourceSpecs(state.SpecJSON, canonical)
		}
		if entry.Destructive {
			entry.BlockedBy = append(entry.BlockedBy, "destructive approval required")
			plan.BlockedEdges = append(plan.BlockedEdges, DependencyEdge{From: logicalID, To: "approval", Reason: "destructive change"})
		}
		plan.Entries = append(plan.Entries, entry)
	}
	stale := make([]DeclarativeResourceState, 0)
	for _, state := range states {
		if _, exists := resourceByID[state.LogicalID]; !exists {
			stale = append(stale, state)
		}
	}
	sort.Slice(stale, func(left, right int) bool { return stale[left].LogicalID < stale[right].LogicalID })
	stale = orderStaleStates(stale)
	for index, state := range stale {
		plan.Order = append(plan.Order, state.LogicalID)
		entry := PlanEntry{LogicalID: state.LogicalID, ResourceID: state.ResourceID, Type: state.Type, Action: PlanDelete, Order: len(graph.Order) + index, Status: "pending", Destructive: true, BlockedBy: []string{"destructive approval required"}}
		if state.Lifecycle.PreventDestroy {
			entry.BlockedBy = append(entry.BlockedBy, "lifecycle.preventDestroy")
			entry.Status = "blocked"
			plan.BlockedEdges = append(plan.BlockedEdges, DependencyEdge{From: state.LogicalID, To: "lifecycle.preventDestroy", Reason: "lifecycle policy"})
		} else {
			plan.BlockedEdges = append(plan.BlockedEdges, DependencyEdge{From: state.LogicalID, To: "approval", Reason: "destructive change"})
		}
		plan.Entries = append(plan.Entries, entry)
	}
	return plan, nil
}

func orderStaleStates(states []DeclarativeResourceState) []DeclarativeResourceState {
	logicalByResourceID := make(map[string]string, len(states))
	for _, state := range states {
		logicalByResourceID[state.ResourceID] = state.LogicalID
	}
	parentByLogicalID := make(map[string]string, len(states))
	for _, state := range states {
		if parentLogicalID, exists := logicalByResourceID[state.ParentID]; exists {
			parentByLogicalID[state.LogicalID] = parentLogicalID
		}
	}
	depthByLogicalID := make(map[string]int, len(states))
	var depth func(string, map[string]bool) int
	depth = func(logicalID string, visiting map[string]bool) int {
		if value, exists := depthByLogicalID[logicalID]; exists {
			return value
		}
		if visiting[logicalID] {
			return 0
		}
		visiting[logicalID] = true
		value := 0
		if parentLogicalID, exists := parentByLogicalID[logicalID]; exists {
			value = depth(parentLogicalID, visiting) + 1
		}
		delete(visiting, logicalID)
		depthByLogicalID[logicalID] = value
		return value
	}
	for _, state := range states {
		depth(state.LogicalID, map[string]bool{})
	}
	sort.SliceStable(states, func(left, right int) bool {
		leftDepth := depthByLogicalID[states[left].LogicalID]
		rightDepth := depthByLogicalID[states[right].LogicalID]
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return states[left].LogicalID < states[right].LogicalID
	})
	return states
}

func diffResourceSpecs(beforeJSON, afterJSON []byte) ([]PlanChange, bool) {
	var before, after map[string]any
	if json.Unmarshal(beforeJSON, &before) != nil || json.Unmarshal(afterJSON, &after) != nil {
		return []PlanChange{{Path: "spec", Before: string(beforeJSON), After: string(afterJSON)}}, false
	}
	changes := make([]PlanChange, 0)
	destructive := false
	keys := map[string]struct{}{}
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	for _, key := range sortedKeys {
		beforeValue, beforeExists := before[key]
		afterValue, afterExists := after[key]
		changes, destructive = appendValueChanges(changes, destructive, key, key, beforeValue, beforeExists, afterValue, afterExists)
	}
	return changes, destructive
}

func appendValueChanges(changes []PlanChange, destructive bool, path, root string, before any, beforeExists bool, after any, afterExists bool) ([]PlanChange, bool) {
	beforeMap, beforeMapOK := before.(map[string]any)
	afterMap, afterMapOK := after.(map[string]any)
	if beforeExists && afterExists && beforeMapOK && afterMapOK {
		keys := map[string]struct{}{}
		for key := range beforeMap {
			keys[key] = struct{}{}
		}
		for key := range afterMap {
			keys[key] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for key := range keys {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		for _, key := range sortedKeys {
			var childBefore, childAfter any
			childBefore, beforeExists = beforeMap[key]
			childAfter, afterExists = afterMap[key]
			changes, destructive = appendValueChanges(changes, destructive, path+"."+key, root, childBefore, beforeExists, childAfter, afterExists)
		}
		return changes, destructive
	}
	beforeBytes, _ := json.Marshal(before)
	afterBytes, _ := json.Marshal(after)
	if beforeExists == afterExists && bytes.Equal(beforeBytes, afterBytes) {
		return changes, destructive
	}
	changeIsDestructive := root == "name" || root == "type" || root == "scope" || root == "parent"
	if (root == "tags" || root == "properties") && !afterExists {
		changeIsDestructive = true
	}
	changes = append(changes, PlanChange{Path: path, Before: before, After: after, Destructive: changeIsDestructive})
	return changes, destructive || changeIsDestructive
}

func (engine *DeclarativeEngine) ApplyDocument(principal Principal, data []byte, filename string, options ApplyOptions) (*ApplyResult, error) {
	document, err := ParseDocument(data, filename)
	if err != nil {
		return nil, err
	}
	return engine.Apply(principal, document, options)
}

func (engine *DeclarativeEngine) Apply(principal Principal, document Document, options ApplyOptions) (*ApplyResult, error) {
	plan, err := engine.Plan(principal, document, options.Scope)
	if err != nil {
		return nil, err
	}
	result := &ApplyResult{Plan: plan}
	for index := range result.Plan.Entries {
		entry := &result.Plan.Entries[index]
		if entry.Destructive && !options.ApproveDestructive {
			entry.Status = "blocked"
			continue
		}
		if entry.Action == PlanDelete && contains(entry.BlockedBy, "lifecycle.preventDestroy") {
			entry.Status = "blocked"
			continue
		}
	}
	for _, entry := range result.Plan.Entries {
		if entry.Destructive && !options.ApproveDestructive {
			return result, ErrDestructiveApprovalRequired
		}
		if entry.Action == PlanDelete && contains(entry.BlockedBy, "lifecycle.preventDestroy") {
			return result, ErrLifecyclePreventsDestruction
		}
	}
	requestID, correlationID := options.identifiers()
	states, err := engine.authority.ListDeclarativeResources(principal, options.Scope)
	if err != nil {
		return nil, err
	}
	stateByLogicalID := make(map[string]DeclarativeResourceState, len(states))
	for _, state := range states {
		stateByLogicalID[state.LogicalID] = state
	}
	resourcesByLogicalID := make(map[string]string, len(states))
	for _, state := range states {
		resourcesByLogicalID[state.LogicalID] = state.ResourceID
	}
	for index := range result.Plan.Entries {
		entry := &result.Plan.Entries[index]
		if entry.Action == PlanNoop || entry.Status == "blocked" {
			continue
		}
		if entry.Action == PlanDelete {
			state := stateByLogicalID[entry.LogicalID]
			var spec ResourceSpec
			if err := json.Unmarshal(state.SpecJSON, &spec); err != nil {
				return result, fmt.Errorf("decode managed resource %s: %w", entry.LogicalID, err)
			}
			_, operation, applyErr := engine.authority.ApplyDeclarativeResource(principal, spec, state.ParentID, string(entry.Action), requestID, correlationID)
			if applyErr != nil {
				return result, applyErr
			}
			if operation != nil {
				entry.OperationID = operation.ID
				result.Operations = append(result.Operations, operation)
			}
			if err := engine.authority.DeleteDeclarativeState(principal, options.Scope, entry.LogicalID, requestID, correlationID); err != nil {
				return result, err
			}
			entry.Status = "applied"
			continue
		}
		var resourceSpec ResourceSpec
		for _, candidate := range document.Resources {
			if candidate.ID == entry.LogicalID {
				resourceSpec = candidate
				break
			}
		}
		parentID := ""
		if resourceSpec.Parent != "" {
			parentID = resourcesByLogicalID[resourceSpec.Parent]
			if parentID == "" {
				return result, fmt.Errorf("parent %s for %s was not applied", resourceSpec.Parent, resourceSpec.ID)
			}
		}
		resource, operation, applyErr := engine.authority.ApplyDeclarativeResource(principal, resourceSpec, parentID, string(entry.Action), requestID, correlationID)
		if applyErr != nil {
			return result, applyErr
		}
		if resource == nil || operation == nil {
			return result, fmt.Errorf("declarative authority returned incomplete result for %s", entry.LogicalID)
		}
		hash, canonical, hashErr := resourceHash(resourceSpec)
		if hashErr != nil {
			return result, hashErr
		}
		state := DeclarativeResourceState{LogicalID: resourceSpec.ID, ResourceID: resource.ID, APIVersion: DeclarativeAPIVersion, Type: resourceSpec.Type, Scope: resourceSpec.Scope, ParentID: parentID, SpecHash: hash, SpecJSON: canonical, Lifecycle: resourceSpec.Lifecycle}
		if err := engine.authority.SaveDeclarativeState(principal, state, requestID, correlationID); err != nil {
			return result, err
		}
		resourcesByLogicalID[resourceSpec.ID] = resource.ID
		entry.ResourceID = resource.ID
		entry.OperationID = operation.ID
		entry.Status = "applied"
		result.Operations = append(result.Operations, operation)
	}
	return result, nil
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
