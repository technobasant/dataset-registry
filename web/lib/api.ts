import "server-only";

import { cookies } from "next/headers";

import { personaById, type Persona } from "./personas";

const API_URL = process.env.API_URL ?? "http://127.0.0.1:8088";

export class ApiError extends Error {
  constructor(
    public status: number,
    public detail: string,
  ) {
    super(detail);
  }
}

export async function currentPersona(): Promise<Persona> {
  const jar = await cookies();
  return personaById(jar.get("persona")?.value);
}

// Tokens come from the API's development issuer, one per persona, cached for
// the life of the server process.
const tokens = new Map<string, string>();

async function tokenFor(p: Persona): Promise<string | null> {
  if (!p.subject) return null;
  const cached = tokens.get(p.id);
  if (cached) return cached;
  const res = await fetch(`${API_URL}/api/v1/dev/tokens`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ subject: p.subject, role: p.role, team: p.team }),
    cache: "no-store",
  });
  if (!res.ok) throw new ApiError(res.status, "could not obtain a development token");
  const { token } = (await res.json()) as { token: string };
  tokens.set(p.id, token);
  return token;
}

export async function api<T>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const token = await tokenFor(await currentPersona());
  const res = await fetch(`${API_URL}${path}`, {
    method: init.method ?? "GET",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  if (!res.ok) {
    let detail = text;
    try {
      detail = (JSON.parse(text) as { detail?: string }).detail ?? text;
    } catch {
      // not JSON
    }
    throw new ApiError(res.status, detail);
  }
  return JSON.parse(text) as T;
}
