package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	cdp "github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
)

// SnapshotManager takes accessibility tree snapshots and maps UIDs to DOM nodes.
type SnapshotManager struct {
	mu          sync.Mutex
	snapshotSeq int
	uidToNode   map[string]cdp.BackendNodeID
}

func NewSnapshotManager() *SnapshotManager {
	return &SnapshotManager{
		uidToNode: make(map[string]cdp.BackendNodeID),
	}
}

// Reset invalidates UIDs after a connection change while keeping the manager
// shared by concurrent handlers. Keep the sequence increasing so stale UIDs
// cannot be reassigned to unrelated nodes after reconnection.
func (sm *SnapshotManager) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.uidToNode = make(map[string]cdp.BackendNodeID)
}

// axTreeResult receives the raw CDP response for Accessibility.getFullAXTree.
// We use jsontext.Value for lenient parsing to avoid failures on unknown enum
// values in newer Chrome versions.
type axTreeResult struct {
	Nodes jsontext.Value `json:"nodes"`
}

// axNode is a lenient representation of an accessibility tree node.
// Uses standard encoding/json so unknown enum values are accepted as strings.
type axNode struct {
	NodeID           string            `json:"nodeId"`
	Ignored          bool              `json:"ignored"`
	Role             *axVal            `json:"role,omitempty"`
	Name             *axVal            `json:"name,omitempty"`
	Value            *axVal            `json:"value,omitempty"`
	Properties       []*axProp         `json:"properties,omitempty"`
	ParentID         string            `json:"parentId,omitempty"`
	ChildIDs         []string          `json:"childIds,omitempty"`
	BackendDOMNodeID cdp.BackendNodeID `json:"backendDOMNodeId,omitempty"`
}

type axVal struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

type axProp struct {
	Name  string `json:"name"`
	Value *axVal `json:"value,omitempty"`
}

// TakeSnapshot captures the page's accessibility tree and returns a text
// representation with UIDs assigned to each interactive or named element.
func (sm *SnapshotManager) TakeSnapshot(ctx context.Context) (string, error) {
	// A reset waits for an in-progress capture; a response from an old
	// connection cannot repopulate the mapping after it was invalidated.
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var raw axTreeResult
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdp.Execute(ctx, "Accessibility.getFullAXTree", nil, &raw)
	})); err != nil {
		return "", fmt.Errorf("get accessibility tree: %w", err)
	}

	var nodes []*axNode
	if err := json.Unmarshal([]byte(raw.Nodes), &nodes); err != nil {
		return "", fmt.Errorf("parse accessibility nodes: %w", err)
	}

	sm.snapshotSeq++
	sm.uidToNode = make(map[string]cdp.BackendNodeID, len(nodes))

	nodeMap := make(map[string]*axNode, len(nodes))
	for _, n := range nodes {
		nodeMap[n.NodeID] = n
	}

	var roots []*axNode
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
		} else if _, ok := nodeMap[n.ParentID]; !ok {
			roots = append(roots, n)
		}
	}
	if len(roots) == 0 && len(nodes) > 0 {
		roots = nodes[:1]
	}

	var sb strings.Builder
	uidCounter := 0
	var walk func(node *axNode, depth int)
	walk = func(node *axNode, depth int) {
		if node.Ignored {
			for _, cid := range node.ChildIDs {
				if child, ok := nodeMap[cid]; ok {
					walk(child, depth)
				}
			}
			return
		}

		role := rawAxVal(node.Role)
		name := rawAxVal(node.Name)

		if role == "generic" && name == "" && len(node.Properties) == 0 {
			for _, cid := range node.ChildIDs {
				if child, ok := nodeMap[cid]; ok {
					walk(child, depth)
				}
			}
			return
		}
		if role == "InlineTextBox" {
			return
		}

		uid := fmt.Sprintf("s%d_%d", sm.snapshotSeq, uidCounter)
		uidCounter++
		if node.BackendDOMNodeID != 0 {
			sm.uidToNode[uid] = node.BackendDOMNodeID
		}

		indent := strings.Repeat("  ", depth)
		sb.WriteString(indent)
		sb.WriteString("uid=")
		sb.WriteString(uid)
		sb.WriteString(" [")
		sb.WriteString(role)
		sb.WriteString("]")
		if name != "" {
			sb.WriteString(` "`)
			sb.WriteString(name)
			sb.WriteString(`"`)
		}

		for _, prop := range node.Properties {
			pv := rawAxVal(prop.Value)
			switch prop.Name {
			case "level", "valuetext", "valuemin", "valuemax", "autocomplete", "orientation":
				sb.WriteString(" ")
				sb.WriteString(prop.Name)
				sb.WriteString("=")
				sb.WriteString(pv)
			case "focused", "required", "disabled", "checked", "selected", "expanded", "multiselectable", "readonly":
				if pv == "true" {
					sb.WriteString(" ")
					sb.WriteString(prop.Name)
				}
			}
		}

		if v := rawAxVal(node.Value); v != "" && role != "StaticText" {
			sb.WriteString(` value="`)
			if len(v) > 80 {
				v = v[:80] + "..."
			}
			sb.WriteString(v)
			sb.WriteString(`"`)
		}

		sb.WriteString("\n")

		for _, cid := range node.ChildIDs {
			if child, ok := nodeMap[cid]; ok {
				walk(child, depth+1)
			}
		}
	}

	for _, root := range roots {
		walk(root, 0)
	}
	return sb.String(), nil
}

// ResolveUID returns the BackendNodeID for a previously assigned UID.
func (sm *SnapshotManager) ResolveUID(uid string) (cdp.BackendNodeID, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	nodeID, ok := sm.uidToNode[uid]
	if !ok {
		return 0, fmt.Errorf("uid %q not found; call take_snapshot first to get fresh UIDs", uid)
	}
	return nodeID, nil
}

// ResolveNodeObject converts a UID to a CDP RemoteObject for JS interaction.
func (sm *SnapshotManager) ResolveNodeObject(ctx context.Context, uid string) (runtime.RemoteObjectID, error) {
	backendID, err := sm.ResolveUID(uid)
	if err != nil {
		return "", err
	}

	var objID runtime.RemoteObjectID
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		obj, err := dom.ResolveNode().WithBackendNodeID(backendID).Do(ctx)
		if err != nil {
			return fmt.Errorf("resolve node for uid %s: %w", uid, err)
		}
		objID = obj.ObjectID
		return nil
	})); err != nil {
		return "", err
	}
	return objID, nil
}

// ElementCenter scrolls the element into view and returns its center coordinates.
func (sm *SnapshotManager) ElementCenter(ctx context.Context, uid string) (x, y float64, err error) {
	objID, err := sm.ResolveNodeObject(ctx, uid)
	if err != nil {
		return 0, 0, err
	}

	type pos struct{ X, Y float64 }
	var p pos
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, _, err := runtime.CallFunctionOn(`function(){
			this.scrollIntoView({block:"center",inline:"center"});
			var r=this.getBoundingClientRect();
			return {x:r.x+r.width/2,y:r.y+r.height/2};
		}`).WithObjectID(objID).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(res.Value), &p)
	})); err != nil {
		return 0, 0, err
	}
	return p.X, p.Y, nil
}

func rawAxVal(v *axVal) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	var f float64
	if json.Unmarshal(v.Value, &f) == nil {
		return fmt.Sprintf("%g", f)
	}
	var b bool
	if json.Unmarshal(v.Value, &b) == nil {
		if b {
			return "true"
		}
		return "false"
	}
	return strings.Trim(string(v.Value), `"`)
}
