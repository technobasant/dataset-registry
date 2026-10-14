import type { SLOStatus } from "./types";

export function duration(seconds: number): string {
  if (seconds < 90) return `${Math.round(seconds)} s`;
  const m = seconds / 60;
  if (m < 90) return `${Math.round(m)} min`;
  const h = m / 60;
  if (h < 48) return `${Math.round(h * 10) / 10} h`;
  return `${Math.round(h / 24)} d`;
}

export function ago(iso: string | null, now: number = Date.now()): string {
  if (!iso) return "never";
  return `${duration((now - new Date(iso).getTime()) / 1000)} ago`;
}

export function when(iso: string | null): string {
  if (!iso) return "—";
  return new Date(iso).toISOString().replace("T", " ").slice(0, 19) + " UTC";
}

export const statusLabel: Record<SLOStatus, string> = {
  healthy: "Healthy",
  at_risk: "At risk",
  breached: "Breached",
  unknown: "No data",
};
