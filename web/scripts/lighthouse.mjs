// Lighthouse against a running, seeded stack, using Playwright's Chromium
// (the host's Chrome is not used). Desktop and mobile (Lighthouse's default:
// emulated mid-range phone, simulated slow 4G) presets; three representative pages.
import { writeFileSync } from "node:fs";

import { chromium } from "@playwright/test";
import * as chromeLauncher from "chrome-launcher";
import lighthouse from "lighthouse";
import desktop from "lighthouse/core/config/desktop-config.js";

const base = process.argv[2] ?? "http://localhost:3088";
const api = process.env.E2E_API_URL ?? "http://localhost:8088";
const runs = Number(process.env.LH_RUNS ?? 3);

const byName = async (name) =>
  (await (await fetch(`${api}/api/v1/datasets/by-name?namespace=iceberg%3A%2F%2Flake&name=${name}`)).json()).id;
const pages = {
  "dataset list": `${base}/`,
  "dataset page (lineage, diff)": `${base}/datasets/${await byName("marts.daily_revenue")}`,
  "change request": `${base}/change-requests/1`,
};

const chrome = await chromeLauncher.launch({ chromePath: chromium.executablePath(), chromeFlags: ["--headless=new", "--no-sandbox"] });
const results = {};
try {
  for (const [preset, config] of [["desktop", desktop], ["mobile", undefined]]) {
  results[preset] = {};
  for (const [label, url] of Object.entries(pages)) {
    const scores = [];
    for (let i = 0; i < runs; i++) {
      const r = await lighthouse(url, { port: chrome.port, output: "json", logLevel: "error" }, config);
      const c = r.lhr.categories;
      scores.push({
        performance: Math.round(c.performance.score * 100),
        accessibility: Math.round(c.accessibility.score * 100),
        "best-practices": Math.round(c["best-practices"].score * 100),
        seo: Math.round(c.seo.score * 100),
        lcp_ms: Math.round(r.lhr.audits["largest-contentful-paint"].numericValue),
        tbt_ms: Math.round(r.lhr.audits["total-blocking-time"].numericValue),
        failing_a11y: Object.values(r.lhr.audits)
          .filter((a) => a.score === 0 && r.lhr.categories.accessibility.auditRefs.some((ref) => ref.id === a.id))
          .map((a) => a.id),
      });
    }
    const med = (k) => scores.map((s) => s[k]).sort((a, b) => a - b)[Math.floor(scores.length / 2)];
    results[preset][label] = {
      url,
      runs: scores,
      median: Object.fromEntries(["performance", "accessibility", "best-practices", "seo", "lcp_ms", "tbt_ms"].map((k) => [k, med(k)])),
    };
    console.log(preset, label, JSON.stringify(results[preset][label].median), scores[0].failing_a11y);
  }
  }
} finally {
  await chrome.kill();
}
writeFileSync("../bench/results/lighthouse.json", JSON.stringify({ lighthouse: "13.5.0", runs_per_page: runs, chromium: chromium.executablePath().split("/").slice(-6, -4).join("/"), results }, null, 2) + "\n");
