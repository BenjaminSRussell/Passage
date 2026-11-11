import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

/**
 * Pay vendors end to end against the demo build (fake backend, every duty held): propose the invoices that are
 * due, approve the proposal, take the payment run through its steps, read a vendor's remittance advice and go
 * back from the paid proposal to its run. A second test puts a vendor on payment hold and releases it.
 */

/** Serious or critical WCAG 2.2 AA violations of the page as it stands (an open dialog or card included). */
async function blockingViolations(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"]).analyze();
  return results.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
}

/** Payables and print requests the demo backend had no handler for. */
async function unhandled(page: Page): Promise<string[]> {
  const misses = await page.evaluate(() => (window as unknown as { __passageDemoMisses?: string[] }).__passageDemoMisses ?? []);
  return misses.filter((m) => /\/(payables|print)\//.test(m));
}

test("a proposal is created, approved, paid by a run, and gives a remittance advice", async ({ page }) => {
  await page.goto("./payables/proposals/new");
  await expect(page.getByRole("heading", { level: 1, name: "New payment proposal" })).toBeVisible();
  await page.getByLabel("Company currency").click();
  await page.getByRole("option", { name: /USD/ }).first().click();
  await page.getByRole("button", { name: "Create proposal" }).click();

  // Propose: the draft lists a payment per vendor.
  await expect(page.getByRole("heading", { name: /^Payment proposal for / })).toBeVisible();
  const payments = page.getByRole("table", { name: "Payments in this proposal" });
  await expect(payments.getByText("Ironbridge Foundry")).toBeVisible();

  // Approve.
  await page.getByRole("button", { name: "Approve proposal" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Approve proposal" }).click();
  await expect(page.getByText("The proposal is approved.")).toBeVisible();

  // Run.
  await page.getByRole("button", { name: "Start payment run" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Payment run" })).toBeVisible();
  for (const step of ["Generate the bank file", "Post to the ledger", "Settle the invoices", "Complete the run"]) {
    await page.getByRole("button", { name: step }).click();
  }
  await expect(page.getByText("This payment run is complete")).toBeVisible();

  // Remittance advice of one vendor: the invoices paid and the amount.
  await page.getByRole("button", { name: /^Remittance advice for Ironbridge Foundry/ }).click();
  const advice = page.getByRole("table", { name: /^Invoices paid with payment / });
  await expect(advice.getByText("IBF-88120")).toBeVisible();
  await expect(advice.getByText("18,640.00 USD").first()).toBeVisible();
  expect(await blockingViolations(page)).toEqual([]);
  const csv = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download CSV" }).click();
  expect((await csv).suggestedFilename()).toMatch(/^remittance-advice-.*\.csv$/);
  const pdf = page.waitForEvent("download");
  await page.getByRole("button", { name: "Download PDF" }).click();
  expect((await pdf).suggestedFilename()).toMatch(/^remittance-advice-.*\.pdf$/);

  // The paid proposal links back to its run.
  await page.getByRole("link", { name: "Payment proposal" }).click();
  await expect(page.getByRole("list", { name: "Proposal totals" }).getByText("Paid")).toBeVisible();
  await page.getByRole("link", { name: "Open payment run" }).click();
  await expect(page.getByText("This payment run is complete")).toBeVisible();
  expect(await unhandled(page)).toEqual([]);
});

test("a vendor is put on payment hold with a reason and released again", async ({ page }) => {
  await page.goto("./payables/open-items");
  await expect(page.getByRole("heading", { level: 1, name: "Vendor open items" })).toBeVisible();
  await page.getByRole("button", { name: "Hold payments to a vendor" }).click();
  const dialog = page.getByRole("dialog", { name: "Hold payments to a vendor" });
  expect(await blockingViolations(page)).toEqual([]);
  await dialog.getByRole("combobox", { name: "Vendor" }).fill("Volta");
  await dialog.getByRole("option", { name: /Volta Motor Works/ }).click();
  await dialog.getByLabel("Reason for the hold").fill("Bank details under review");
  await dialog.getByRole("button", { name: "Put vendor on hold" }).click();

  await expect(page.getByText(/^Payments to Volta Motor Works.* are on hold/)).toBeVisible();
  const held = page.getByRole("table", { name: "Vendors on payment hold" });
  await expect(held.getByText("Bank details under review")).toBeVisible();
  await expect(page.getByText("Vendor on hold: Bank details under review")).toBeVisible();
  expect(await blockingViolations(page)).toEqual([]);

  await held.getByRole("button", { name: /^Release hold on Volta Motor Works/ }).click();
  await expect(page.getByText(/^The hold on Volta Motor Works.* is released/)).toBeVisible();
  await expect(held).toHaveCount(0);
  expect(await unhandled(page)).toEqual([]);
});
