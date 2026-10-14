import type { Report } from "@/lib/types";

export function ReportTable({ report }: { report: Report }) {
  if (report.changes.length === 0) {
    return <p>No differences that affect validity.</p>;
  }
  return (
    <table>
      <caption>
        Compatibility under <strong>{report.mode}</strong>:{" "}
        {report.compatible ? "compatible" : `${report.breaking_count} breaking change${report.breaking_count === 1 ? "" : "s"}`}
      </caption>
      <thead>
        <tr>
          <th scope="col">Path</th>
          <th scope="col">Change</th>
          <th scope="col">Breaks backward</th>
          <th scope="col">Breaks forward</th>
          <th scope="col">Verdict</th>
        </tr>
      </thead>
      <tbody>
        {report.changes.map((c, i) => (
          <tr key={i}>
            <td>
              <code>{c.path}</code>
            </td>
            <td>
              <strong>{c.kind.replaceAll("_", " ")}</strong>
              <br />
              <span className="muted">{c.detail}</span>
            </td>
            <td>{c.breaks_backward ? "yes" : "no"}</td>
            <td>{c.breaks_forward ? "yes" : "no"}</td>
            <td>
              <span className={c.breaking ? "verdict-breaking" : "verdict-safe"}>{c.breaking ? "Breaking" : "Safe"}</span>
              <br />
              <span className="muted">{c.reason}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
