import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "./shell.page";

const ME = "0195f0a0-0000-7000-8000-000000000001";
const REPORT = "0195f0a0-0000-7000-8000-000000000002";

const me = {
  id: ME,
  personnelNumber: "W-1",
  workerType: "EMPLOYEE",
  partyPersonId: ME,
  legalName: "Rita Ray",
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
  pii: { nationalId: "***-**-6789", masked: true },
  asOf: { workerId: ME, date: "2026-11-05", employments: [], positionId: null, positionNumber: "P-1", fteShare: "1.00", managerWorkerId: null },
};

/** A small in-memory HR service: the employee's changes and the manager's queue share one list. */
async function fakeHr(page: Page) {
  const changes: Record<string, unknown>[] = [
    { id: "0195f0a0-0000-7000-8000-000000000020", workerId: REPORT, workerName: "Sam Lee", personnelNumber: "W-2", kind: "HOME_ADDRESS", status: "PENDING", summary: "Shelbyville, US", requestedBy: "u2", createdAt: "2026-11-04T10:00:00Z" },
  ];
  const posted: unknown[] = [];
  await page.route("**/api/v1/hr/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1/hr", "");
    if (path === "/me") return route.fulfill({ json: me });
    if (path === "/me/profile-changes" && request.method() === "POST") {
      const body = request.postDataJSON();
      posted.push(body);
      const created = { id: `0195f0a0-0000-7000-8000-00000000003${posted.length}`, workerId: ME, workerName: "Rita Ray", personnelNumber: "W-1", kind: body.kind,
        status: body.kind === "PREFERRED_NAME" ? "APPLIED" : "PENDING", summary: body.kind === "PREFERRED_NAME" ? body.preferredName : `${body.address.city}, ${body.address.country}`,
        requestedBy: "u1", createdAt: "2026-11-05T09:00:00Z" };
      changes.unshift(created);
      return route.fulfill({ status: 201, json: created });
    }
    if (path === "/me/profile-changes") return route.fulfill({ json: { items: changes.filter((c) => c.workerId === ME) } });
    if (path === "/me/team/profile-changes") return route.fulfill({ json: { items: changes.filter((c) => c.workerId === REPORT && c.status === "PENDING") } });
    const decide = path.match(/^\/me\/team\/profile-changes\/([^/]+)\/decide$/);
    if (decide) {
      const hit = changes.find((c) => c.id === decide[1]);
      if (!hit) return route.fulfill({ status: 404, json: { code: "HCM-ESS-NOT-FOUND", detail: "profile change not found" } });
      hit.status = request.postDataJSON().decision === "APPROVE" ? "APPROVED" : "REJECTED";
      return route.fulfill({ json: hit });
    }
    return route.fulfill({ status: 404, json: { code: "TEST-404", detail: path } });
  });
  return posted;
}

test("an employee updates their details and their manager approves a report's address", async ({ page }) => {
  const posted = await fakeHr(page);
  const shell = new ShellPage(page);

  await shell.goto("/hr/my/profile");
  await expect(shell.title).toHaveText("My profile");
  await page.getByRole("button", { name: "Change preferred name" }).click();
  await page.getByRole("textbox", { name: "Preferred name", exact: true }).fill("Rae");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Preferred name updated.")).toBeVisible();

  await page.getByRole("button", { name: "Change home address" }).click();
  await page.getByRole("textbox", { name: "Street address", exact: true }).fill("1 Main St");
  await page.getByRole("textbox", { name: "City", exact: true }).fill("Springfield");
  await page.getByRole("textbox", { name: "Postal code", exact: true }).fill("62701");
  await page.getByRole("textbox", { name: "Country code", exact: true }).fill("us");
  await page.getByRole("button", { name: "Send for approval" }).click();
  await expect(page.getByText("Home address change sent for approval.")).toBeVisible();
  const mine = page.getByRole("table", { name: "My changes" });
  await expect(mine).toContainText("Springfield, US");
  await expect(mine).toContainText("Waiting for approval");
  expect(posted).toEqual([
    { kind: "PREFERRED_NAME", preferredName: "Rae" },
    { kind: "HOME_ADDRESS", address: { line1: "1 Main St", city: "Springfield", postalCode: "62701", country: "US" } },
  ]);

  await shell.goto("/hr/my/team/changes");
  await expect(shell.title).toHaveText("Team changes");
  await page.getByRole("button", { name: "Approve Sam Lee's home address change" }).click();
  await expect(page.getByText("Sam Lee's home address change is approved.")).toBeVisible();
  await expect(page.getByText("Nothing waiting for a decision")).toBeVisible();
});
