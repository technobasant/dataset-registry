import Link from "next/link";
import { notFound } from "next/navigation";

import { proposeSchema, registerConsumer, unregisterConsumer } from "@/app/actions";
import { Flash, one, type SearchParams } from "@/components/Flash";
import { LineageGraph } from "@/components/LineageGraph";
import { ReportTable } from "@/components/ReportTable";
import { SchemaDiff } from "@/components/SchemaDiff";
import { StateBadge, StatusBadge } from "@/components/StatusBadge";
import { api, ApiError, currentPersona } from "@/lib/api";
import { ago, duration, when } from "@/lib/format";
import type { DatasetDetail, LineageGraph as Graph, Report } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function DatasetPage({ params, searchParams }: { params: Promise<{ id: string }>; searchParams: SearchParams }) {
  const { id } = await params;
  const sp = await searchParams;
  let d: DatasetDetail;
  try {
    d = await api<DatasetDetail>(`/api/v1/datasets/${encodeURIComponent(id)}`);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 400)) notFound();
    throw e;
  }
  const persona = await currentPersona();
  const depth = Math.min(Math.max(Number(one(sp.depth) ?? 2) || 2, 1), 5);
  const graph = await api<Graph>(`/api/v1/datasets/${d.id}/lineage?direction=both&depth=${depth}`);

  const versions = [...d.versions].sort((a, b) => a.version - b.version);
  const latest = versions.at(-1);
  const toV = Number(one(sp.to) ?? latest?.version ?? 0);
  const fromV = Number(one(sp.from) ?? (toV > 1 ? toV - 1 : 0));
  const fromSchema = versions.find((v) => v.version === fromV);
  const toSchema = versions.find((v) => v.version === toV);
  let report: Report | null = null;
  if (fromSchema && toSchema && fromV !== toV) {
    report = await api<Report>(`/api/v1/datasets/${d.id}/compatibility?from=${fromV}&to=${toV}`);
  }
  const published = versions.filter((v) => v.status === "published").at(-1);
  const isOwner = persona.role === "admin" || (persona.role === "owner" && persona.team === d.owner?.team);
  const isConsumer = d.consumers.some((c) => c.team === persona.team);

  return (
    <>
      <p className="crumbs">
        <Link href="/">Datasets</Link> / {d.namespace}
      </p>
      <h1>{d.name}</h1>
      <Flash error={one(sp.error)} notice={one(sp.notice)} />
      {d.description ? <p className="lede">{d.description}</p> : null}

      {!d.registered ? (
        <p className="flash flash-error" role="alert">
          Nobody owns this dataset. Pipelines write it, but nobody can depend on it until a team registers it.
        </p>
      ) : null}

      <dl className="facts">
        <div>
          <dt>Owner</dt>
          <dd>{d.owner ? `${d.owner.team} — ${d.owner.person}` : "nobody"}</dd>
        </div>
        <div>
          <dt>Tier</dt>
          <dd>{d.tier}</dd>
        </div>
        <div>
          <dt>Freshness</dt>
          <dd>
            <StatusBadge status={d.slo_status} slo={d.freshness_slo_seconds} />
            {d.freshness_slo_seconds ? <span className="muted"> within {duration(d.freshness_slo_seconds)}</span> : null}
          </dd>
        </div>
        <div>
          <dt>Last successful write</dt>
          <dd>{ago(d.last_write_at)}</dd>
        </div>
        <div>
          <dt>Contract</dt>
          <dd>
            {published ? `v${published.version}` : "none"} · {d.compatibility}
          </dd>
        </div>
        <div>
          <dt>Consumers</dt>
          <dd>{d.consumers.length}</dd>
        </div>
      </dl>

      {d.open_change_request ? (
        <section className="callout" aria-labelledby="open-cr">
          <h2 id="open-cr">
            Change request #{d.open_change_request.id} <StateBadge state={d.open_change_request.state} />
          </h2>
          <p>
            v{d.open_change_request.from_version} → v{d.open_change_request.to_version}: {d.open_change_request.report.breaking_count} breaking
            change(s); waiting on {d.open_change_request.pending_reviews ?? 0} consumer(s).{" "}
            <Link href={`/change-requests/${d.open_change_request.id}`}>Review it</Link>
          </p>
        </section>
      ) : null}

      <section aria-labelledby="lineage">
        <h2 id="lineage">Lineage</h2>
        <nav aria-label="Lineage depth" className="filters">
          {[1, 2, 3, 4, 5].map((n) => (
            <Link key={n} href={`/datasets/${d.id}?depth=${n}`} aria-current={n === depth ? "page" : undefined} scroll={false}>
              {n} hop{n > 1 ? "s" : ""}
            </Link>
          ))}
        </nav>
        {graph.nodes.length > 1 ? <LineageGraph graph={graph} /> : <p className="muted">No lineage recorded yet.</p>}
        <details>
          <summary>Lineage as a list</summary>
          <ul>
            {graph.nodes.map((n) => (
              <li key={n.id}>
                {n.depth < 0 ? "upstream" : n.depth > 0 ? "downstream" : "this dataset"} ({Math.ceil(Math.abs(n.depth) / 2)} hop
                {Math.abs(n.depth) > 2 ? "s" : ""}): {n.kind} {n.name}
                {n.kind === "dataset" ? ` — ${n.registered ? `owned by ${n.owner_team}, ${n.slo_status}` : "unowned"}` : ` — ${n.last_state ?? ""}`}
              </li>
            ))}
          </ul>
        </details>
      </section>

      <section aria-labelledby="consumers">
        <h2 id="consumers">Consumers</h2>
        {d.consumers.length === 0 ? (
          <p className="muted">Nobody has registered a dependency. A breaking change would publish without asking anyone.</p>
        ) : (
          <table>
            <caption className="sr-only">Registered consumers</caption>
            <thead>
              <tr>
                <th scope="col">Team</th>
                <th scope="col">Contact</th>
                <th scope="col">Purpose</th>
                <th scope="col">Since</th>
              </tr>
            </thead>
            <tbody>
              {d.consumers.map((c) => (
                <tr key={c.team}>
                  <td>{c.team}</td>
                  <td>{c.contact}</td>
                  <td>{c.purpose}</td>
                  <td>{when(c.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {persona.subject && d.registered && !isConsumer && persona.role !== "admin" ? (
          <form action={registerConsumer} className="inline">
            <input type="hidden" name="dataset_id" value={d.id} />
            <label>
              Contact for {persona.team}
              <input name="contact" required defaultValue={`${persona.subject}@lumenloom.example`} />
            </label>
            <label>
              Purpose
              <input name="purpose" placeholder="what breaks if this changes" />
            </label>
            <button type="submit">Register {persona.team} as a consumer</button>
          </form>
        ) : null}
        {isConsumer ? (
          <form action={unregisterConsumer} className="inline">
            <input type="hidden" name="dataset_id" value={d.id} />
            <input type="hidden" name="team" value={persona.team} />
            <button type="submit" className="secondary">
              Unregister {persona.team}
            </button>
          </form>
        ) : null}
      </section>

      <section aria-labelledby="contract">
        <h2 id="contract">Contract versions</h2>
        {versions.length === 0 ? (
          <p className="muted">No contract yet.</p>
        ) : (
          <>
            <table>
              <caption className="sr-only">Schema versions</caption>
              <thead>
                <tr>
                  <th scope="col">Version</th>
                  <th scope="col">Status</th>
                  <th scope="col">By</th>
                  <th scope="col">At</th>
                  <th scope="col">Compare</th>
                </tr>
              </thead>
              <tbody>
                {[...versions].reverse().map((v) => (
                  <tr key={v.version}>
                    <td>v{v.version}</td>
                    <td>
                      <StateBadge state={v.status} />
                    </td>
                    <td>{v.created_by}</td>
                    <td>{when(v.created_at)}</td>
                    <td>
                      {v.version > 1 ? (
                        <Link href={`/datasets/${d.id}?from=${v.version - 1}&to=${v.version}#diff`}>
                          v{v.version - 1} → v{v.version}
                        </Link>
                      ) : (
                        "—"
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {fromSchema && toSchema && fromV !== toV ? (
              <div id="diff">
                <h3>
                  v{fromV} → v{toV}
                </h3>
                <SchemaDiff from={fromSchema.schema} to={toSchema.schema} fromLabel={`v${fromV}`} toLabel={`v${toV}`} />
                {report ? <ReportTable report={report} /> : null}
              </div>
            ) : latest ? (
              <details>
                <summary>v{latest.version} schema</summary>
                <pre>{JSON.stringify(latest.schema, null, 2)}</pre>
              </details>
            ) : null}
          </>
        )}
        {isOwner && d.registered ? (
          <form action={proposeSchema} className="stack">
            <input type="hidden" name="dataset_id" value={d.id} />
            <label>
              Propose a new version
              <textarea
                name="schema"
                rows={12}
                spellCheck={false}
                defaultValue={JSON.stringify(published?.schema ?? { type: "object", properties: {} }, null, 2)}
              />
            </label>
            <p className="muted small">
              Compatible changes under {d.compatibility} publish immediately. Breaking ones open a change request that every registered consumer
              must acknowledge.
            </p>
            <button type="submit">Propose</button>
          </form>
        ) : null}
      </section>

      <section aria-labelledby="slo-history">
        <h2 id="slo-history">Freshness history</h2>
        {d.slo_history.length === 0 ? (
          <p className="muted">No status changes yet.</p>
        ) : (
          <table>
            <caption className="sr-only">Freshness status transitions, newest first</caption>
            <thead>
              <tr>
                <th scope="col">Detected</th>
                <th scope="col">Change</th>
                <th scope="col">Staleness</th>
                <th scope="col">Detected after the deadline</th>
              </tr>
            </thead>
            <tbody>
              {d.slo_history.map((t, i) => (
                <tr key={i}>
                  <td>{when(t.detected_at)}</td>
                  <td>
                    <StatusBadge status={t.from} /> → <StatusBadge status={t.to} />
                  </td>
                  <td>{duration(t.staleness_ms / 1000)}</td>
                  <td>{t.deadline_at ? duration((new Date(t.detected_at).getTime() - new Date(t.deadline_at).getTime()) / 1000) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}
