import { expect, test, type Page } from "@playwright/test";
import path from "node:path";

// Requires a running stack with at least one registered, online server.
// Tests start signed in (see auth.setup.ts) unless they reset the storage state.
const SHOTS = process.env.E2E_SHOTS; // optional screenshot directory

async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: path.join(SHOTS, `${name}.png`), fullPage: true });
}

async function login(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
}

test.describe("signed out", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("unauthenticated visitors are sent to the login page", async ({ page }) => {
    await page.goto("/servers");
    await expect(page).toHaveURL(/\/login\?next=%2Fservers/);
  });

  test("a wrong password shows a clear error", async ({ page }) => {
    await page.goto("/login");
    await page.getByLabel("Email").fill("nobody@example.com");
    await page.getByLabel("Password").fill("wrong-password");
    await page.getByRole("button", { name: "Sign in" }).click();
    await expect(page.getByText("Email or password is incorrect")).toBeVisible();
    await shot(page, "01-login-error");
  });
});

test("overview shows live fleet numbers from the API", async ({ page }) => {
  await login(page);
  const summary = await (await page.request.get("/api/v1/fleet/summary")).json();
  await expect(page.getByRole("link", { name: /Registered/ })).toContainText(String(summary.servers.registered));
  await expect(page.getByRole("link", { name: /Online/ })).toContainText(String(summary.servers.online));
  await expect(page.getByText("Fleet CPU")).toBeVisible();
  await expect(page.getByText("Pricing not configured").first()).toBeVisible();
  await shot(page, "02-overview");
});

test("servers table, sorting, detail page with charts", async ({ page }) => {
  await login(page);
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  const rows = page.locator("tbody tr");
  await expect(rows.first()).toBeVisible();
  await expect(page.locator("tbody").getByText("Online").first()).toBeVisible();
  await page.getByRole("button", { name: /^CPU/ }).click();
  await expect(page).toHaveURL(/sort=cpu/);
  await shot(page, "03-servers");

  await page.locator("tbody a").first().click();
  await expect(page.getByRole("heading", { name: "History" })).toBeVisible();
  await expect(page.getByText("CPU used")).toBeVisible();
  await expect(page.locator(".recharts-surface").first()).toBeVisible({ timeout: 15_000 });
  await page.getByRole("tab", { name: "1h" }).click();
  await expect(page.getByText(/Data coverage/)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Filesystems", exact: true })).toBeVisible();
  await shot(page, "04-server-detail");
});

test("add-server drawer checks the endpoint and refuses duplicates", async ({ page }) => {
  await login(page);
  await page.goto("/servers");
  await page.getByRole("button", { name: "+ Add server" }).click();
  const drawer = page.getByRole("dialog", { name: "Add server" });
  const existing = await (await page.request.get("/api/v1/servers?page_size=1")).json();
  const url = existing.items[0].exporter_url as string;
  await drawer.getByLabel("Name", { exact: true }).fill("e2e-duplicate");
  await drawer.getByLabel("Node Exporter URL").fill(url);
  await drawer.getByRole("button", { name: "Check" }).click();
  await expect(drawer.getByRole("status")).toContainText("reachable");
  await shot(page, "05-add-server-check");
  await page.getByRole("button", { name: "Add server", exact: true }).click();
  await expect(drawer.getByRole("alert").first()).toContainText("already uses this exporter address");
  await drawer.getByLabel("Node Exporter URL").fill("http://169.254.169.254:9100/metrics");
  await drawer.getByRole("button", { name: "Check" }).click();
  await expect(drawer.getByRole("status")).toContainText("forbidden range");
  await page.keyboard.press("Escape");
});

test("costs, alerts, insights and settings render real states", async ({ page }) => {
  await login(page);
  await page.getByRole("link", { name: "Costs", exact: true }).click();
  await expect(page.getByText("Pricing not configured").filter({ visible: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "How this is calculated" }).filter({ visible: true })).toBeVisible();
  await shot(page, "06-costs");

  await page.getByRole("link", { name: /^△?\s*Alerts/ }).click();
  await page.getByRole("tab", { name: "Rules" }).filter({ visible: true }).click();
  await expect(page.getByRole("cell", { name: /High CPU/ }).filter({ visible: true })).toBeVisible();
  await shot(page, "07-alert-rules");

  await page.getByRole("link", { name: "Insights", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Underutilized servers" }).filter({ visible: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Month-to-month comparison" }).filter({ visible: true })).toBeVisible();
  await shot(page, "08-insights");

  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("tab", { name: "Notifications" }).filter({ visible: true }).click();
  await expect(page.getByText("email configured").filter({ visible: true })).toBeVisible();
  await shot(page, "09-settings-notifications");
  await page.getByRole("tab", { name: "Audit log" }).filter({ visible: true }).click();
  await expect(page.getByText("server.create").filter({ visible: true }).first()).toBeVisible();
});

test("mobile layout and dark theme", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await login(page);
  await expect(page.getByRole("button", { name: "Open menu" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  await shot(page, "10-mobile-overview");
  await page.getByRole("button", { name: "Open menu" }).click();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await expect(page.locator("tbody tr").filter({ visible: true }).first()).toBeVisible();
  await shot(page, "11-mobile-servers");

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => {
    localStorage.setItem("bm-theme", "dark");
    document.documentElement.dataset.theme = "dark";
  });
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await shot(page, "12-dark-overview");
});
