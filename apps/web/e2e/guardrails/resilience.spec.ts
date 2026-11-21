import { expect, test, type Page } from "@playwright/test";
import { ShellPage } from "../shell.page";
import { covers, FAULTS, postInvoice, stubBriefings, stubCoreApi, stubFault, type Fault } from "./support";

async function expectNoAiError(page: Page) {
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByRole("dialog")).toHaveCount(0);
}

async function arrange(page: Page, fault: Fault) {
  const core = await stubCoreApi(page);
  await stubBriefings(page);
  await stubFault(page, fault);
  return core;
}

for (const fault of FAULTS) {
  test.describe(`AI layer failing (${fault})`, () => {
    test("the home shows no AI error alert", async ({ page }) => {
      covers("O8");
      await arrange(page, fault);
      await new ShellPage(page).goto("/");
      await expectNoAiError(page);
    });

    test("navigates, looks up, creates, approves and posts without an AI error on any surface", async ({ page }) => {
      covers("O1", "O4", "O5", "O8");
      const core = await arrange(page, fault);
      const shell = new ShellPage(page);

      await shell.goto("/");
      await expect(page.getByRole("article", { name: "Copilot answer" })).toHaveCount(0);

      await shell.openPaletteWithShortcut();
      await shell.paletteInput.fill("glossary");
      await shell.palette.getByRole("option", { name: /^Glossary/ }).first().click();
      await expect(page).toHaveURL(/\/glossary$/);
      await expectNoAiError(page);

      await shell.openPaletteWithShortcut();
      await shell.paletteInput.fill("acme");
      await expect(shell.palette.getByRole("option", { name: /Party V-0007 Acme Tooling/ })).toBeVisible();
      await page.keyboard.press("Escape");
      await expectNoAiError(page);

      await shell.goto("/hr/my/profile");
      await page.getByRole("button", { name: "Change preferred name" }).click();
      await page.getByRole("textbox", { name: "Preferred name", exact: true }).fill("Rae");
      await page.getByRole("button", { name: "Save", exact: true }).click();
      await expect(page.getByText("Preferred name updated.")).toBeVisible();
      expect(core.created).toEqual([{ kind: "PREFERRED_NAME", preferredName: "Rae" }]);
      await expectNoAiError(page);

      await shell.goto("/workflow/inbox");
      await page.getByRole("button", { name: "Approve Customer change CHG-1" }).click();
      await page.getByRole("button", { name: "Confirm approval" }).click();
      await expect(page.getByText("Approved: CHG-1.")).toBeVisible();
      await expectNoAiError(page);

      await postInvoice(page);
      expect(core.posted).toMatchObject([{ invoiceNumber: "INV-100" }]);
      await expectNoAiError(page);
    });
  });
}
