import Link from "next/link";

import { Flash, one, type SearchParams } from "@/components/Flash";
import { StatusBadge } from "@/components/StatusBadge";
import { api } from "@/lib/api";
import { ago, duration, qualified } from "@/lib/format";
import type { Dataset, SLOStatus } from "@/lib/types";

export const dynamic = "force-dynamic";

const order: SLOStatus[] = ["breached", "at_risk", "healthy", "unknown"];

export default async function Home({ searchParams }: { searchParams: SearchParams }) {
  const sp = await searchParams;
  const status = one(sp.status) as SLOStatus | undefined;
  const { datasets } = await api<{ datasets: Dataset[] }>("/api/v1/datasets");
  const owned = datasets.filter((d) => d.registered);
  const unowned = datasets.filter((d) => !d.registered);
  const counts = Object.fromEntries(order.map((s) => [s, owned.filter((d) => d.freshness_slo_seconds && d.slo_status === s).length]));
  const shown = (status ? owned.filter((d) => d.slo_status === status) : owned).sort(
    (a, b) => order.indexOf(a.slo_status) - order.indexOf(b.slo_status) || a.name.localeCompare(b.name),
  );

  return (
    <>
      <h1>Datasets</h1>
      <Flash error={one(sp.error)} notice={one(sp.notice)} />
      <p className="lede">
        {owned.length} owned datasets. Every one has a team, a person, a freshness SLO and a contract; {unowned.length} more are
        written by pipelines and owned by nobody.
      </p>

      <nav aria-label="Filter by freshness" className="filters">
        <Link href="/" aria-current={!status ? "page" : undefined}>
          All ({owned.length})
        </Link>
        {order.map((s) => (
          <Link key={s} href={`/?status=${s}`} aria-current={status === s ? "page" : undefined}>
            <StatusBadge status={s} /> {counts[s]}
          </Link>
        ))}
      </nav>

      <table className="datasets">
        <caption className="sr-only">Owned datasets, most urgent first</caption>
        <thead>
          <tr>
            <th scope="col">Dataset</th>
            <th scope="col">Owner</th>
            <th scope="col">Tier</th>
            <th scope="col">Freshness</th>
            <th scope="col">Last write</th>
            <th scope="col">SLO</th>
            <th scope="col">Contract</th>
            <th scope="col">Consumers</th>
          </tr>
        </thead>
        <tbody>
          {shown.map((d) => (
            <tr key={d.id} className={d.slo_status === "breached" ? "row-breached" : undefined}>
              <td>
                <Link href={`/datasets/${d.id}`}>{d.name}</Link>
                <div className="muted small">{d.namespace}</div>
              </td>
              <td>
                {d.owner?.team}
                <div className="muted small">{d.owner?.person}</div>
              </td>
              <td>{d.tier}</td>
              <td>
                <StatusBadge status={d.slo_status} slo={d.freshness_slo_seconds} />
              </td>
              <td>{ago(d.last_write_at)}</td>
              <td>{d.freshness_slo_seconds ? duration(d.freshness_slo_seconds) : "—"}</td>
              <td>
                {d.current_version ? `v${d.current_version}` : "none"}
                {d.open_change_request_id ? (
                  <div className="small">
                    <Link href={`/change-requests/${d.open_change_request_id}`}>change request #{d.open_change_request_id}</Link>
                  </div>
                ) : null}
              </td>
              <td>{d.consumer_count ?? 0}</td>
            </tr>
          ))}
        </tbody>
      </table>

      {unowned.length > 0 ? (
        <section aria-labelledby="unowned">
          <h2 id="unowned">Seen in lineage, owned by nobody</h2>
          <p className="muted">
            Pipelines write these, and some are read downstream. Nobody can register as a consumer until a team claims them.
          </p>
          <ul className="unowned">
            {unowned.map((d) => (
              <li key={d.id}>
                <Link href={`/datasets/${d.id}`}>{qualified(d.namespace, d.name)}</Link>{" "}
                <span className="muted small">last write {ago(d.last_write_at)}</span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </>
  );
}
