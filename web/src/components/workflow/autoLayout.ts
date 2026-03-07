import dagre from 'dagre';
import type { Node, Edge } from 'reactflow';

const NODE_WIDTH_DEFAULT = 200;
const NODE_HEIGHT_DEFAULT = 60;
const DIAMOND_SIZE = 56;

const DIAMOND_TYPES = new Set(['map', 'reduce']);

export function getAutoLayout(
  nodes: Node[],
  edges: Edge[],
  direction: 'TB' | 'LR' = 'TB',
): { nodes: Node[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setDefaultEdgeLabel(() => ({}));
  g.setGraph({ rankdir: direction, nodesep: 60, ranksep: 80, edgesep: 30 });

  nodes.forEach((node) => {
    const isDiamond = DIAMOND_TYPES.has(node.type || '');
    g.setNode(node.id, {
      width: isDiamond ? DIAMOND_SIZE : NODE_WIDTH_DEFAULT,
      height: isDiamond ? DIAMOND_SIZE : NODE_HEIGHT_DEFAULT,
    });
  });

  edges.forEach((edge) => {
    g.setEdge(edge.source, edge.target);
  });

  dagre.layout(g);

  const layoutedNodes = nodes.map((node) => {
    const pos = g.node(node.id);
    const isDiamond = DIAMOND_TYPES.has(node.type || '');
    const w = isDiamond ? DIAMOND_SIZE : NODE_WIDTH_DEFAULT;
    const h = isDiamond ? DIAMOND_SIZE : NODE_HEIGHT_DEFAULT;
    return {
      ...node,
      position: { x: pos.x - w / 2, y: pos.y - h / 2 },
    };
  });

  return { nodes: layoutedNodes, edges };
}
