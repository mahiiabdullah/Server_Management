import { defineConfig, devices } from "@playwright/test";

// End-to-end tests run against a running stack (docker compose ... up), not a dev server:
//   E2E_BASE_URL=http://localhost:8088 E2E_EMAIL=... E2E_PASSWORD=... npx playwright test
// The setup project signs in once and shares the session, because the login endpoint
// is rate limited per IP (5 attempts per 15 minutes).
export default defineConfig({
  testDir: "./tests-e2e",
  timeout: 60_000,
  retries: 0,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:8088",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    {
      name: "chromium",
      dependencies: ["setup"],
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 }, storageState: "test-results/.auth.json" },
    },
  ],
});
