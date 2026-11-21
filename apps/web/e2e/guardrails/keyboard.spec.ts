import { expect, test, type Locator, type Page } from "@playwright/test";
import { ShellPage } from "../shell.page";
import { covers, PROMPT_NAME, stubAvailability, stubBriefings, stubInbox, stubSearch } from "./support";

const MAX_PRESSES = 80;

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let presses = 0; presses < MAX_PRESSES; presses += 1) {
    if (await target.evaluate((el) => el === document.activeElement)) return;
    await page.keyboard.press(key);
  }
  throw new Error(`${key} never reached the target within ${MAX_PRESSES} presses`);
}

test.use({ storageState: { cookies: [], origins: [] } });

test("onboarding, home, command bar, assistant shortcut and inbox approval run without a pointer", async ({ page }) => {
  covers("O1", "O2", "O5", "O10");
  await stubAvailability(page);
  await stubBriefings(page);
  await stubSearch(page);
  await stubInbox(page);
  const shell = new ShellPage(page);

  await shell.goto("/");
  await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
  const next = page.getByRole("button", { name: "Next" });
  await expect(next).toBeFocused();
  await page.keyboard.press("Tab");
  await page.keyboard.press("Shift+Tab");
  await expect(next).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.getByText("Step 2 of 5", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  const prompt = page.getByRole("textbox", { name: PROMPT_NAME });
  await tabTo(page, prompt);
  await expect(prompt).toBeFocused();
  await tabTo(page, page.getByRole("link", { name: "Review Customer change CHG-1" }));

  await page.keyboard.press("ControlOrMeta+k");
  await expect(shell.paletteInput).toBeFocused();
  await page.keyboard.type("acme");
  await expect(shell.palette.getByRole("option", { name: /Party V-0007/ })).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/party\/v-1$/);

  await page.keyboard.press("ControlOrMeta+/");
  await expect(page).toHaveURL(/\/copilot$/);

  await tabTo(page, page.getByRole("link", { name: /^My work/ }), "Shift+Tab");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/workflow\/inbox$/);

  await tabTo(page, page.getByRole("button", { name: "Approve Customer change CHG-1" }));
  await page.keyboard.press("Enter");
  await tabTo(page, page.getByRole("button", { name: "Confirm approval" }));
  await page.keyboard.press("Enter");
  await expect(page.getByText("Approved: CHG-1.")).toBeVisible();
});
