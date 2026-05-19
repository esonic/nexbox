package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// GraphQL Work Items client (hierarchy support)
// ---------------------------------------------------------------------------

// WorkItemChild is a child item from hierarchyWidget.
type WorkItemChild struct {
	ID    string // GraphQL global ID
	IID   string
	Title string
	State string // "OPEN" or "CLOSED"
	Type  string // "Issue", "Task", etc.
}

// WorkItemInfo holds basic info about a work item.
type WorkItemInfo struct {
	GID      string // "gid://gitlab/WorkItem/6582"
	TypeName string // "Issue", "Task", etc.
}

// CreateChildTaskResult holds the result of creating a child task.
type CreateChildTaskResult struct {
	GID   string
	IID   int
	Title string
}

// WorkItemListEntry represents a work item from the list query with hierarchy info.
type WorkItemListEntry struct {
	IID       string
	Title     string
	State     string // "OPEN", "CLOSED"
	TypeName  string // "Issue", "Task", etc.
	CreatedAt time.Time
	UpdatedAt time.Time
	Weight    int // from WorkItemWidgetWeight
	Labels    []string
	// Parent info (from hierarchy widget)
	ParentIID string
	// Children info
	Children []WorkItemChild
}

// graphqlRequest sends a GraphQL query to GitLab.
func (c *Client) graphqlRequest(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	body, err := c.apiRequest(ctx, "POST", baseURL+"/api/graphql", payload)
	if err != nil {
		return nil, fmt.Errorf("graphql: %w", err)
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("graphql decode: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", resp.Errors[0].Message)
	}
	return resp.Data, nil
}

// ---------------------------------------------------------------------------
// GetWorkItemInfo — uses GraphQL to get GID and actual type
// ---------------------------------------------------------------------------

const queryWorkItemInfo = `
query($projectPath: ID!, $iid: String!) {
  project(fullPath: $projectPath) {
    workItems(iids: [$iid]) {
      nodes {
        id
        workItemType { name }
      }
    }
  }
}
`

