import { mkdirSync } from "node:fs";

import { expect, test, type Page } from "@playwright/test";

// Run against a freshly seeded stack (make clean up seed screenshots), before
// make demo publishes the open change request.
const API = process.env.E2E_API_URL ?? `http://localhost:${process.env.DR_API_PORT ?? "8088"}`;
const OUT = "../docs/screenshots";

async function datasetId(name: string): Promise<number> {
  const res = await fetch(`${API}/api/v1/datasets/by-name?namespace=${encodeURIComponent("iceberg://lake")}&name=${encodeURIComponent(name)}`);
  return ((await res.json()) as { id: number }).id;
}

async function actAs(page: Page, label: string) {
  await page.getByLabel("Acting as").selectOption({ label });
  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "POST" && r.status() < 400),
    page.getByRole("button", { name: "Switch" }).click(),
  ]);
}

test.use({ viewport: { width: 1440, height: 1000 } });

test("screenshots", async ({ page }) => {
  mkdirSync(OUT, { recursive: true });
  await page.goto("/");
  await actAs(page, "Hana Sato — consumer, growth");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Datasets", level: 1 })).toBeVisible();
  await page.screenshot({ path: `${OUT}/datasets.png`, fullPage: true });

  const revenue = await datasetId("marts.daily_revenue");
  await page.goto(`/datasets/${revenue}?depth=2`);
  await expect(page.locator(".react-flow__node").first()).toBeVisible();
  await page.waitForTimeout(500);
  await page.locator("section", { has: page.getByRole("heading", { name: "Lineage" }) }).screenshot({ path: `${OUT}/lineage.png` });

  await page.goto(`/change-requests/1`);
  await expect(page.getByRole("button", { name: /Acknowledge for growth/ })).toBeVisible();
  await page.screenshot({ path: `${OUT}/change-request.png`, fullPage: true });

  const raw = await datasetId("raw.orders");
  await page.goto(`/datasets/${raw}?from=1&to=2`);
  await page.locator("#diff").screenshot({ path: `${OUT}/schema-diff.png` });

  const inv = await datasetId("raw.inventory_snapshots");
  await page.goto(`/datasets/${inv}`);
  await expect(page.getByText("Breached").first()).toBeVisible();
  await page.screenshot({ path: `${OUT}/breached.png`, fullPage: false });
});
