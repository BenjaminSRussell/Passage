import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const PROPOSAL = "0195f0a0-0000-7000-8000-000000000051";
const RUN = "0195f0a0-0000-7000-8000-000000000052";
const VENDOR = "0195f0a0-0000-7000-8000-000000000053";

const proposal = (status: string) => ({
  id: PROPOSAL,
  legalEntityId: "00000000-0000-0000-0000-0000000000e1",
  runDate: "2026-11-20",
  runId: "run-1",
  dueBy: "2026-11-30",
  legalEntityCurrency: "USD",
  paymentMethod: "TRANSFER",
  status,
  createdBy: "ana",
  createdAt: "2026-11-10T10:00:00Z",
  totalValued: "968.10",
  groups: [{ id: "g1", vendorId: VENDOR, currency: "USD", paymentMethod: "TRANSFER", gross: "968.10", discount: "0", net: "968.10", valued: "968.10", lines: [] }],
});

test("an approved proposal goes through its payment run to completed", async ({ page }) => {
  let run: Record<string, unknown> = {
    id: RUN,
    legalEntityId: "00000000-0000-0000-0000-0000000000e1",
    proposalId: PROPOSAL,
    status: "RUNNING",
    requestedBy: "marcus",
    createdAt: "2026-11-11T09:00:00Z",
    payments: [{ groupId: "g1", vendorId: VENDOR, currency: "USD", paymentMethod: "TRANSFER", endToEndId: "E2E-1", amount: "968.10", valuedAmount: "968.10" }],
  };
  await page.route("**/api/v1/party/**", (route) => route.fulfill({ status: 404, json: { code: "COM-0404", detail: "none" } }));
  await page.route("**/api/v1/payables/payment-proposals/**", (route) => route.fulfill({ json: proposal("APPROVED") }));
  await page.route("**/api/v1/payables/payment-runs", (route) => route.fulfill({ status: 201, json: run }));
  await page.route("**/api/v1/payables/payment-runs/**", (route) => {
    const url = route.request().url();
    const step = /:(generate-file|post-journal|settle|complete)$/.exec(url)?.[1];
    if (step === "generate-file") run = { ...run, fileGeneratedAt: "2026-11-11T09:01:00Z", fileNumber: "PF-1" };
    if (step === "post-journal") run = { ...run, journalPostedAt: "2026-11-11T09:02:00Z" };
    if (step === "settle") run = { ...run, settledAt: "2026-11-11T09:03:00Z" };
    if (step === "complete") run = { ...run, status: "COMPLETED" };
    return route.fulfill({ json: run });
  });
  const shell = new ShellPage(page);

  await shell.goto(`/payables/proposals/${PROPOSAL}`);
  await page.getByRole("button", { name: "Start payment run" }).click();
  for (const step of ["Generate the bank file", "Post to the ledger", "Settle the invoices", "Complete the run"]) {
    await page.getByRole("button", { name: step }).click();
  }
  await expect(page.getByText("This payment run is complete")).toBeVisible();
});
