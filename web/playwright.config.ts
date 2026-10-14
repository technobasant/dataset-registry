import { defineConfig, devices } from "@playwright/test";

// flow.spec.ts starts its own API (from source, with a 1 s freshness
// interval) and the production build of the UI. screenshots.spec.ts instead
// points at a running, seeded stack through E2E_BASE_URL.
const API_PORT = process.env.E2E_API_PORT ?? "8099";
const WEB_PORT = process.env.E2E_WEB_PORT ?? "3099";
const external = process.env.E2E_BASE_URL;
const DATABASE_URL =
  process.env.E2E_DATABASE_URL ?? `postgres://registry:registry@127.0.0.1:${process.env.DR_PG_PORT ?? "55433"}/registry_e2e?sslmode=disable`;

export default defineConfig({
  testDir: "e2e",
  timeout: 90_000,
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: external ?? `http://127.0.0.1:${WEB_PORT}`,
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: external
    ? undefined
    : [
        {
          command: "go build -o bin/registry ./cmd/registry && ./bin/registry serve",
          cwd: "../api",
          url: `http://127.0.0.1:${API_PORT}/readyz`,
          timeout: 180_000,
          reuseExistingServer: false,
          env: {
            DATABASE_URL,
            DR_ADDR: `127.0.0.1:${API_PORT}`,
            DR_SIGNING_KEY: "e2e-only-signing-key-0123456789abcdef",
            DR_DEV_TOKENS: "true",
            DR_FRESHNESS_INTERVAL: "1s",
            LOG_LEVEL: "WARN",
          },
        },
        {
          // The standalone server, as the container runs it.
          command: `cp -r .next/static .next/standalone/.next/ && cp -r public .next/standalone/ && node .next/standalone/server.js`,
          url: `http://127.0.0.1:${WEB_PORT}`,
          timeout: 120_000,
          reuseExistingServer: false,
          env: { API_URL: `http://127.0.0.1:${API_PORT}`, PORT: WEB_PORT, HOSTNAME: "127.0.0.1" },
        },
      ],
});
