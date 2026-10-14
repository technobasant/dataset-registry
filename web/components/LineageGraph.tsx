"use client";

import "@xyflow/react/dist/style.css";

import dagre from "@dagrejs/dagre";
import { Background, Controls, MarkerType, Position, ReactFlow, type Edge, type Node } from "@xyflow/react";
import { useRouter } from "next/navigation";
import { useMemo } from "react";

import type { LineageGraph as Graph } from "@/lib/types";

const W = 220;
const H = 56;

function layout(g: Graph): { nodes: Node[]; edges: Edge[] } {
  const d = new dagre.graphlib.Graph();
  d.setGraph({ rankdir: "LR", nodesep: 24, ranksep: 60 });
  d.setDefaultEdgeLabel(() => ({}));
  for (const n of g.nodes) d.setNode(n.id, { width: W, height: H });
  for (const e of g.edges) d.setEdge(e.from, e.to);
  dagre.layout(d);

  const nodes: Node[] = g.nodes.map((n) => {
    const p = d.node(n.id);
    const root = n.id === g.root;
    const cls = [
      "lg-node",
      `lg-${n.kind}`,
      n.kind === "dataset" ? `lg-${n.registered ? n.slo_status ?? "unknown" : "unowned"}` : "",
      n.kind === "job" && n.last_state === "FAIL" ? "lg-failed" : "",
      root ? "lg-root" : "",
    ].join(" ");
    const sub =
      n.kind === "job"
        ? `job · ${n.last_state?.toLowerCase() ?? "unknown"}`
        : n.registered
          ? `${n.owner_team} · ${n.slo_status?.replace("_", " ")}`
          : "UNOWNED";
    return {
      id: n.id,
      position: { x: p.x - W / 2, y: p.y - H / 2 },
      sourcePosition: Position.Right,
      targetPosition: Position.Left,
      data: {
        label: (
          <div title={`${n.namespace}/${n.name}`}>
            <div className="lg-name">{n.name}</div>
            <div className="lg-sub">{sub}</div>
          </div>
        ),
        datasetId: n.dataset_id,
      },
      className: cls,
      style: { width: W, height: H },
      ariaLabel: `${n.kind} ${n.name}, ${sub}`,
    };
  });
  const edges: Edge[] = g.edges.map((e) => ({
    id: `${e.from}->${e.to}`,
    source: e.from,
    target: e.to,
    markerEnd: { type: MarkerType.ArrowClosed },
  }));
  return { nodes, edges };
}

export function LineageGraph({ graph }: { graph: Graph }) {
  const router = useRouter();
  const { nodes, edges } = useMemo(() => layout(graph), [graph]);
  const datasets = graph.nodes.filter((n) => n.kind === "dataset").length;
  return (
    <div className="lineage" role="group" aria-label={`Lineage graph: ${datasets} datasets, ${graph.nodes.length - datasets} jobs`}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        fitViewOptions={{ padding: 0.05 }}
        minZoom={0.2}
        nodesDraggable={false}
        nodesConnectable={false}
        proOptions={{ hideAttribution: true }}
        onNodeClick={(_, n) => {
          const id = (n.data as { datasetId?: number }).datasetId;
          if (id) router.push(`/datasets/${id}`);
        }}
      >
        <Background gap={24} size={1} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
