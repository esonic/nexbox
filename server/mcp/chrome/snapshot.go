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
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
)

// SnapshotManager takes accessibility tree snapshots and maps UIDs to DOM nodes.
type SnapshotManager struct {
	mu           sync.Mutex
	snapshotSeq  int
	uidToNode    map[string]cdp.BackendNodeID
	pageTargetID target.ID
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
	sm.pageTargetID = ""
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
	// Objects are only needed between snapshots; release the previous group.
	_ = chromedp.Run(ctx, runtime.ReleaseObjectGroup("beehub-snapshot"))
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

	sm.pageTargetID = chromedp.FromContext(ctx).Target.TargetID
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
			fmt.Fprintf(&sb, " %q", name)
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
			if runes := []rune(v); len(runes) > 80 {
				v = string(runes[:80]) + "..."
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
	sm.mu.Lock()
	pageTargetID := sm.pageTargetID
	sm.mu.Unlock()
	if pageTargetID != chromedp.FromContext(ctx).Target.TargetID {
		return "", fmt.Errorf("snapshot belongs to another tab; call take_snapshot for fresh UIDs")
	}
	backendID, err := sm.ResolveUID(uid)
	if err != nil {
		return "", err
	}

	var objID runtime.RemoteObjectID
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		obj, err := dom.ResolveNode().WithBackendNodeID(backendID).WithObjectGroup("beehub-snapshot").Do(ctx)
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
func (sm *SnapshotManager) ElementCenter(ctx context.Context, uid string, allowDisabled ...bool) (x, y float64, err error) {
	objID, err := sm.ResolveNodeObject(ctx, uid)
	if err != nil {
		return 0, 0, err
	}

	type pos struct{ X, Y float64 }
	var p pos
	if err := chromedp.Run(ctx, chromedp.CallFunctionOn(toolScriptComment+`function(allowDisabled){
        if(!this.isConnected) throw new Error('Element is detached; take_snapshot again');
        if(!allowDisabled && this.matches(':disabled')) throw new Error('Element is disabled');
        this.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
        const r=this.getBoundingClientRect();
        const win=this.ownerDocument.defaultView;
        const left=Math.max(0,r.left),right=Math.min(win.innerWidth,r.right);
        const top=Math.max(0,r.top),bottom=Math.min(win.innerHeight,r.bottom);
        if(right<=left||bottom<=top) throw new Error('Element has no visible bounds');
        const x=(left+right)/2,y=(top+bottom)/2;
        const hit=this.getRootNode().elementFromPoint(x,y);
        if(!hit || (hit!==this&&!this.contains(hit))) throw new Error('Element is covered by another element');
        if(win!==win.top) throw new Error('Frame coordinates are unsupported for pointer actions; use evaluate_script with element args');
        return {x,y};
    }`, &p, func(p *runtime.CallFunctionOnParams) *runtime.CallFunctionOnParams { return p.WithObjectID(objID) }, len(allowDisabled) > 0 && allowDisabled[0])); err != nil {
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
