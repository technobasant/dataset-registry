import { switchPersona } from "@/app/actions";
import { currentPersona } from "@/lib/api";
import { personas } from "@/lib/personas";

export async function PersonaSwitcher() {
  const current = await currentPersona();
  return (
    <form action={switchPersona} className="persona">
      <label htmlFor="persona">Acting as</label>
      <select id="persona" name="persona" defaultValue={current.id}>
        {personas.map((p) => (
          <option key={p.id} value={p.id}>
            {p.label}
          </option>
        ))}
      </select>
      <button type="submit">Switch</button>
    </form>
  );
}
