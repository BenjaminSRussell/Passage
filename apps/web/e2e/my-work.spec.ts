import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const item = {
  id: "0195f0a0-0000-7000-8000-000000000041",
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: "PR-1042",
  link: "/purchasing/requisitions/0195f0a0-0000-7000-8000-000000000041",
  actions: ["approve", "reject", "open"],
  createdAt: "2026-11-05T09:00:00Z",
};

test("My work lists what is waiting and Open goes to the document", async ({ page }) => {
  await page.route("**/api/v1/inbox**", (route) => route.fulfill({ json: { items: [item], unavailable: [], truncated: [] } }));
  const shell = new ShellPage(page);

  await shell.goto("/workflow/inbox");
  await expect(page.getByRole("heading", { level: 2, name: "My work" })).toBeVisible();
  const table = page.getByRole("table", { name: "Work waiting for you" });
  await expect(table).toContainText("PR-1042");
  await expect(table).toContainText("Waiting for approval");
  await page.getByRole("link", { name: "Open Requisition approval PR-1042" }).click();
  await expect(page).toHaveURL(/\/purchasing\/requisitions\/0195f0a0-0000-7000-8000-000000000041$/);
});
