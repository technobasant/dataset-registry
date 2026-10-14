import { statusLabel } from "@/lib/format";
import type { SLOStatus } from "@/lib/types";

const icon: Record<SLOStatus, string> = { healthy: "●", at_risk: "▲", breached: "■", unknown: "○" };

export function StatusBadge({ status, slo }: { status: SLOStatus; slo?: number | null }) {
  if (slo === null) {
    return <span className="badge badge-none">No SLO</span>;
  }
  return (
    <span className={`badge badge-${status}`}>
      <span aria-hidden="true">{icon[status]} </span>
      {statusLabel[status]}
    </span>
  );
}

export function StateBadge({ state }: { state: string }) {
  return <span className={`badge state-${state}`}>{state}</span>;
}
