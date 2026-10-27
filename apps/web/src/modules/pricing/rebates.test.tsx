import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RebateAccrual, RebateAgreement } from "./api/client";
import { sumDecimals } from "./format";
import { emptyRebate, toAgreementInput, validateRebate } from "./NewRebatePage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const A1 = "0190f0a0-0000-7000-8000-000000000401";
const C1 = "0190f0a0-0000-7000-8000-000000000501";
const G1 = "0190f0a0-0000-7000-8000-000000000601";

const tiered: RebateAgreement = {
  id: A1,
  legalEntityId: LE,
  name: "Acme quarterly",
  customerId: C1,
  basis: "NET_AMOUNT",
  period: "QUARTER",
  tiers: [
    { from: "0", ratePct: "1", amountPerUnit: "0" },
    { from: "5000", ratePct: "2.5", amountPerUnit: "0" },
  ],
  currency: "USD",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  accrualAccount: "2150",
  expenseAccount: "4150",
  status: "ACTIVE",
};

function accrual(patch: Partial<RebateAccrual>): RebateAccrual {
  return {
    id: "acc-1",
    agreementId: A1,
    periodId: "2027-Q2",
    invoiceLineId: "INV-1/1",
    invoiceDate: "2027-04-02",
    basisValue: "3000",
    basisQuantity: "0",
    amount: "30",
    currency: "USD",
    status: "ACCRUED",
    ...patch,
  };
}

describe("Rebate agreements", () => {
  it("lists agreements with their tiers and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/rebates/agreements")) return json(200, { items: [tiered, { ...tiered, id: "a2", name: "Group monthly", customerId: undefined, customerGroupId: G1 }] });
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, tiered);
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt("/pricing/rebates");
    const table = await screen.findByRole("table", { name: "Rebate agreements" });
    await waitFor(() => expect(table).toHaveTextContent("Acme quarterly"));
    expect(table).toHaveTextContent("from 0: 1% · from 5000: 2.5%");
    expect(table).toHaveTextContent(`Group ${G1}`);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Acme quarterly" }));
    expect(await screen.findByRole("heading", { name: "Acme quarterly" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/rebates");
    expect(await screen.findByText("No rebate agreements")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.rebate.view" });
    renderAt("/pricing/rebates");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty pricing.rebate.view (AUTH-0003)");
  });
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("New rebate agreement", () => {
  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/pricing/rebates/new");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByText("Enter the customer id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a rate above 0 and up to 100.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a tiered agreement with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/rebates/agreements")) return json(201, tiered);
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, tiered);
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/pricing/rebates/new");
    fill("Legal entity id", LE);
    fill("Agreement name", "Acme quarterly");
    fill("Customer id", C1);
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fill("Rate %", "1");
    fireEvent.click(screen.getByRole("button", { name: "Add tier" }));
    fireEvent.change(screen.getAllByLabelText("From")[1], { target: { value: "5000" } });
    fireEvent.change(screen.getAllByLabelText("Rate %")[1], { target: { value: "2.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("heading", { name: "Acme quarterly" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({
      legalEntityId: LE,
      name: "Acme quarterly",
      customerId: C1,
      basis: "NET_AMOUNT",
      period: "QUARTER",
      tiers: [
        { from: "0", ratePct: "1" },
        { from: "5000", ratePct: "2.5" },
      ],
      currency: "USD",
      validFrom: "2027-01-01",
      validTo: "2027-12-31",
      accrualAccount: "2150",
      expenseAccount: "4150",
    });
  });

  it("shows a service error it cannot place on a field", async () => {
    server.handler = () => json(503, { code: "COM-0500", detail: "database unavailable" });
    renderAt("/pricing/rebates/new");
    fill("Legal entity id", LE);
    fill("Agreement name", "x");
    fill("Customer id", C1);
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fill("Rate %", "2");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("database unavailable (COM-0500)");
  });
});

describe("Rebate agreement detail", () => {
  it("shows the tiers and the accruals with an exact total, and filters by period", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, tiered);
      if (url.pathname.endsWith("/rebates/accruals")) {
        const rows = [accrual({}), accrual({ id: "acc-2", invoiceLineId: "INV-2/1", basisValue: "3000", amount: "120" }), accrual({ id: "acc-3", invoiceLineId: "CN-1/1", basisValue: "-1500", amount: "-105.1" })];
        return json(200, { items: url.searchParams.get("period") === "2027-Q3" ? [] : rows });
      }
      return undefined;
    };
    const { container } = renderAt(`/pricing/rebates/${A1}`);
    expect(await screen.findByRole("heading", { name: "Acme quarterly" })).toBeInTheDocument();
    expect(screen.getByText("Dr 4150 / Cr 2150")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Rebate tiers" })).toHaveTextContent("2.5%");
    const totals = screen.getByRole("list", { name: "Accrual totals" });
    await waitFor(() => expect(totals).toHaveTextContent("Accrued, all periods 44.9 USD"));
    expect(totals).toHaveTextContent("Invoice lines 3");
    expect(server.calls.find((c) => c.url.pathname.endsWith("/rebates/accruals"))?.url.searchParams.get("agreementId")).toBe(A1);
    expect(pageViolations(container)).toEqual([]);

    fill("Period", "2027-13");
    fireEvent.click(screen.getByRole("button", { name: "Show accruals" }));
    expect(await screen.findByText("Use a period such as 2027-03, 2027-Q1 or 2027.")).toBeInTheDocument();
    fill("Period", "2027-Q3");
    fireEvent.click(screen.getByRole("button", { name: "Show accruals" }));
    expect(await screen.findByText("No accruals yet")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("period")).toBe("2027-Q3");
  });

  it("shows a load error with a retry", async () => {
    server.handler = () => json(404, { code: "PRC-0035", detail: `no rebate agreement ${A1}` });
    renderAt(`/pricing/rebates/${A1}`);
    expect(await screen.findAllByRole("alert")).not.toHaveLength(0);
    expect(screen.getAllByRole("alert")[0]).toHaveTextContent("PRC-0035");
  });
});

describe("rebate form rules", () => {
  it("mirrors the service rules for tiers and scope", () => {
    const ok = { ...emptyRebate, legalEntityId: LE, name: "x", customer: C1, validFrom: "2027-01-01", validTo: "2027-12-31", tiers: [{ from: "0", value: "2" }] };
    expect(validateRebate(ok)).toEqual({});
    expect(validateRebate({ ...ok, tiers: [{ from: "0", value: "101" }] })["tiers[0].value"]).toBeDefined();
    expect(validateRebate({ ...ok, basis: "QUANTITY", tiers: [{ from: "0", value: "150" }] })).toEqual({});
    expect(validateRebate({ ...ok, tiers: [{ from: "100", value: "1" }, { from: "50", value: "2" }] })["tiers[1].from"]).toMatch(/increase/);
    expect(validateRebate({ ...ok, expenseAccount: "41" }).expenseAccount).toBeDefined();
    expect(toAgreementInput({ ...ok, scope: "group", customer: G1, basis: "QUANTITY" })).toMatchObject({ customerGroupId: G1, tiers: [{ from: "0", amountPerUnit: "2" }] });
    expect(toAgreementInput(ok)).not.toHaveProperty("customerGroupId");
  });

  it("sums decimal strings exactly", () => {
    expect(sumDecimals(["0.1", "0.2"])).toBe("0.3");
    expect(sumDecimals(["30", "120", "-105.1"])).toBe("44.9");
    expect(sumDecimals(["-1.5", "1"])).toBe("-0.5");
    expect(sumDecimals([])).toBe("0");
  });
});