// GetWorkItemInfo returns the GraphQL ID and type of a work item by IID.
func (c *Client) GetWorkItemInfo(ctx context.Context, projectPath string, iid int) (*WorkItemInfo, error) {
	data, err := c.graphqlRequest(ctx, queryWorkItemInfo, map[string]any{
		"projectPath": projectPath,
		"iid":         strconv.Itoa(iid),
	})
	if err != nil {
		// Fallback: use REST to construct GID (type unknown)
		issue, restErr := c.GetIssue(ctx, projectPath, iid)
		if restErr != nil {
			return nil, fmt.Errorf("get work item info: %w", err)
		}
		return &WorkItemInfo{
			GID:      fmt.Sprintf("gid://gitlab/WorkItem/%d", issue.ID),
			TypeName: "Issue",
		}, nil
	}

	var result struct {
		Project struct {
			WorkItems struct {
				Nodes []struct {
					ID   string `json:"id"`
					Type struct {
						Name string `json:"name"`
					} `json:"workItemType"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode GetWorkItemInfo: %w", err)
	}
	if len(result.Project.WorkItems.Nodes) == 0 {
		return nil, fmt.Errorf("work item #%d not found in %s", iid, projectPath)
	}
	node := result.Project.WorkItems.Nodes[0]
	return &WorkItemInfo{
		GID:      node.ID,
		TypeName: node.Type.Name,
	}, nil
}

// ---------------------------------------------------------------------------
// List all work items (Issues + Tasks) with hierarchy — single GraphQL call
// ---------------------------------------------------------------------------

const queryListWorkItems = `
query($projectPath: ID!, $first: Int!, $after: String, $search: String) {
  project(fullPath: $projectPath) {
    workItems(first: $first, after: $after, search: $search, sort: UPDATED_DESC) {
      nodes {
        iid
        title
        state
        createdAt
        updatedAt
        workItemType { name }
        widgets {
          ... on WorkItemWidgetHierarchy {
            parent {
              iid
            }
            children(first: 50) {
              nodes {
                id
                iid
                title
                state
                workItemType { name }
              }
            }
          }
          ... on WorkItemWidgetWeight {
            weight
          }
          ... on WorkItemWidgetLabels {
            labels { nodes { title } }
          }
        }
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}
`

// ListWorkItems fetches all work items (Issues + Tasks) with hierarchy info in one call.
func (c *Client) ListWorkItems(ctx context.Context, projectPath string, limit int, offset int, search string) ([]WorkItemListEntry, error) {
	if limit <= 0 {
		limit = 30
	}

	vars := map[string]any{
		"projectPath": projectPath,
		"first":       limit,
	}
	if search != "" {
		vars["search"] = search
	}
	// Simple cursor-based pagination: skip pages
	if offset > 0 {
		// For cursor pagination, we'd need to paginate through pages.
		// For now, fetch a larger first page to cover the offset.
		vars["first"] = limit + offset
	}

	data, err := c.graphqlRequest(ctx, queryListWorkItems, vars)
	if err != nil {
		return nil, err
	}

	var result struct {
		Project struct {
			WorkItems struct {
				Nodes []struct {
					IID       string    `json:"iid"`
					Title     string    `json:"title"`
					State     string    `json:"state"`
					CreatedAt time.Time `json:"createdAt"`
					UpdatedAt time.Time `json:"updatedAt"`
					Type      struct {
						Name string `json:"name"`
					} `json:"workItemType"`
					Widgets []json.RawMessage `json:"widgets"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode ListWorkItems: %w", err)
	}

	entries := make([]WorkItemListEntry, 0, len(result.Project.WorkItems.Nodes))
	for _, node := range result.Project.WorkItems.Nodes {
		entry := WorkItemListEntry{
			IID:       node.IID,
			Title:     node.Title,
			State:     node.State,
			TypeName:  node.Type.Name,
			CreatedAt: node.CreatedAt,
			UpdatedAt: node.UpdatedAt,
		}

		// Parse widgets: hierarchy, weight, labels
		for _, w := range node.Widgets {
			parentIID, children := parseHierarchyWidget(w)
			if parentIID != "" {
				entry.ParentIID = parentIID
			}
			entry.Children = append(entry.Children, children...)

			// Try weight widget
			var weightWidget struct {
				Weight *int `json:"weight"`
			}
			if err := json.Unmarshal(w, &weightWidget); err == nil && weightWidget.Weight != nil {
				entry.Weight = *weightWidget.Weight
			}

			// Try labels widget
			var labelsWidget struct {
				Labels *struct {
					Nodes []struct {
						Title string `json:"title"`
					} `json:"nodes"`
				} `json:"labels"`
			}
			if err := json.Unmarshal(w, &labelsWidget); err == nil && labelsWidget.Labels != nil {
				for _, l := range labelsWidget.Labels.Nodes {
					entry.Labels = append(entry.Labels, l.Title)
				}
			}
		}

		entries = append(entries, entry)
	}

	// If we over-fetched for offset, trim
	if offset > 0 && offset < len(entries) {
		entries = entries[offset:]
	} else if offset >= len(entries) {
		return nil, nil
	}
	if len(entries) > limit {
		entries = entries[:limit]
	}

	return entries, nil
}

// WorkItemHierarchy holds parent and children info for a work item.
type WorkItemHierarchy struct {
	TypeName  string // "Issue", "Task", etc.
	ParentIID string
	Children  []WorkItemChild
}

// parseHierarchyWidget extracts parent and children from a raw hierarchy widget JSON.
func parseHierarchyWidget(w json.RawMessage) (parentIID string, children []WorkItemChild) {
	var hierarchy struct {
		Parent *struct {
			IID string `json:"iid"`
		} `json:"parent"`
		Children *struct {
			Nodes []struct {
				ID    string `json:"id"`
				IID   string `json:"iid"`
				Title string `json:"title"`
				State string `json:"state"`
				Type  struct {
					Name string `json:"name"`
				} `json:"workItemType"`
			} `json:"nodes"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w, &hierarchy); err != nil {
		return "", nil
	}
	if hierarchy.Parent != nil {
		parentIID = hierarchy.Parent.IID
	}
	if hierarchy.Children != nil {
		for _, n := range hierarchy.Children.Nodes {
			children = append(children, WorkItemChild{
				ID:    n.ID,
				IID:   n.IID,
				Title: n.Title,
				State: n.State,
				Type:  n.Type.Name,
			})
		}
	}
	return parentIID, children
}

// ---------------------------------------------------------------------------
// Fetch hierarchy (parent + children) of a work item
// ---------------------------------------------------------------------------

const queryWorkItemHierarchy = `
query($projectPath: ID!, $iid: String!) {
  project(fullPath: $projectPath) {
    workItems(iids: [$iid]) {
      nodes {
        id
        workItemType { name }
        widgets {
          ... on WorkItemWidgetHierarchy {
            parent {
              iid
            }
            children(first: 50) {
              nodes {
                id
                iid
                title
                state
                workItemType { name }
              }
            }
          }
        }
      }
    }
  }
}
`

// FetchWorkItemHierarchy returns parent and children of a given work item IID.
func (c *Client) FetchWorkItemHierarchy(ctx context.Context, projectPath string, iid int) (*WorkItemHierarchy, error) {
	data, err := c.graphqlRequest(ctx, queryWorkItemHierarchy, map[string]any{
		"projectPath": projectPath,
		"iid":         strconv.Itoa(iid),
	})
	if err != nil {
		// Fallback: try REST for children only (no parent info available via REST)
		children, restErr := c.fetchChildItemsREST(ctx, projectPath, iid)
		if restErr != nil {
			return nil, err
		}
		return &WorkItemHierarchy{Children: children}, nil
	}

	var result struct {
		Project struct {
			WorkItems struct {
				Nodes []struct {
					Type struct {
						Name string `json:"name"`
					} `json:"workItemType"`
					Widgets []json.RawMessage `json:"widgets"`
				} `json:"nodes"`
			} `json:"workItems"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode FetchWorkItemHierarchy: %w", err)
	}
	if len(result.Project.WorkItems.Nodes) == 0 {
		return &WorkItemHierarchy{}, nil
	}

	h := &WorkItemHierarchy{
		TypeName: result.Project.WorkItems.Nodes[0].Type.Name,
	}
	for _, node := range result.Project.WorkItems.Nodes {
		for _, w := range node.Widgets {
			parentIID, children := parseHierarchyWidget(w)
			if parentIID != "" {
				h.ParentIID = parentIID
			}
			h.Children = append(h.Children, children...)
		}
	}
	return h, nil
}

// FetchChildItems returns the child work items of a given issue IID.
func (c *Client) FetchChildItems(ctx context.Context, projectPath string, iid int) ([]WorkItemChild, error) {
	h, err := c.FetchWorkItemHierarchy(ctx, projectPath, iid)
	if err != nil {
		return nil, err
	}
	return h.Children, nil
}

// fetchChildItemsREST is a fallback using REST issue links.
func (c *Client) fetchChildItemsREST(ctx context.Context, projectPath string, iid int) ([]WorkItemChild, error) {
	encoded := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/issues/%d/links", baseURL, encoded, iid)

	body, err := c.apiRequest(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, nil
	}

	var links []struct {
		ID    int    `json:"id"`
		IID   int    `json:"iid"`
		Title string `json:"title"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &links); err != nil {
		return nil, nil
	}

	children := make([]WorkItemChild, 0, len(links))
	for _, l := range links {
		children = append(children, WorkItemChild{
			ID:    fmt.Sprintf("gid://gitlab/WorkItem/%d", l.ID),
			IID:   strconv.Itoa(l.IID),
			Title: l.Title,
			State: l.State,
			Type:  "Issue",
		})
	}
	return children, nil
}

// ---------------------------------------------------------------------------
// Fetch child count
// ---------------------------------------------------------------------------

// FetchChildCount returns the number of child items for a work item.
func (c *Client) FetchChildCount(ctx context.Context, projectPath string, iid int) (int, error) {
	children, err := c.FetchChildItems(ctx, projectPath, iid)
	if err != nil {
		return 0, err
	}
	return len(children), nil
}

// ---------------------------------------------------------------------------
// Create a Task-type work item as child of an Issue
// ---------------------------------------------------------------------------

const mutationCreateChildTask = `
mutation($projectPath: ID!, $title: String!, $typeId: WorkItemsTypeID!, $parentId: WorkItemID, $description: String!) {
  workItemCreate(input: {
    projectPath: $projectPath,
    title: $title,
    workItemTypeId: $typeId,
    hierarchyWidget: { parentId: $parentId },
    descriptionWidget: { description: $description }
  }) {
    workItem {
      id
      iid
      title
    }
    errors
  }
}
`

// CreateChildTask creates a Task-type work item as a child of parentGID.
func (c *Client) CreateChildTask(ctx context.Context, projectPath, title, description, parentGID, taskTypeID string) (*CreateChildTaskResult, error) {
	vars := map[string]any{
		"projectPath": projectPath,
		"title":       title,
		"typeId":      taskTypeID,
		"parentId":    parentGID,
		"description": description,
	}

	data, err := c.graphqlRequest(ctx, mutationCreateChildTask, vars)
	if err != nil {
		return nil, err
	}

	var result struct {
		WorkItemCreate struct {
			WorkItem *struct {
				ID    string `json:"id"`
				IID   string `json:"iid"`
				Title string `json:"title"`
			} `json:"workItem"`
			Errors []string `json:"errors"`
		} `json:"workItemCreate"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode CreateChildTask: %w", err)
	}
	if len(result.WorkItemCreate.Errors) > 0 {
		return nil, fmt.Errorf("CreateChildTask: %s", result.WorkItemCreate.Errors[0])
	}
	if result.WorkItemCreate.WorkItem == nil {
		return nil, fmt.Errorf("CreateChildTask: no work item returned")
	}

	iid, _ := strconv.Atoi(result.WorkItemCreate.WorkItem.IID)
	return &CreateChildTaskResult{
		GID:   result.WorkItemCreate.WorkItem.ID,
		IID:   iid,
		Title: result.WorkItemCreate.WorkItem.Title,
	}, nil
}

// ---------------------------------------------------------------------------
// Set parent on an existing work item
// ---------------------------------------------------------------------------

const mutationSetParent = `
mutation($id: WorkItemID!, $parentId: WorkItemID!) {
  workItemUpdate(input: {
    id: $id,
    hierarchyWidget: { parentId: $parentId }
  }) {
    workItem { id }
    errors
  }
}
`

// decodeWorkItemUpdateErrors extracts errors from a workItemUpdate GraphQL response.
func decodeWorkItemUpdateErrors(data json.RawMessage, caller string) error {
	var result struct {
		WorkItemUpdate struct {
			Errors []string `json:"errors"`
		} `json:"workItemUpdate"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode %s: %w", caller, err)
	}
	if len(result.WorkItemUpdate.Errors) > 0 {
		return fmt.Errorf("%s: %s", caller, result.WorkItemUpdate.Errors[0])
	}
	return nil
}

// SetWorkItemParent sets the parent of a work item.
func (c *Client) SetWorkItemParent(ctx context.Context, childGID, parentGID string) error {
	data, err := c.graphqlRequest(ctx, mutationSetParent, map[string]any{
		"id":       childGID,
		"parentId": parentGID,
	})
	if err != nil {
		return err
	}
	return decodeWorkItemUpdateErrors(data, "SetWorkItemParent")
}

// ---------------------------------------------------------------------------
// Remove parent from a work item
// ---------------------------------------------------------------------------

const mutationRemoveParent = `
mutation($id: WorkItemID!) {
  workItemUpdate(input: {
    id: $id,
    hierarchyWidget: { parentId: null }
  }) {
    workItem { id }
    errors
  }
}
`

// RemoveWorkItemParent removes the parent of a work item.
func (c *Client) RemoveWorkItemParent(ctx context.Context, childGID string) error {
	data, err := c.graphqlRequest(ctx, mutationRemoveParent, map[string]any{
		"id": childGID,
	})
	if err != nil {
		return err
	}
	return decodeWorkItemUpdateErrors(data, "RemoveWorkItemParent")
}

// ---------------------------------------------------------------------------
// Resolve work item type
// ---------------------------------------------------------------------------

const queryWorkItemTypes = `
query($projectPath: ID!) {
  project(fullPath: $projectPath) {
    workItemTypes { nodes { id name } }
  }
}
`

// GetTaskTypeID returns the GraphQL ID for the "Task" work item type.
func (c *Client) GetTaskTypeID(ctx context.Context, projectPath string) (string, error) {
	data, err := c.graphqlRequest(ctx, queryWorkItemTypes, map[string]any{
		"projectPath": projectPath,
	})
	if err != nil {
		return "", err
	}

	var result struct {
		Project struct {
			WorkItemTypes struct {
				Nodes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"workItemTypes"`
		} `json:"project"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("decode GetTaskTypeID: %w", err)
	}
	for _, t := range result.Project.WorkItemTypes.Nodes {
		if t.Name == "Task" {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("Task work item type not found in project %s", projectPath)
}

// ---------------------------------------------------------------------------
// Update weight on a work item
// ---------------------------------------------------------------------------

const mutationUpdateWorkItemWeight = `
mutation($id: WorkItemID!, $weight: Int!) {
  workItemUpdate(input: {
    id: $id,
    weightWidget: { weight: $weight }
  }) {
    workItem { id }
    errors
  }
}
`

// UpdateWorkItemWeight sets the weight on a work item via GraphQL.
func (c *Client) UpdateWorkItemWeight(ctx context.Context, workItemGID string, weight int) error {
	data, err := c.graphqlRequest(ctx, mutationUpdateWorkItemWeight, map[string]any{
		"id":     workItemGID,
		"weight": weight,
	})
	if err != nil {
		return err
	}
	return decodeWorkItemUpdateErrors(data, "UpdateWorkItemWeight")
}
