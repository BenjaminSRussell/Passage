import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "./shell.page";

const item = (n: number) => ({
  id: `0195f0a0-0000-7000-8000-00000000004${n}`,
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: `PR-100${n}`,
  link: `/purchasing/requisitions/${n}`,
  actions: ["approve", "reject", "open"],
  createdAt: "2026-11-05T09:00:00Z",
  dueDate: "2026-11-06",
});

test("home shows my work, tiles, a badge on My work and stays under 30 links", async ({ page }) => {
  await page.route("**/api/v1/inbox**", (route) => route.fulfill({ json: { items: [item(1), item(2), item(3)], unavailable: [], truncated: [] } }));
  const shell = new ShellPage(page);
  await shell.goto("/");
  const main = page.getByRole("main");
  await expect(main.getByText("3 items are waiting for you.")).toBeVisible();
  await expect(main.getByRole("list", { name: "My work at a glance" })).toContainText("Overdue");
  await expect(shell.workspaceNav.getByRole("link", { name: /^My work.*3 waiting$/ })).toBeVisible();
  expect(await main.getByRole("link").count()).toBeLessThanOrEqual(30);
});

const steps = [
  ["features", "What your company does"],
  ["company", "Company and legal entity"],
  ["fiscal-calendar", "Fiscal calendar"],
];

async function mockSetup(page: Page) {
  const status: Record<string, string> = {};
  const body = () => {
    const list = steps.map(([key, name]) => ({ key, name, description: `${name}.`, status: status[key] ?? "todo" }));
    const finished = list.filter((s) => s.status !== "todo").length;
    return { steps: list, total: list.length, finished, complete: finished === list.length };
  };
  await page.route("**/api/v1/tenant/setup**", async (route) => {
    const request = route.request();
    if (request.method() === "PUT") status[new URL(request.url()).pathname.split("/").pop() ?? ""] = (request.postDataJSON() as { status: string }).status;
    await route.fulfill({ json: body() });
  });
}

test("the setup wizard walks the steps, saves progress and resumes", async ({ page }) => {
  await mockSetup(page);
  const shell = new ShellPage(page);
  await shell.goto("/setup");
  await expect(page.getByText("0 of 3 steps finished.")).toBeVisible();
  await page.getByRole("button", { name: "Mark as done" }).click();
  await expect(page.getByText("1 of 3 steps finished.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Company and legal entity" })).toBeVisible();
  await page.getByRole("button", { name: "Skip for now" }).click();
  await expect(page.getByText("2 of 3 steps finished.")).toBeVisible();

  await page.reload();
  await expect(page.getByText("2 of 3 steps finished.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Fiscal calendar" })).toBeVisible();
  await page.getByRole("button", { name: "Mark as done" }).click();
  await expect(page.getByText("Setup is finished.").first()).toBeVisible();
});

test("the features page turns a feature off and shows the plain message when a dependency blocks it", async ({ page }) => {
  const feature = (key: string, name: string, enabled: boolean) => ({ key, name, description: `${name}.`, module: key, dependsOn: [], enabled });
  let edi = true;
  await page.route("**/api/v1/tenant/features", async (route) => {
    const request = route.request();
    if (request.method() === "PUT") {
      const change = (request.postDataJSON() as { features?: Record<string, boolean> }).features ?? {};
      if (change.finance === false) {
        await route.fulfill({ status: 422, json: { code: "IDN-FEATURE-DEPENDENCY", detail: "x", errors: { finance: "Finance cannot be turned off while Electronic data interchange uses it. Turn those off first." } } });
        return;
      }
      edi = change.edi ?? edi;
    }
    await route.fulfill({ json: { features: [feature("finance", "Finance", true), feature("edi", "Electronic data interchange", edi)], templates: [] } });
  });
  const shell = new ShellPage(page);
  await shell.goto("/setup/features");
  await page.getByRole("checkbox", { name: /Electronic data interchange/ }).click();
  await expect(page.getByRole("checkbox", { name: /Electronic data interchange/ })).not.toBeChecked();
  await page.getByRole("checkbox", { name: /^Finance/ }).click();
  await expect(page.getByText(/Finance cannot be turned off while Electronic data interchange uses it/)).toBeVisible();
  await expect(page.getByRole("checkbox", { name: /^Finance/ })).toBeChecked();
});
