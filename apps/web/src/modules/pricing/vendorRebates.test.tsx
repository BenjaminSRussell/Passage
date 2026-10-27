import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RebateAgreement, VendorClaim, VendorClaimPreview } from "./api/client";
import { emptyRebate, withParty } from "./NewRebatePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { claimTotals, validateClaimFilter } from "./VendorClaimsPage";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const VA = "0190f0a0-0000-7000-8000-000000000701";
const V1 = "0190f0a0-0000-7000-8000-000000000801";
const RC1 = "0190f0a0-0000-7000-8000-000000000901";

const billBack: RebateAgreement = {
  id: VA,
  legalEntityId: LE,
  name: "Supplier bill-back",
  partyRole: "VENDOR",
  vendorId: V1,
  basis: "NET_AMOUNT",
  period: "MONTH",
  tiers: [{ from: "0", ratePct: "3", amountPerUnit: "0" }],
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  accrualAccount: "2160",
  expenseAccount: "5100",
  status: "ACTIVE",
};

function claim(patch: Partial<VendorClaim> = {}): VendorClaim {
  return {
    id: RC1,
    number: "RC-000001",
    legalEntityId: LE,
    agreementId: VA,
    vendorId: V1,
    periodId: "2027-03",
    accruedTotal: "600",
    priorClaimed: "0",
    amount: "600",
    currency: "USD",
    settlementMode: "CREDIT_NOTE",
    status: "OPEN",
    version: 1,
    createdBy: "u1",
    createdAt: "2027-04-01T08:00:00Z",
    ...patch,
  };
}

function preview(patch: Partial<VendorClaimPreview> = {}): VendorClaimPreview {
  return { agreementId: VA, vendorId: V1, periodId: "2027-03", accrued: "600", priorClaimed: "0", amount: "600", currency: "USD", settlementMode: "CREDIT_NOTE", claims: 0, ...patch };
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Vendor rebate agreements", () => {
  it("creates a vendor rebate on 2160 / 5100 with a vendor id", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/rebates/agreements")) return json(201, billBack);
      if (url.pathname.endsWith(`/rebates/agreements/${VA}`)) return json(200, billBack);
      if (url.pathname.endsWith("/rebates/accruals") || url.pathname.endsWith("/rebates/vendor-claims")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/pricing/rebates/new");
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "VENDOR" } });
    expect(screen.queryByLabelText("Rebates")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Receivable account")).toHaveValue("2160");
    expect(screen.getByLabelText("Income account")).toHaveValue("5100");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fill("Legal entity id", LE);
    fill("Agreement name", "Supplier bill-back");
    fill("Vendor id", V1);
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fill("Rate %", "3");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("heading", { name: "Supplier bill-back" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toMatchObject({ partyRole: "VENDOR", vendorId: V1, accrualAccount: "2160", expenseAccount: "5100" });
    expect(post?.body).not.toHaveProperty("customerId");
  });

  it("keeps accounts the user typed when the type changes", () => {
    const typed = { ...emptyRebate, accrualAccount: "2199" };
    expect(withParty(typed, "VENDOR")).toMatchObject({ party: "VENDOR", accrualAccount: "2199", expenseAccount: "5100" });
    expect(withParty(withParty(emptyRebate, "VENDOR"), "CUSTOMER")).toMatchObject({ accrualAccount: "2150", expenseAccount: "4150" });
  });

  it("filters the list by type", async () => {
    server.handler = (url) => (url.pathname.endsWith("/rebates/agreements") ? json(200, { items: url.searchParams.get("partyRole") === "VENDOR" ? [billBack] : [] }) : undefined);
    renderAt("/pricing/rebates");
    expect(await screen.findByText("No rebate agreements")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "VENDOR" } });
    const table = await findTable({ name: "Rebate agreements" });
    await waitFor(() => expect(table).toHaveTextContent("Supplier bill-back"));
    expect(table).toHaveTextContent("Vendor rebate");
    expect(table).toHaveTextContent(`Vendor ${V1}`);
    expect(table).toHaveTextContent("Vendor-invoiced net amount");
    expect(server.calls.at(-1)?.url.searchParams.get("partyRole")).toBe("VENDOR");
  });
});

