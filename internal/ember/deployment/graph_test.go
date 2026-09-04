package deployment

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestBuildDependencyGraphReturnsStableParentFirstOrder(t *testing.T) {
	platform := graphResource(models.ResourceTypeGroup, "platform", "")
	logging := graphResource(models.ResourceTypeGroup, "logging", "")
	assets := graphResource(models.ResourceTypeBucket, "assets", platform.ID)
	archive := graphResource(models.ResourceTypeBucket, "archive", logging.ID)
	document := ResolvedDocument{
		Version: CurrentVersion,
		Resources: []ResolvedResource{
			assets,
			platform,
			archive,
			logging,
		},
	}

	graph, err := BuildDependencyGraph(document)
	if err != nil {
		t.Fatalf("BuildDependencyGraph() error = %v", err)
	}

	wantNodes := []DependencyNode{
		{ID: archive.ID, Name: archive.Spec.Name, Type: archive.Spec.Type},
		{ID: assets.ID, Name: assets.Spec.Name, Type: assets.Spec.Type},
		{ID: logging.ID, Name: logging.Spec.Name, Type: logging.Spec.Type},
		{ID: platform.ID, Name: platform.Spec.Name, Type: platform.Spec.Type},
	}
	if !reflect.DeepEqual(graph.Nodes, wantNodes) {
		t.Fatalf("graph nodes = %#v, want %#v", graph.Nodes, wantNodes)
	}

	wantEdges := []DependencyEdge{
		{From: logging.ID, To: archive.ID},
		{From: platform.ID, To: assets.ID},
	}
	if !reflect.DeepEqual(graph.Edges, wantEdges) {
		t.Fatalf("graph edges = %#v, want %#v", graph.Edges, wantEdges)
	}

	wantOrder := []string{logging.ID, archive.ID, platform.ID, assets.ID}
	if !reflect.DeepEqual(graph.Order, wantOrder) {
		t.Fatalf("graph order = %#v, want %#v", graph.Order, wantOrder)
	}

	shuffled := document
	shuffled.Resources = []ResolvedResource{platform, archive, logging, assets}
	shuffledGraph, err := BuildDependencyGraph(shuffled)
	if err != nil {
		t.Fatalf("BuildDependencyGraph(shuffled) error = %v", err)
	}
	if !reflect.DeepEqual(graph.Nodes, shuffledGraph.Nodes) || !reflect.DeepEqual(graph.Edges, shuffledGraph.Edges) || !reflect.DeepEqual(graph.Order, shuffledGraph.Order) {
		t.Fatalf("graph output changed with declaration order: original = %#v, shuffled = %#v", graph, shuffledGraph)
	}
}

func TestBuildDependencyGraphRejectsDirectCycleBeforeReturningGraph(t *testing.T) {
	first := graphResource(models.ResourceTypeGroup, "first", "")
	second := graphResource(models.ResourceTypeGroup, "second", first.ID)
	first.Spec.ParentID = second.ID

	graph, err := BuildDependencyGraph(ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{first, second},
	})
	if err == nil || !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("BuildDependencyGraph() error = %v, want dependency cycle", err)
	}
	if !reflect.DeepEqual(graph, DependencyGraph{}) {
		t.Fatalf("graph = %#v, want no partial graph on cycle", graph)
	}
	if !strings.Contains(err.Error(), "$.resources[0]") || !strings.Contains(err.Error(), "$.resources[1]") {
		t.Fatalf("cycle error = %v, want both resource paths", err)
	}
	if strings.Contains(err.Error(), first.Spec.Name) || strings.Contains(err.Error(), second.Spec.Name) {
		t.Fatalf("cycle error = %v, want path-only diagnostics", err)
	}
}

func TestBuildDependencyGraphRejectsIndirectCycleWithDeterministicMembers(t *testing.T) {
	alpha := graphResource(models.ResourceTypeGroup, "alpha", "")
	beta := graphResource(models.ResourceTypeGroup, "beta", alpha.ID)
	gamma := graphResource(models.ResourceTypeGroup, "gamma", beta.ID)
	alpha.Spec.ParentID = gamma.ID

	_, err := BuildDependencyGraph(ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{gamma, alpha, beta},
	})
	if err == nil || !errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("BuildDependencyGraph() error = %v, want dependency cycle", err)
	}
	graphErr, ok := err.(*DependencyGraphError)
	if !ok {
		t.Fatalf("error type = %T, want *DependencyGraphError", err)
	}
	wantPath := []string{"$.resources[1]", "$.resources[2]", "$.resources[0]", "$.resources[1]"}
	if len(graphErr.CyclePaths) != 1 || !reflect.DeepEqual(graphErr.CyclePaths[0], wantPath) {
		t.Fatalf("cycle paths = %#v, want %#v", graphErr.CyclePaths, [][]string{wantPath})
	}
}

func TestBuildDependencyGraphRejectsMissingDependencyWithoutEchoingInput(t *testing.T) {
	secretID := "/resources/group/runtime-secret-dependency"
	resource := graphResource(models.ResourceTypeBucket, "assets", secretID)

	graph, err := BuildDependencyGraph(ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{resource},
	})
	if err == nil || !errors.Is(err, ErrInvalidDependencyGraph) {
		t.Fatalf("BuildDependencyGraph() error = %v, want invalid dependency graph", err)
	}
	if errors.Is(err, ErrDependencyCycle) {
		t.Fatalf("BuildDependencyGraph() error = %v, want missing dependency rather than cycle", err)
	}
	if !reflect.DeepEqual(graph, DependencyGraph{}) {
		t.Fatalf("graph = %#v, want no partial graph on missing dependency", graph)
	}
	if strings.Contains(err.Error(), secretID) {
		t.Fatalf("missing dependency error = %v, want no input value", err)
	}
	graphErr, ok := err.(*DependencyGraphError)
	if !ok || len(graphErr.Diagnostics) != 1 {
		t.Fatalf("error = %#v, want one graph diagnostic", err)
	}
	if diagnostic := graphErr.Diagnostics[0]; diagnostic.Path != "$.resources[0].parentId" || diagnostic.Code != "missing_dependency" {
		t.Fatalf("diagnostic = %#v, want missing dependency path/code", diagnostic)
	}
}

func TestBuildDependencyGraphRejectsOversizedInputWithoutAllocatingGraph(t *testing.T) {
	resources := make([]ResolvedResource, MaxResourceDeclarations+1)
	for index := range resources {
		resources[index] = graphResource(models.ResourceTypeGroup, "resource-"+string(rune('a'+index%26))+string(rune('0'+index/26)), "")
	}

	graph, err := BuildDependencyGraph(ResolvedDocument{Version: CurrentVersion, Resources: resources})
	if err == nil {
		t.Fatal("BuildDependencyGraph() error = nil, want resource bound diagnostic")
	}
	if !reflect.DeepEqual(graph, DependencyGraph{}) {
		t.Fatalf("graph = %#v, want no partial graph on oversized input", graph)
	}
	if !strings.Contains(err.Error(), "too_many_items") {
		t.Fatalf("error = %v, want bounded resource diagnostic", err)
	}
}

func graphResource(resourceType models.ResourceType, name, parentID string) ResolvedResource {
	spec := models.ResourceSpec{Type: resourceType, Name: name, ParentID: parentID}
	return ResolvedResource{ID: logicalResourceID(resourceType, name), Spec: spec}
}
