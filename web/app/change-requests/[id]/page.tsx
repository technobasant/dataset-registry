import Link from "next/link";
import { notFound } from "next/navigation";

import { closeChange, reviewChange } from "@/app/actions";
import { Flash, one, type SearchParams } from "@/components/Flash";
import { ReportTable } from "@/components/ReportTable";
import { SchemaDiff } from "@/components/SchemaDiff";
import { StateBadge } from "@/components/StatusBadge";
import { api, ApiError, currentPersona } from "@/lib/api";
import { when } from "@/lib/format";
import type { ChangeRequest, SchemaVersion } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function ChangeRequestPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: SearchParams }) {
  const { id } = await params;
  const sp = await searchParams;
  let cr: ChangeRequest;
  try {
    cr = await api<ChangeRequest>(`/api/v1/change-requests/${encodeURIComponent(id)}`);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 400)) notFound();
    throw e;
  }
  const [from, to] = await Promise.all([
    api<SchemaVersion>(`/api/v1/datasets/${cr.dataset_id}/schemas/${cr.from_version}`),
    api<SchemaVersion>(`/api/v1/datasets/${cr.dataset_id}/schemas/${cr.to_version}`),
  ]);
  const persona = await currentPersona();
  const reviews = cr.reviews ?? [];
  const mine = reviews.find((r) => r.consumer_team === persona.team);
  const isOwner = persona.role === "admin" || (persona.role === "owner" && persona.team === cr.owner_team);
  const live = cr.state === "open" || cr.state === "approved";

  return (
    <>
      <p className="crumbs">
        <Link href="/change-requests">Change requests</Link> / <Link href={`/datasets/${cr.dataset_id}`}>{cr.dataset}</Link>
      </p>
      <h1>
        Change request #{cr.id} <StateBadge state={cr.state} />
      </h1>
      <Flash error={one(sp.error)} notice={one(sp.notice)} />
      <p className="lede">
        {cr.dataset}: v{cr.from_version} → v{cr.to_version}, opened by {cr.opened_by} at {when(cr.opened_at)}.
      </p>

      <section aria-labelledby="reviews">
        <h2 id="reviews">Consumers</h2>
        {reviews.length === 0 ? (
          <p className="muted">Nobody depended on this dataset when the change was proposed, so nobody had to be asked.</p>
        ) : (
          <table>
            <caption className="sr-only">Consumer reviews</caption>
            <thead>
              <tr>
                <th scope="col">Team</th>
                <th scope="col">Decision</th>
                <th scope="col">Reason</th>
                <th scope="col">By</th>
              </tr>
            </thead>
            <tbody>
              {reviews.map((r) => (
                <tr key={r.consumer_team}>
                  <td>{r.consumer_team}</td>
                  <td>{r.decision ? <StateBadge state={r.decision} /> : <span className="badge state-pending">waiting</span>}</td>
                  <td>{r.reason || "—"}</td>
                  <td>{r.decided_by ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {cr.state === "open" && mine && !mine.decision ? (
          <form action={reviewChange} className="stack review">
            <input type="hidden" name="change_request_id" value={cr.id} />
            <label>
              Reason ({persona.team}); required to reject
              <textarea name="reason" rows={2} />
            </label>
            <div className="buttons">
              <button type="submit" name="decision" value="acknowledged">
                Acknowledge for {persona.team}
              </button>
              <button type="submit" name="decision" value="rejected" className="danger">
                Reject
              </button>
            </div>
          </form>
        ) : null}

        {isOwner && live ? (
          <form action={closeChange} className="buttons">
            <input type="hidden" name="change_request_id" value={cr.id} />
            <button type="submit" name="action" value="publish" disabled={cr.state !== "approved"} aria-describedby="publish-help">
              Publish v{cr.to_version}
            </button>
            <button type="submit" name="action" value="withdraw" className="secondary">
              Withdraw
            </button>
            <span id="publish-help" className="muted small">
              {cr.state === "approved" ? "Every consumer has acknowledged." : `Waiting on ${cr.pending_reviews ?? 0} consumer(s).`}
            </span>
          </form>
        ) : null}
      </section>

      <section aria-labelledby="what">
        <h2 id="what">What changes</h2>
        <ReportTable report={cr.report} />
        <SchemaDiff from={from.schema} to={to.schema} fromLabel={`v${cr.from_version}`} toLabel={`v${cr.to_version}`} />
      </section>
    </>
  );
}
