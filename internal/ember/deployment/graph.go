package deployment

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	// MaxDependencyGraphNodes keeps graph construction within the deployment
	// document resource bound. A resource has at most one parent dependency.
	MaxDependencyGraphNodes = MaxResourceDeclarations
)

var (
	// ErrInvalidDependencyGraph identifies malformed or unresolvable graph input.
	ErrInvalidDependencyGraph = errors.New("invalid dependency graph")
	// ErrDependencyCycle identifies one or more resource dependency cycles.
	ErrDependencyCycle = errors.New("dependency cycle")
)

// DependencyNode is the safe identity used by a deployment graph. It contains
// no resource tags or other arbitrary field values.
type DependencyNode struct {
	ID   string              `json:"id"`
	Name string              `json:"name"`
	Type models.ResourceType `json:"type"`
}

// DependencyEdge points from a prerequisite resource to the resource that
// depends on it.
type DependencyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// DependencyGraph is a bounded, deterministic graph for a resolved document.
// Nodes and edges are sorted by stable logical ID. Order is a parent-first
// topological order with stable logical-ID tie breaking.
type DependencyGraph struct {
	Nodes []DependencyNode `json:"nodes"`
	Edges []DependencyEdge `json:"edges"`
	Order []string         `json:"order"`
}

// Graph is a short compatibility name for DependencyGraph.
type Graph = DependencyGraph

// DependencyGraphError contains safe, deterministic graph diagnostics. Paths
// identify document locations and never include input IDs, names, or values.
type DependencyGraphError struct {
	Diagnostics []Diagnostic
	CyclePaths  [][]string
	hasCycle    bool
}

func (err *DependencyGraphError) Error() string {
	if err == nil {
		return ErrInvalidDependencyGraph.Error()
	}
	prefix := ErrInvalidDependencyGraph.Error()
	if err.hasCycle {
		prefix = ErrDependencyCycle.Error()
	}
	if len(err.Diagnostics) == 0 {
		return prefix
	}
	parts := make([]string, 0, len(err.Diagnostics))
	for _, diagnostic := range err.Diagnostics {
		parts = append(parts, diagnostic.Path+" ["+diagnostic.Code+"]: "+diagnostic.Message)
	}
	return prefix + ": " + strings.Join(parts, "; ")
}

func (err *DependencyGraphError) Unwrap() error {
	return ErrInvalidDependencyGraph
}

func (err *DependencyGraphError) Is(target error) bool {
	if err == nil {
		return false
	}
	return target == ErrInvalidDependencyGraph || (target == ErrDependencyCycle && err.hasCycle)
}

// BuildGraph is the concise entry point for building a graph from a resolved
// deployment document.
func BuildGraph(document ResolvedDocument) (DependencyGraph, error) {
	return BuildDependencyGraph(document)
}

