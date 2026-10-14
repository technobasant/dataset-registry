import { expect, test, type Page } from "@playwright/test";

const run = Date.now().toString(36);

// Switching persona is a server action that sets a cookie; wait for its
// response before navigating, or the navigation can cancel it.
async function actAs(page: Page, label: RegExp) {
  await page.getByLabel("Acting as").selectOption({ label: (await optionLabel(page, label))! });
  await Promise.all([
    page.waitForResponse((r) => r.request().method() === "POST" && r.status() < 400),
    page.getByRole("button", { name: "Switch" }).click(),
  ]);
}

async function optionLabel(page: Page, label: RegExp) {
  const options = await page.getByLabel("Acting as").locator("option").allTextContents();
  return options.find((o) => label.test(o));
}

test("a breaking change waits for its consumer, then publishes", async ({ page }) => {
  const name = `e2e.returns_${run}`;
  await page.goto("/");
  await actAs(page, /Amara Okafor/);

  // Register a dataset with a contract.
  await page.goto("/datasets/new");
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Person").fill("amara.okafor");
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();
  await expect(page.getByRole("status")).toHaveText("Registered.");
  const datasetUrl = page.url().split("?")[0]!;

  // A consumer registers its dependency.
  await actAs(page, /Jun Park/);
  await page.goto(datasetUrl);
  await page.getByLabel("Purpose").fill("refund reporting");
  await page.getByRole("button", { name: /Register finance-analytics as a consumer/ }).click();
  await expect(page.getByRole("status")).toContainText("registered consumer");

  // The owner removes a required field: breaking under FORWARD.
  await actAs(page, /Amara Okafor/);
  await page.goto(datasetUrl);
  const editor = page.getByLabel("Propose a new version");
  const schema = JSON.parse(await editor.inputValue()) as { properties: Record<string, unknown>; required: string[] };
  delete schema.properties.reason;
  schema.required = schema.required.filter((f) => f !== "reason");
  await editor.fill(JSON.stringify(schema, null, 2));
  await page.getByRole("button", { name: "Propose" }).click();
  await expect(page.getByRole("status")).toContainText("is breaking");

  await page.getByRole("link", { name: "Review it" }).click();
  await expect(page.getByRole("heading", { level: 1 })).toContainText("open");
  await expect(page.getByRole("button", { name: "Publish v2" })).toBeDisabled();
  await expect(page.getByRole("cell", { name: "field removed" })).toBeVisible();
  const crUrl = page.url().split("?")[0]!;

  // The consumer acknowledges.
  await actAs(page, /Jun Park/);
  await page.goto(crUrl);
  await page.getByLabel(/Reason/).fill("our refund report does not read reason");
  await page.getByRole("button", { name: "Acknowledge for finance-analytics" }).click();
  await expect(page.getByRole("status")).toContainText("now approved");

  // The owner publishes.
  await actAs(page, /Amara Okafor/);
  await page.goto(crUrl);
  await page.getByRole("button", { name: "Publish v2" }).click();
  await expect(page.getByRole("status")).toContainText("published");

  await page.goto(datasetUrl);
  await expect(page.getByText("v2 · FORWARD")).toBeVisible();
});

test("a dataset that misses its freshness SLO shows as breached", async ({ page }) => {
  const name = `e2e.stale_${run}`;
  await page.goto("/");
  await actAs(page, /Amara Okafor/);
  await page.goto("/datasets/new");
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Person").fill("amara.okafor");
  // 0.05 minutes = 3 s, never written: breached once 3 s pass and the
  // evaluator (1 s interval in this test) runs.
  await page.getByLabel(/Freshness SLO/).fill("0.05");
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page.getByRole("heading", { level: 1, name })).toBeVisible();

  await expect(async () => {
    await page.goto("/?status=breached");
    await expect(page.getByRole("row", { name: new RegExp(name.replace(".", "\\.")) })).toContainText("Breached");
  }).toPass({ timeout: 20_000 });
});
