import { expect, test } from "@playwright/test";
import workspaces from "../../src/shell/workspaces/workspaces.json" with { type: "json" };
import { ShellPage } from "../shell.page";
import { DEV_IDENTITY, PERSONAS, type PersonaName } from "./personas";
import { covers, stubAvailability, stubBriefings } from "./support";

const UNRELATED_KEY = "depot.test.unrelated";
const BROWSE = "Browse workspaces";
const personas: PersonaName[] = ["ap-clerk", "planner"];

for (const persona of personas) {
  test(`reset restores the ${persona} defaults after activity changed the order`, async ({ browser }) => {
    covers("O9");
    const origin = `http://localhost:${PERSONAS[persona].port}`;
    const context = await browser.newContext({
      baseURL: origin,
      storageState: { cookies: [], origins: [{ origin, localStorage: [{ name: `depot.onboarding.v1.${DEV_IDENTITY}`, value: "completed" }] }] },
    });
    const page = await context.newPage();
    await stubAvailability(page);
    await stubBriefings(page);
    const order = () => page.getByRole("region", { name: BROWSE }).getByRole("heading", { level: 3 }).allTextContents();

    await new ShellPage(page).goto("/");
    await expect.poll(async () => (await order()).length).toBeGreaterThanOrEqual(3);
    const defaults = await order();
    const target = defaults[defaults.length - 1] ?? "";
    const routes = workspaces.workspaces.find((w) => w.label === target)?.entries ?? [];
    expect(routes.length).toBeGreaterThan(0);

    const at = new Date().toISOString();
    const rows = JSON.stringify(routes.map((route) => ({ kind: "page", route, at })));
    await page.addInitScript(
      ([activityKey, activity, unrelatedKey]) => {
        window.localStorage.setItem(activityKey ?? "", activity ?? "");
        window.localStorage.setItem(unrelatedKey ?? "", "keep me");
      },
      [`depot.activity.v1.${DEV_IDENTITY}`, rows, UNRELATED_KEY],
    );
    await page.reload();
    await expect.poll(async () => (await order())[1]).toBe(target);

    await page.getByRole("button", { name: "Reset personalization" }).click();
    await page.getByRole("alertdialog").getByRole("button", { name: "Reset", exact: true }).click();
    await expect.poll(order).toEqual(defaults);
    await expect(page.getByText("Pages you open show up here")).toBeVisible();
    expect(await page.evaluate((key) => window.localStorage.getItem(key), `depot.activity.v1.${DEV_IDENTITY}`)).toBeNull();
    expect(await page.evaluate((key) => window.localStorage.getItem(key), UNRELATED_KEY)).toBe("keep me");
    await context.close();
  });
}
