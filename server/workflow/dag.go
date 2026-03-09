package workflow

import (
	"fmt"

	"gitlab.okg.com/sre_ai/boxnex/store"
)

// ValidateDAG checks if the workflow definition is a valid DAG (no cycles, connected)
func ValidateDAG(def *store.WorkflowDefinition) error {
	if len(def.Nodes) == 0 {
		return fmt.Errorf("workflow must have at least one node")
	}

	// Build adjacency list
	graph := make(map[string][]string)
	inDegree := make(map[string]int)

	// Initialize all nodes
	for _, node := range def.Nodes {
		graph[node.ID] = []string{}
		inDegree[node.ID] = 0
	}

	// Build edges
	for _, edge := range def.Edges {
		if _, ok := graph[edge.Source]; !ok {
			return fmt.Errorf("edge references non-existent source node: %s", edge.Source)
		}
		if _, ok := graph[edge.Target]; !ok {
			return fmt.Errorf("edge references non-existent target node: %s", edge.Target)
		}

		graph[edge.Source] = append(graph[edge.Source], edge.Target)
		inDegree[edge.Target]++
	}

	// Check for cycles using DFS
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(nodeID string) bool {
		visited[nodeID] = true
		recStack[nodeID] = true

		for _, neighbor := range graph[nodeID] {
			if !visited[neighbor] {
				if hasCycle(neighbor) {
					return true
				}
			} else if recStack[neighbor] {
				return true
			}
		}

		recStack[nodeID] = false
		return false
	}

	for _, node := range def.Nodes {
		if !visited[node.ID] {
			if hasCycle(node.ID) {
				return fmt.Errorf("workflow contains a cycle")
			}
		}
	}

	return nil
}

// TopologicalSort performs topological sort using Kahn's algorithm
func TopologicalSort(def *store.WorkflowDefinition) ([]string, error) {
	// Build adjacency list and calculate in-degrees
	graph := make(map[string][]string)
	inDegree := make(map[string]int)

	// Initialize all nodes
	for _, node := range def.Nodes {
		graph[node.ID] = []string{}
		inDegree[node.ID] = 0
	}

	// Build edges
	for _, edge := range def.Edges {
		graph[edge.Source] = append(graph[edge.Source], edge.Target)
		inDegree[edge.Target]++
	}

	// Find start nodes (in-degree = 0)
	var queue []string
	for nodeID, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, nodeID)
		}
	}

	if len(queue) == 0 {
		return nil, fmt.Errorf("workflow has no starting node (all nodes have incoming edges)")
	}

	// Kahn's algorithm
	var result []string
	for len(queue) > 0 {
		// Dequeue
		current := queue[0]
		queue = queue[1:]
		result = append(result, current)

		// Process neighbors
		for _, neighbor := range graph[current] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	// Check if all nodes were processed (detects cycles)
	if len(result) != len(def.Nodes) {
		return nil, fmt.Errorf("workflow contains a cycle or disconnected nodes")
	}

	return result, nil
}

// FindStartNodes finds all nodes with no incoming edges
func FindStartNodes(def *store.WorkflowDefinition) []string {
	inDegree := make(map[string]int)

	// Initialize all nodes
	for _, node := range def.Nodes {
		inDegree[node.ID] = 0
	}

	// Count incoming edges
	for _, edge := range def.Edges {
		inDegree[edge.Target]++
	}

	// Find nodes with 0 in-degree
	var starts []string
	for nodeID, degree := range inDegree {
		if degree == 0 {
			starts = append(starts, nodeID)
		}
	}

	return starts
}

// FindEndNodes finds all nodes with no outgoing edges
func FindEndNodes(def *store.WorkflowDefinition) []string {
	outDegree := make(map[string]int)

	// Initialize all nodes
	for _, node := range def.Nodes {
		outDegree[node.ID] = 0
	}

	// Count outgoing edges
	for _, edge := range def.Edges {
		outDegree[edge.Source]++
	}

	// Find nodes with 0 out-degree
	var ends []string
	for nodeID, degree := range outDegree {
		if degree == 0 {
			ends = append(ends, nodeID)
		}
	}

	return ends
}