// BuildDependencyGraph validates a resolved deployment document, constructs its
// parent dependency edges, and returns a stable parent-first order. It never
// returns a partial graph when validation, reference, or cycle checks fail.
func BuildDependencyGraph(document ResolvedDocument) (DependencyGraph, error) {
	diagnostics := make([]Diagnostic, 0)
	if document.Version != CurrentVersion {
		diagnostics = append(diagnostics, Diagnostic{
			Path:    "$.version",
			Code:    "unsupported_version",
			Message: "document version is not supported",
		})
	}
	if len(document.Resources) > MaxDependencyGraphNodes {
		diagnostics = append(diagnostics, Diagnostic{
			Path:    "$.resources",
			Code:    "too_many_items",
			Message: "resources exceeds the configured graph limit",
		})
	}
	if len(diagnostics) > 0 {
		return DependencyGraph{}, &DependencyGraphError{Diagnostics: diagnostics}
	}

	type indexedResource struct {
		node          DependencyNode
		resourceIndex int
		parentID      string
		resourcePath  string
	}
	resources := make([]indexedResource, 0, len(document.Resources))
	byID := make(map[string]int, len(document.Resources))
	byName := make(map[string]int, len(document.Resources))

	for index, resource := range document.Resources {
		path := resourcePath(index)
		if err := resource.Spec.Validate(); err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    path,
				Code:    "invalid_resource",
				Message: "resolved resource is not valid",
			})
			continue
		}

		expectedID := logicalResourceID(resource.Spec.Type, resource.Spec.Name)
		id := resource.ID
		if id == "" {
			id = expectedID
		} else if id != expectedID {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    path + ".id",
				Code:    "unstable_resource_id",
				Message: "resource ID does not match its stable logical identity",
			})
		}

		if previous, exists := byID[id]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    path + ".id",
				Code:    "duplicate_resource_id",
				Message: "resource ID is declared more than once",
			})
			_ = previous
		} else {
			byID[id] = len(resources)
		}
		if previous, exists := byName[resource.Spec.Name]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    path + ".name",
				Code:    "ambiguous_resource_name",
				Message: "resource name is declared more than once",
			})
			_ = previous
		} else {
			byName[resource.Spec.Name] = len(resources)
		}

		resources = append(resources, indexedResource{
			node:          DependencyNode{ID: id, Name: resource.Spec.Name, Type: resource.Spec.Type},
			resourceIndex: index,
			parentID:      resource.Spec.ParentID,
			resourcePath:  path,
		})
	}
	if len(diagnostics) > 0 {
		return DependencyGraph{}, &DependencyGraphError{Diagnostics: diagnostics}
	}

	sort.Slice(resources, func(left, right int) bool {
		if resources[left].node.ID == resources[right].node.ID {
			return resources[left].resourceIndex < resources[right].resourceIndex
		}
		return resources[left].node.ID < resources[right].node.ID
	})

	nodes := make([]DependencyNode, len(resources))
	sortedByID := make(map[string]int, len(resources))
	for index, resource := range resources {
		nodes[index] = resource.node
		sortedByID[resource.node.ID] = index
	}

	parentIndexes := make([]int, len(resources))
	for index := range parentIndexes {
		parentIndexes[index] = -1
	}
	children := make([][]int, len(resources))
	indegree := make([]int, len(resources))
	edges := make([]DependencyEdge, 0, len(resources))
	for childIndex, resource := range resources {
		if resource.parentID == "" {
			continue
		}
		parentIndex, exists := sortedByID[resource.parentID]
		if !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    resource.resourcePath + ".parentId",
				Code:    "missing_dependency",
				Message: "resource parent dependency is not declared",
			})
			continue
		}
		parentIndexes[childIndex] = parentIndex
		children[parentIndex] = append(children[parentIndex], childIndex)
		indegree[childIndex]++
		edges = append(edges, DependencyEdge{From: nodes[parentIndex].ID, To: nodes[childIndex].ID})
	}
	if len(diagnostics) > 0 {
		return DependencyGraph{}, &DependencyGraphError{Diagnostics: diagnostics}
	}

	for parentIndex := range children {
		sort.Slice(children[parentIndex], func(left, right int) bool {
			return nodes[children[parentIndex][left]].ID < nodes[children[parentIndex][right]].ID
		})
	}
	sort.Slice(edges, func(left, right int) bool {
		if edges[left].From == edges[right].From {
			return edges[left].To < edges[right].To
		}
		return edges[left].From < edges[right].From
	})

	cycles := dependencyCycles(nodes, parentIndexes)
	if len(cycles) > 0 {
		cyclePaths := make([][]string, 0, len(cycles))
		cycleDiagnostics := make([]Diagnostic, 0, len(cycles))
		for _, cycle := range cycles {
			paths := make([]string, len(cycle))
			for index, nodeIndex := range cycle {
				paths[index] = resources[nodeIndex].resourcePath
			}
			cyclePaths = append(cyclePaths, append([]string(nil), paths...))
			cycleDiagnostics = append(cycleDiagnostics, Diagnostic{
				Path:    strings.Join(paths, " -> "),
				Code:    "dependency_cycle",
				Message: "resource dependency cycle detected",
			})
		}
		return DependencyGraph{}, &DependencyGraphError{
			Diagnostics: cycleDiagnostics,
			CyclePaths:  cyclePaths,
			hasCycle:    true,
		}
	}

	orderIndexes := stableTopologicalOrder(nodes, children, indegree)
	if len(orderIndexes) != len(nodes) {
		return DependencyGraph{}, &DependencyGraphError{
			Diagnostics: []Diagnostic{{
				Path:    "$.resources",
				Code:    "dependency_cycle",
				Message: "resource dependency cycle detected",
			}},
			hasCycle: true,
		}
	}
	order := make([]string, len(orderIndexes))
	for index, nodeIndex := range orderIndexes {
		order[index] = nodes[nodeIndex].ID
	}
	return DependencyGraph{Nodes: nodes, Edges: edges, Order: order}, nil
}

