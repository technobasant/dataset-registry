"use server";

import { revalidatePath } from "next/cache";
import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";

import { api, ApiError } from "@/lib/api";
import { personaById } from "@/lib/personas";
import type { ChangeRequest, Dataset } from "@/lib/types";

function back(path: string, params: Record<string, string>): never {
  const q = new URLSearchParams(params).toString();
  redirect(`${path}${path.includes("?") ? "&" : "?"}${q}`);
}

async function attempt(path: string, fn: () => Promise<string | void>): Promise<never> {
  let notice: string | void;
  try {
    notice = await fn();
  } catch (e) {
    if (e instanceof ApiError) back(path, { error: e.detail });
    throw e;
  }
  revalidatePath("/", "layout");
  back(path, notice ? { notice } : {});
}

function field(form: FormData, key: string): string {
  return String(form.get(key) ?? "").trim();
}

export async function switchPersona(form: FormData) {
  const p = personaById(field(form, "persona"));
  (await cookies()).set("persona", p.id, { path: "/", sameSite: "lax", httpOnly: true });
  const referer = (await headers()).get("referer");
  let to = "/";
  if (referer) {
    const u = new URL(referer);
    to = u.pathname;
  }
  redirect(to);
}

export async function createDataset(form: FormData) {
  let created: Dataset | undefined;
  try {
    const slo = Number(field(form, "slo_minutes"));
    const schema = field(form, "schema");
    created = await api<Dataset>("/api/v1/datasets", {
      method: "POST",
      body: {
        namespace: field(form, "namespace"),
        name: field(form, "name"),
        description: field(form, "description"),
        owner: { team: field(form, "owner_team"), person: field(form, "owner_person") },
        tier: field(form, "tier"),
        compatibility: field(form, "compatibility"),
        ...(slo > 0 ? { freshness_slo_seconds: Math.round(slo * 60) } : {}),
        ...(schema ? { schema: JSON.parse(schema) as unknown } : {}),
      },
    });
  } catch (e) {
    if (e instanceof ApiError) back("/datasets/new", { error: e.detail });
    if (e instanceof SyntaxError) back("/datasets/new", { error: `The schema is not valid JSON: ${e.message}` });
    throw e;
  }
  revalidatePath("/", "layout");
  redirect(`/datasets/${created.id}?notice=${encodeURIComponent("Registered.")}`);
}

export async function proposeSchema(form: FormData) {
  const id = field(form, "dataset_id");
  const path = `/datasets/${id}`;
  let schema: unknown;
  try {
    schema = JSON.parse(field(form, "schema"));
  } catch (e) {
    back(path, { error: `The schema is not valid JSON: ${(e as Error).message}` });
  }
  return attempt(path, async () => {
    const res = await api<{ published: boolean; version: { version: number }; change_request?: ChangeRequest }>(
      `/api/v1/datasets/${id}/schemas`,
      { method: "POST", body: { schema } },
    );
    if (res.published) return `Version ${res.version.version} published: compatible under this dataset's mode.`;
    return `Version ${res.version.version} is breaking. Change request #${res.change_request?.id} is waiting for consumers.`;
  });
}

export async function registerConsumer(form: FormData) {
  const id = field(form, "dataset_id");
  return attempt(`/datasets/${id}`, async () => {
    await api(`/api/v1/datasets/${id}/consumers`, {
      method: "POST",
      body: { contact: field(form, "contact"), purpose: field(form, "purpose") },
    });
    return "Your team is now a registered consumer. Breaking changes will wait for you.";
  });
}

export async function unregisterConsumer(form: FormData) {
  const id = field(form, "dataset_id");
  return attempt(`/datasets/${id}`, async () => {
    await api(`/api/v1/datasets/${id}/consumers/${encodeURIComponent(field(form, "team"))}`, { method: "DELETE" });
    return "Unregistered.";
  });
}

export async function reviewChange(form: FormData) {
  const id = field(form, "change_request_id");
  return attempt(`/change-requests/${id}`, async () => {
    const decision = field(form, "decision");
    const cr = await api<ChangeRequest>(`/api/v1/change-requests/${id}/reviews`, {
      method: "POST",
      body: { decision, reason: field(form, "reason") },
    });
    return `Recorded: ${decision}. The change request is now ${cr.state}.`;
  });
}

export async function closeChange(form: FormData) {
  const id = field(form, "change_request_id");
  const action = field(form, "action") === "withdraw" ? "withdraw" : "publish";
  return attempt(`/change-requests/${id}`, async () => {
    const cr = await api<ChangeRequest>(`/api/v1/change-requests/${id}/${action}`, { method: "POST" });
    return `Change request ${cr.state}.`;
  });
}