describe("Vendor claims on the agreement", () => {
  // AC-26.11 in the screen: the preview of the period shows 600.00, and the claim is created from it.
  it("previews a period, creates the claim with an Idempotency-Key and lists it", async () => {
    let created = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/rebates/agreements/${VA}`)) return json(200, billBack);
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      if (url.pathname.endsWith(`/rebates/agreements/${VA}/claim-preview`)) return json(200, created ? preview({ priorClaimed: "600", amount: "0", claims: 1 }) : preview());
      if (init.method === "POST" && url.pathname.endsWith("/rebates/vendor-claims")) {
        created = true;
        return json(201, claim({ settlementMode: "PAYMENT_DEDUCTION" }));
      }
      if (url.pathname.endsWith("/rebates/vendor-claims")) return json(200, { items: created ? [claim({ settlementMode: "PAYMENT_DEDUCTION" })] : [] });
      return undefined;
    };
    const { container } = renderAt(`/pricing/rebates/${VA}`);
    expect(await screen.findByRole("heading", { name: "Supplier bill-back" })).toBeInTheDocument();
    expect(screen.getByText("Dr 2160 / Cr 5100")).toBeInTheDocument();
    expect(await screen.findByText("No claims yet")).toBeInTheDocument();

    // The claim period is the second Period field on the page (the first filters the accruals).
    const claimPeriod = screen.getAllByLabelText("Period").at(-1)!;
    fireEvent.change(claimPeriod, { target: { value: "March" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    expect(await screen.findByText("Use a month such as 2027-03.")).toBeInTheDocument();

    fireEvent.change(claimPeriod, { target: { value: "2027-03" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    const tiles = await screen.findByRole("list", { name: "Claim preview for 2027-03" });
    expect(tiles).toHaveTextContent("Accrued in the period 600 USD");
    expect(tiles).toHaveTextContent("To claim now 600 USD");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Settle by"), { target: { value: "PAYMENT_DEDUCTION" } });
    fireEvent.click(screen.getByRole("button", { name: "Create claim for 600 USD" }));
    expect(await screen.findByText("Claim RC-000001 created")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({ agreementId: VA, period: "2027-03", settlementMode: "PAYMENT_DEDUCTION" });
    expect(await screen.findByText("The period is fully claimed.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Create claim/ })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("table", { name: "Vendor claims of this agreement" })).toHaveTextContent("RC-000001"));
  });

  it("explains an over-claimed period and shows a refusal", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/rebates/agreements/${VA}`)) return json(200, billBack);
      if (url.pathname.endsWith("/rebates/accruals") || (init.method !== "POST" && url.pathname.endsWith("/rebates/vendor-claims"))) return json(200, { items: [] });
      if (url.pathname.endsWith("/claim-preview")) return json(200, preview({ accrued: "450", priorClaimed: "600", amount: "-150", settlementMode: "VENDOR_INVOICE", claims: 1 }));
      if (init.method === "POST") return json(409, { code: "PRC-0015", detail: "vendor rebate claim already processed" });
      return undefined;
    };
    renderAt(`/pricing/rebates/${VA}`);
    await screen.findByRole("heading", { name: "Supplier bill-back" });
    const periods = screen.getAllByLabelText("Period");
    fireEvent.change(periods[periods.length - 1], { target: { value: "2027-03" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview claim" }));
    expect(await screen.findByText("The period was over-claimed: this claim creates a vendor invoice for the difference.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Settle by")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create claim for -150 USD" }));
    expect(await findAlert()).toHaveTextContent("vendor rebate claim already processed (PRC-0015)");
  });

  it("does not offer claims on a customer agreement", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(`/rebates/agreements/${VA}`)) return json(200, { ...billBack, partyRole: "CUSTOMER", vendorId: undefined, customerId: V1 });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/rebates/${VA}`);
    await screen.findByRole("heading", { name: "Supplier bill-back" });
    expect(screen.queryByRole("button", { name: "Preview claim" })).not.toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/rebates/vendor-claims"))).toBe(false);
  });
});

describe("Vendor rebate claims inquiry", () => {
  it("lists claims with exact totals, filters them and opens one", async () => {
    const rows = [claim(), claim({ id: "rc-2", number: "RC-000002", amount: "150.1", status: "APPROVED" }), claim({ id: "rc-3", number: "RC-000003", amount: "-150", settlementMode: "VENDOR_INVOICE" })];
    server.handler = (url) => {
      if (url.pathname.endsWith("/rebates/vendor-claims")) return json(200, { items: url.searchParams.get("status") === "APPROVED" ? [rows[1]] : rows });
      if (url.pathname.endsWith("/rebates/vendor-claims/rc-2")) return json(200, rows[1]);
      return undefined;
    };
    const { container } = renderAt("/pricing/rebates/claims");
    const table = await findTable({ name: "Vendor rebate claims" });
    await waitFor(() => expect(table).toHaveTextContent("RC-000003"));
    expect(table).toHaveTextContent("Vendor invoice (over-accrued)");
    const totals = screen.getByRole("list", { name: "Claim totals" });
    expect(totals).toHaveTextContent("Claimed 600.1 USD");
    expect(totals).toHaveTextContent("Waiting for approval 2");
    expect(pageViolations(container)).toEqual([]);

    fill("Vendor id", "V1");
    fireEvent.click(screen.getByRole("button", { name: "Show claims" }));
    expect(await screen.findByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    fill("Vendor id", V1);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "APPROVED" } });
    fireEvent.click(screen.getByRole("button", { name: "Show claims" }));
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("APPROVED"));
    expect(server.calls.at(-1)?.url.searchParams.get("vendorId")).toBe(V1);
    await waitFor(() => expect(table).not.toHaveTextContent("RC-000003"));

    fireEvent.click(within(table).getByRole("button", { name: "RC-000002" }));
    expect(await screen.findByRole("heading", { name: "Claim RC-000002" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve claim" })).not.toBeInTheDocument();
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/rebates/claims");
    expect(await screen.findByText("No vendor rebate claims")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.rebate.view" });
    renderAt("/pricing/rebates/claims");
    expect(await findAlert()).toHaveTextContent("missing duty pricing.rebate.view (AUTH-0003)");
  });

  it("checks filters and sums per currency", () => {
    expect(validateClaimFilter({ vendorId: "", period: "2027-Q1", status: "" })).toEqual({});
    expect(validateClaimFilter({ vendorId: "", period: "Q1", status: "" }).period).toBeDefined();
    expect(claimTotals([{ amount: "0.1", currency: "USD" }, { amount: "0.2", currency: "USD" }, { amount: "-5", currency: "EUR" }])).toBe("0.3 USD · -5 EUR");
  });
});

describe("Vendor rebate claim detail", () => {
  it("approves an open claim at its version with an Idempotency-Key", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/vendor-claims/${RC1}:approve`)) {
        return json(200, claim({ status: "APPROVED", version: 2, approvedBy: "u2", approvedAt: "2027-04-02T09:00:00Z" }));
      }
      if (url.pathname.endsWith(`/rebates/vendor-claims/${RC1}`)) return json(200, claim());
      return undefined;
    };
    const { container } = renderAt(`/pricing/rebates/claims/${RC1}`);
    expect(await screen.findByRole("heading", { name: "Claim RC-000001" })).toBeInTheDocument();
    expect(screen.getByText("Vendor credit note")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Approve claim" }));
    expect(await screen.findByText("The claim is approved. Accounts payable settles it by vendor credit note.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({ version: 1 });
    expect(screen.getByText("2027-04-02 by u2")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve claim" })).not.toBeInTheDocument();
  });

  it("shows the four-eyes refusal and a load error", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(403, { code: "PRC-0046", detail: "the user who created claim RC-000001 cannot approve it" });
      if (url.pathname.endsWith(`/rebates/vendor-claims/${RC1}`)) return json(200, claim());
      return undefined;
    };
    const first = renderAt(`/pricing/rebates/claims/${RC1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve claim" }));
    expect(await findAlert()).toHaveTextContent("cannot approve it (PRC-0046)");
    first.unmount();
    server.handler = () => json(404, { code: "PRC-0044", detail: `no vendor rebate claim ${RC1}` });
    renderAt(`/pricing/rebates/claims/${RC1}`);
    expect(await findAlert()).toHaveTextContent("PRC-0044");
  });
});
