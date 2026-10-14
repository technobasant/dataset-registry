import { createDataset } from "@/app/actions";
import { Flash, one, type SearchParams } from "@/components/Flash";
import { currentPersona } from "@/lib/api";

const example = JSON.stringify(
  {
    type: "object",
    properties: { return_id: { type: "string" }, order_id: { type: "string" }, reason: { type: "string", enum: ["damaged", "wrong_item", "changed_mind"] } },
    required: ["return_id", "order_id", "reason"],
  },
  null,
  2,
);

export default async function NewDataset({ searchParams }: { searchParams: SearchParams }) {
  const sp = await searchParams;
  const p = await currentPersona();
  return (
    <>
      <h1>Register a dataset</h1>
      <Flash error={one(sp.error)} />
      <p className="lede">Ownership comes first: a dataset needs a team and a person before anyone can depend on it.</p>
      {p.role === "consumer" ? (
        <p className="flash flash-error" role="alert">
          You are acting as a consumer. Switch to an owner or an admin to register a dataset.
        </p>
      ) : null}
      <form action={createDataset} className="stack">
        <fieldset>
          <legend>Identity (as OpenLineage names it)</legend>
          <label>
            Namespace
            <input name="namespace" required defaultValue="iceberg://lake" />
          </label>
          <label>
            Name
            <input name="name" required placeholder="sales.returns" />
          </label>
          <label>
            Description
            <input name="description" placeholder="One row per return request" />
          </label>
        </fieldset>
        <fieldset>
          <legend>Ownership</legend>
          <label>
            Team
            <input name="owner_team" required defaultValue={p.role === "owner" ? p.team : ""} />
          </label>
          <label>
            Person
            <input name="owner_person" required defaultValue={p.role === "owner" ? p.subject : ""} />
          </label>
        </fieldset>
        <fieldset>
          <legend>Promises</legend>
          <label>
            Tier
            <select name="tier" defaultValue="silver">
              <option value="gold">gold</option>
              <option value="silver">silver</option>
              <option value="bronze">bronze</option>
            </select>
          </label>
          <label>
            Freshness SLO (minutes of maximum staleness)
            <input name="slo_minutes" type="number" min="0.01" step="any" defaultValue="60" />
          </label>
          <label>
            Compatibility mode
            <select name="compatibility" defaultValue="FORWARD">
              <option value="FORWARD">FORWARD — protect consumers (default)</option>
              <option value="BACKWARD">BACKWARD — protect readers of history</option>
              <option value="FULL">FULL — both</option>
              <option value="NONE">NONE — no checks</option>
            </select>
          </label>
        </fieldset>
        <label>
          Contract (JSON Schema, version 1)
          <textarea name="schema" rows={12} defaultValue={example} spellCheck={false} />
        </label>
        <button type="submit">Register</button>
      </form>
    </>
  );
}
