import Link from "next/link";

import { Flash, one, type SearchParams } from "@/components/Flash";
import { StateBadge } from "@/components/StatusBadge";
import { api, currentPersona } from "@/lib/api";
import { when } from "@/lib/format";
import type { ChangeRequest } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function ChangeRequests({ searchParams }: { searchParams: SearchParams }) {
  const sp = await searchParams;
  const persona = await currentPersona();
  const mine = one(sp.mine) === "1" && persona.team;
  const q = mine ? `?consumer_team=${encodeURIComponent(persona.team)}` : "";
  const { change_requests: crs } = await api<{ change_requests: ChangeRequest[] }>(`/api/v1/change-requests${q}`);
  return (
    <>
      <h1>Change requests</h1>
      <Flash error={one(sp.error)} notice={one(sp.notice)} />
      <p className="lede">A breaking contract change waits here until every consumer registered when it opened has acknowledged it.</p>
      {persona.team ? (
        <nav aria-label="Filter change requests" className="filters">
          <Link href="/change-requests" aria-current={!mine ? "page" : undefined}>
            All
          </Link>
          <Link href="/change-requests?mine=1" aria-current={mine ? "page" : undefined}>
            Waiting on or reviewed by {persona.team}
          </Link>
        </nav>
      ) : null}
      {crs.length === 0 ? (
        <p className="muted">None.</p>
      ) : (
        <table>
          <caption className="sr-only">Change requests, newest first</caption>
          <thead>
            <tr>
              <th scope="col">#</th>
              <th scope="col">Dataset</th>
              <th scope="col">Versions</th>
              <th scope="col">State</th>
              <th scope="col">Breaking changes</th>
              <th scope="col">Waiting on</th>
              <th scope="col">Opened</th>
            </tr>
          </thead>
          <tbody>
            {crs.map((cr) => (
              <tr key={cr.id}>
                <td>
                  <Link href={`/change-requests/${cr.id}`}>#{cr.id}</Link>
                </td>
                <td>
                  <Link href={`/datasets/${cr.dataset_id}`}>{cr.dataset}</Link>
                </td>
                <td>
                  v{cr.from_version} → v{cr.to_version}
                </td>
                <td>
                  <StateBadge state={cr.state} />
                </td>
                <td>{cr.report.breaking_count}</td>
                <td>{cr.state === "open" ? `${cr.pending_reviews ?? 0} consumer(s)` : "—"}</td>
                <td>{when(cr.opened_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