func resourcePath(index int) string {
	return "$.resources[" + strconv.Itoa(index) + "]"
}

func dependencyCycles(nodes []DependencyNode, parentIndexes []int) [][]int {
	state := make([]uint8, len(nodes))
	positions := make(map[int]int, len(nodes))
	stack := make([]int, 0, len(nodes))
	cycles := make([][]int, 0)

	var visit func(int)
	visit = func(nodeIndex int) {
		state[nodeIndex] = 1
		positions[nodeIndex] = len(stack)
		stack = append(stack, nodeIndex)

		parentIndex := parentIndexes[nodeIndex]
		if parentIndex >= 0 {
			switch state[parentIndex] {
			case 0:
				visit(parentIndex)
			case 1:
				cycle := append([]int(nil), stack[positions[parentIndex]:]...)
				cycles = append(cycles, canonicalCycle(nodes, cycle))
			}
		}

		stack = stack[:len(stack)-1]
		delete(positions, nodeIndex)
		state[nodeIndex] = 2
	}

	for nodeIndex := range nodes {
		if state[nodeIndex] == 0 {
			visit(nodeIndex)
		}
	}
	return cycles
}

func canonicalCycle(nodes []DependencyNode, dependencyPath []int) []int {
	// dependencyPath follows child -> parent. Reverse it so diagnostics follow
	// the public edge direction, prerequisite -> dependent.
	edgePath := make([]int, len(dependencyPath))
	for index := range dependencyPath {
		edgePath[index] = dependencyPath[len(dependencyPath)-1-index]
	}

	start := 0
	for index := 1; index < len(edgePath); index++ {
		if nodes[edgePath[index]].ID < nodes[edgePath[start]].ID {
			start = index
		}
	}
	canonical := make([]int, len(edgePath)+1)
	for index := 0; index < len(edgePath); index++ {
		canonical[index] = edgePath[(start+index)%len(edgePath)]
	}
	canonical[len(edgePath)] = canonical[0]
	return canonical
}

func stableTopologicalOrder(nodes []DependencyNode, children [][]int, indegree []int) []int {
	available := make([]int, 0, len(nodes))
	for index, degree := range indegree {
		if degree == 0 {
			available = append(available, index)
		}
	}
	sort.Slice(available, func(left, right int) bool {
		return nodes[available[left]].ID < nodes[available[right]].ID
	})

	order := make([]int, 0, len(nodes))
	for len(available) > 0 {
		nodeIndex := available[0]
		available = available[1:]
		order = append(order, nodeIndex)
		for _, childIndex := range children[nodeIndex] {
			indegree[childIndex]--
			if indegree[childIndex] == 0 {
				position := sort.Search(len(available), func(position int) bool {
					return nodes[available[position]].ID >= nodes[childIndex].ID
				})
				available = append(available, 0)
				copy(available[position+1:], available[position:])
				available[position] = childIndex
			}
		}
	}
	return order
}
