import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ModifyContractResult, RevenueContract } from "./api/client";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const CONTRACT = "0190f0a0-0000-7000-8000-000000000c01";
const SUPPORT = "0190f0a0-0000-7000-8000-000000000b03";
const PRODUCT = "0190f0a0-0000-7000-8000-0000000000a1";

const contract: RevenueContract = {
  id: CONTRACT,
  legalEntityId: "0190f0a0-0000-7000-8000-000000000001",
  orderId: "0190f0a0-0000-7000-8000-000000000003",
  customerId: "0190f0a0-0000-7000-8000-000000000002",
  currency: "USD",
  transactionPrice: "1200.00",
  recognized: "300.00",
  remaining: "900.00",
  residualRule: "LARGEST",
  ratableBasis: "MONTHLY",
  contractDate: "2027-01-01",
  status: "OPEN",
  version: 3,
  warnings: [],
  obligations: [
    {
      id: SUPPORT,
      lineNo: 1,
      productId: PRODUCT,
      ssp: "1200.00",
      sspSource: "EXPLICIT",
      allocated: "1200.00",
      recognized: "300.00",
      method: "RATABLE",
      termStart: "2027-01-01",
      termEnd: "2027-12-31",
      schedule: [{ id: "l1", seq: 1, date: "2027-01-31", amount: "100.00", status: "RECOGNIZED", recognizedOn: "2027-01-31" }],
    },
  ],
};

const modified: ModifyContractResult = {
  ...contract,
  transactionPrice: "300.00",
  version: 4,
  modification: {
    id: "0190f0a0-0000-7000-8000-000000000d01",
    type: "PROSPECTIVE",
    effectiveDate: "2027-03-31",
    priceBefore: "1200.00",
    priceAfter: "300.00",
    catchUp: "0.00",
    reversal: "0.00",
    billingStatus: "PENDING",
    obligations: [{ obligationId: SUPPORT, allocatedBefore: "1200.00", allocatedAfter: "300.00", sspBefore: "1200.00", sspAfter: "300.00", catchUp: "0.00", reversal: "0.00", removed: true }],
  },
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label, { exact: false }), { target: { value } });
}

describe("Modify revenue contract", () => {
  it("removes an obligation with If-Match and shows the outcome and the billing request", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}:modify` && init.method === "POST") return json(200, modified);
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) return json(200, contract);
      return undefined;
    };
    const { container } = renderAt(`/billing/revenue-contracts/${CONTRACT}/modify`);
    await screen.findByRole("button", { name: "Modify contract" });
    expect(pageViolations(container)).toEqual([]);
    fill("Effective date", "2027-03-31");
    fill("Price change", "-900");
    fill("Remaining scope kept", "0");
    fireEvent.click(screen.getByRole("button", { name: "Modify contract" }));
    const outcome = await findTable({ name: "New allocation of each obligation" });
    expect(outcome).toHaveTextContent("Removed");
    expect(screen.getByText(/Billing will propose the invoice or credit note/)).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"3"');
    expect(post?.body).toEqual({ type: "PROSPECTIVE", effectiveDate: "2027-03-31", transactionPriceChange: "-900", obligations: [{ obligationId: SUPPORT, remainingScope: "0" }] });
  });

  it("checks the scope before sending and explains a refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "REV-0402", detail: "the new transaction price is below what the point-in-time obligations were billed at" });
      if (url.pathname === `/api/v1/revrec/contracts/${CONTRACT}`) return json(200, contract);
      return undefined;
    };
    renderAt(`/billing/revenue-contracts/${CONTRACT}/modify`);
    await screen.findByRole("button", { name: "Modify contract" });
    fill("Remaining scope kept", "1.5");
    fireEvent.click(screen.getByRole("button", { name: "Modify contract" }));
    expect(await screen.findByText("Enter a share from 0 (remove) to 1 (keep all).")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Remaining scope kept", "0.5");
    fill("Price change", "-2000");
    fireEvent.click(screen.getByRole("button", { name: "Modify contract" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("The new price is below what the delivered goods were billed at"));
  });
});
