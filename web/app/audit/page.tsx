import Link from "next/link";

import { api } from "@/lib/api";
import { when } from "@/lib/format";
import type { AuditEntry } from "@/lib/types";

export const dynamic = "force-dynamic";

export default async function Audit() {
  const { entries } = await api<{ entries: AuditEntry[] }>("/api/v1/audit?limit=200");
  return (
    <>
      <h1>Audit log</h1>
      <p className="lede">Append-only: the database refuses updates and deletes on this table.</p>
      <table>
        <caption className="sr-only">Most recent 200 entries</caption>
        <thead>
          <tr>
            <th scope="col">At</th>
            <th scope="col">Actor</th>
            <th scope="col">Action</th>
            <th scope="col">Entity</th>
            <th scope="col">Details</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <tr key={e.id}>
              <td className="nowrap">{when(e.at)}</td>
              <td>{e.actor}</td>
              <td>
                <code>{e.action}</code>
              </td>
              <td>
                {e.entity_type === "dataset" ? (
                  <Link href={`/datasets/${e.entity_id}`}>dataset {e.entity_id}</Link>
                ) : (
                  <Link href={`/change-requests/${e.entity_id}`}>change request #{e.entity_id}</Link>
                )}
              </td>
              <td>
                <code className="small">{JSON.stringify(e.details)}</code>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
