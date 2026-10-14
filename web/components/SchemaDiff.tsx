// A line diff of two schema versions, keys sorted so that only real changes
// show. Longest-common-subsequence on lines; schemas are small.

function canonical(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(canonical);
  if (v && typeof v === "object") {
    return Object.fromEntries(
      Object.keys(v as Record<string, unknown>)
        .sort()
        .map((k) => [k, canonical((v as Record<string, unknown>)[k])]),
    );
  }
  return v;
}

type Line = { kind: "same" | "add" | "del"; text: string };

export function diffLines(a: string[], b: string[]): Line[] {
  const n = a.length;
  const m = b.length;
  const lcs: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
  }
  const out: Line[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i]! });
      i++;
      j++;
    } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) {
      out.push({ kind: "del", text: a[i++]! });
    } else {
      out.push({ kind: "add", text: b[j++]! });
    }
  }
  while (i < n) out.push({ kind: "del", text: a[i++]! });
  while (j < m) out.push({ kind: "add", text: b[j++]! });
  return out;
}

export function SchemaDiff({ from, to, fromLabel, toLabel }: { from: unknown; to: unknown; fromLabel: string; toLabel: string }) {
  const a = JSON.stringify(canonical(from), null, 2).split("\n");
  const b = JSON.stringify(canonical(to), null, 2).split("\n");
  const lines = diffLines(a, b);
  const added = lines.filter((l) => l.kind === "add").length;
  const removed = lines.filter((l) => l.kind === "del").length;
  return (
    <figure className="diff">
      <figcaption>
        {fromLabel} → {toLabel}: <span className="diff-add-count">{added} added</span>,{" "}
        <span className="diff-del-count">{removed} removed</span> lines
      </figcaption>
      <pre aria-label={`Diff from ${fromLabel} to ${toLabel}`}>
        {lines.map((l, idx) => (
          <span key={idx} className={`diff-line diff-${l.kind}`}>
            <span className="diff-sign" aria-hidden="true">
              {l.kind === "add" ? "+" : l.kind === "del" ? "−" : " "}
            </span>
            {l.kind === "add" ? <ins>{l.text}</ins> : l.kind === "del" ? <del>{l.text}</del> : l.text}
            {"\n"}
          </span>
        ))}
      </pre>
    </figure>
  );
}
