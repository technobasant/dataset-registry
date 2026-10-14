export type SLOStatus = "unknown" | "healthy" | "at_risk" | "breached";

export interface Owner {
  team: string;
  person: string;
}

export interface Dataset {
  id: number;
  namespace: string;
  name: string;
  description: string;
  registered: boolean;
  owner: Owner | null;
  tier: "gold" | "silver" | "bronze";
  freshness_slo_seconds: number | null;
  compatibility: "BACKWARD" | "FORWARD" | "FULL" | "NONE";
  last_write_at: string | null;
  slo_status: SLOStatus;
  slo_status_since: string | null;
  created_at: string;
  consumer_count?: number;
  current_version?: number;
  open_change_request_id?: number;
}

export interface SchemaVersion {
  version: number;
  status: "published" | "pending" | "rejected" | "withdrawn";
  schema: unknown;
  created_by: string;
  created_at: string;
}

export interface Consumer {
  team: string;
  contact: string;
  purpose: string;
  created_at: string;
}

export interface Change {
  path: string;
  kind: string;
  detail: string;
  breaks_backward: boolean;
  breaks_forward: boolean;
  breaking: boolean;
  reason: string;
}

export interface Report {
  mode: string;
  compatible: boolean;
  breaking_count: number;
  changes: Change[];
}

export interface Review {
  consumer_team: string;
  decision: "acknowledged" | "rejected" | null;
  reason: string;
  decided_by: string | null;
  decided_at: string | null;
}

export interface ChangeRequest {
  id: number;
  dataset_id: number;
  dataset: string;
  owner_team: string;
  from_version: number;
  to_version: number;
  state: "open" | "approved" | "published" | "rejected" | "withdrawn";
  report: Report;
  opened_by: string;
  opened_at: string;
  closed_at: string | null;
  pending_reviews?: number;
  reviews?: Review[];
}

export interface SLOTransition {
  from: SLOStatus;
  to: SLOStatus;
  staleness_ms: number;
  deadline_at: string | null;
  detected_at: string;
}

export interface DatasetDetail extends Omit<Dataset, "open_change_request_id"> {
  consumers: Consumer[];
  versions: SchemaVersion[];
  open_change_request: ChangeRequest | null;
  slo_history: SLOTransition[];
}

export interface LineageNode {
  id: string;
  kind: "dataset" | "job";
  dataset_id?: number;
  namespace: string;
  name: string;
  registered: boolean;
  owner_team?: string;
  slo_status?: SLOStatus;
  last_state?: string;
  depth: number;
}

export interface LineageGraph {
  root: string;
  direction: string;
  depth: number;
  nodes: LineageNode[];
  edges: { from: string; to: string }[];
}

export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  entity_type: string;
  entity_id: number;
  details: unknown;
}
