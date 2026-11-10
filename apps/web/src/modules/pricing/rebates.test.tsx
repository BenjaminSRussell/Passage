import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { RebateAccrual, RebateAgreement } from "./api/client";
import { sumDecimals } from "./format";
import { emptyRebate, toAgreementInput, validateRebate } from "./NewRebatePage";
import { parseRetroLines } from "./RebateDetailPage";
import { describeProductScope } from "./rebateFormat";
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
  partyRole: "CUSTOMER",
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
  version: 1,
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
    const table = await findTable({ name: "Rebate agreements" });
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
    expect(await findAlert()).toHaveTextContent("missing duty pricing.rebate.view");
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
      partyRole: "CUSTOMER",
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
    expect(await findAlert()).toHaveTextContent("database unavailable");
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

describe("Retroactive recompute", () => {
  it("replays pasted invoice lines, shows the delta and the PRC-0006 notice, and marks retroactive accruals", async () => {
    let retro = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, tiered);
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/agreements/${A1}:retro-recompute`)) {
        retro = true;
        return json(200, {
          agreementId: A1,
          activatedOn: "2027-03-20",
          reaccruedLines: 2,
          replayed: false,
          deltas: [{ periodId: "2027-Q1", target: "1000", posted: "0", delta: "1000" }],
          notice: { code: "PRC-0006", message: "Retroactive activation: 2 posted invoices were re-accrued" },
        });
      }
      if (url.pathname.endsWith("/rebates/accruals")) {
        return json(200, { items: retro ? [accrual({ id: "acc-r", periodId: "2027-Q1", invoiceLineId: "retro:k:2027-Q1", amount: "1000", retroactive: true })] : [] });
      }
      return undefined;
    };
    const { container } = renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("Posted invoice lines", "INV-A, 2027-03-05, 20000\nINV-B, 2027-03-18, 30000, 5");
    fireEvent.click(screen.getByRole("button", { name: "Recompute" }));
    expect(await screen.findByText("Retroactive activation: 2 posted invoices were re-accrued (PRC-0006)")).toBeInTheDocument();
    expect(screen.getByText("Posted 1000 USD across 1 period(s); activated 2027-03-20.")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Retroactive deltas" })).toHaveTextContent("2027-Q1");
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({
      lines: [
        { invoiceLineId: "INV-A", invoiceDate: "2027-03-05", value: "20000", quantity: "0", currency: "USD" },
        { invoiceLineId: "INV-B", invoiceDate: "2027-03-18", value: "30000", quantity: "5", currency: "USD" },
      ],
    });
    await waitFor(() => expect(screen.getByRole("table", { name: "Rebate accruals" })).toHaveTextContent("Retroactive"));
    expect(pageViolations(container)).toEqual([]);
  });

  it("checks the pasted rows and shows a refusal from the service", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, tiered);
      if (init.method === "POST") return json(422, { code: "COM-0001", detail: "agreement is not retroactive" });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("Posted invoice lines", "INV-A, 05/03/2027, 100");
    fireEvent.click(screen.getByRole("button", { name: "Recompute" }));
    expect(await screen.findByText("Row 1: enter the invoice date as YYYY-MM-DD.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Posted invoice lines", "INV-A, 2027-03-05, 100");
    fireEvent.click(screen.getByRole("button", { name: "Recompute" }));
    expect(await findAlert()).toHaveTextContent("agreement is not retroactive");
  });

  it("parses pasted rows", () => {
    expect(parseRetroLines("\n A,2027-01-02,10 \n", "EUR")).toEqual({ lines: [{ invoiceLineId: "A", invoiceDate: "2027-01-02", value: "10", quantity: "0", currency: "EUR" }] });
    expect(parseRetroLines("A,2027-01-02,ten", "EUR").error).toBe("Row 1: value and quantity must be numbers.");
    expect(parseRetroLines(",2027-01-02,1", "EUR").error).toBe("Row 1: enter the invoice line id.");
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

  it("limits an agreement to a product or product group", () => {
    const ok = { ...emptyRebate, legalEntityId: LE, name: "x", customer: C1, validFrom: "2027-01-01", validTo: "2027-12-31", tiers: [{ from: "0", value: "2" }] };
    expect(toAgreementInput(ok)).not.toHaveProperty("productId");
    expect(validateRebate({ ...ok, productScope: "product" }).productKey).toBe("Enter the product id (a UUID).");
    expect(validateRebate({ ...ok, productScope: "group", productKey: "nope" }).productKey).toBe("Enter the product group id (a UUID).");
    expect(validateRebate({ ...ok, productScope: "group", productKey: G1 })).toEqual({});
    expect(toAgreementInput({ ...ok, productScope: "group", productKey: G1 })).toMatchObject({ productGroupId: G1 });
    expect(toAgreementInput({ ...ok, productScope: "product", productKey: G1 })).toMatchObject({ productId: G1 });
    expect(describeProductScope({})).toBe("All products");
    expect(describeProductScope({ productGroupId: G1 })).toBe(`Product group ${G1}`);
  });

  it("parses the product columns of pasted retro rows", () => {
    expect(parseRetroLines(`A,2027-01-02,10,2,${C1},${G1}`, "EUR").lines[0]).toMatchObject({ productId: C1, productGroupId: G1, quantity: "2" });
    expect(parseRetroLines("A,2027-01-02,10,2,nope", "EUR").error).toBe("Row 1: the product and product group ids must be UUIDs.");
    // A four-column row (no product keys) still parses, and so does one that skips the quantity.
    expect(parseRetroLines("A,2027-01-02,10,2", "EUR").lines[0]).toEqual({ invoiceLineId: "A", invoiceDate: "2027-01-02", value: "10", quantity: "2", currency: "EUR" });
    expect(parseRetroLines(`A,2027-01-02,10,,,${G1}`, "EUR").lines[0]).toEqual({ invoiceLineId: "A", invoiceDate: "2027-01-02", value: "10", quantity: "0", currency: "EUR", productGroupId: G1 });
  });

  it("sums decimal strings exactly", () => {
    expect(sumDecimals(["0.1", "0.2"])).toBe("0.3");
    expect(sumDecimals(["30", "120", "-105.1"])).toBe("44.9");
    expect(sumDecimals(["-1.5", "1"])).toBe("-0.5");
    expect(sumDecimals([])).toBe("0");
  });
});

describe("Rebate agreement lifecycle", () => {
  it("revises the end date with the version and idempotency key, then shows the new validity", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/agreements/${A1}:revise`)) return json(200, { ...tiered, name: "Acme renamed", validTo: "2027-06-30", version: 2 });
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, { ...tiered, version: 1 });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("Name", "Acme renamed");
    fill("Valid to", "2027-06-30");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "Acme renamed" })).toBeInTheDocument();
    expect(screen.getByText("The agreement is updated.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ version: 1, name: "Acme renamed", validTo: "2027-06-30" });
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
  });

  it("checks the form before calling the service and shows a refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "PRC-0048", detail: "validTo is before the invoice of 2027-02-10" });
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, { ...tiered, version: 1 });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Change the name, the end date or the product scope.")).toBeInTheDocument();
    fill("Close date", "2030-01-01");
    fireEvent.click(screen.getByRole("button", { name: "Close agreement" }));
    expect(await screen.findByText(`Choose a day from ${tiered.validFrom} to ${tiered.validTo}.`)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Close date", tiered.validFrom);
    fireEvent.click(screen.getByRole("button", { name: "Close agreement" }));
    fireEvent.click(await screen.findByRole("button", { name: "Keep it open" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Close agreement" }));
    fireEvent.click(await screen.findByRole("button", { name: `Close on ${tiered.validFrom}` }));
    expect(await screen.findByText("That date is before an invoice this agreement already accrued")).toBeInTheDocument();
    expect(await findAlert()).toHaveTextContent("PRC-0048");
  });

  it("closes the agreement and shows the closed state", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/agreements/${A1}:close`)) return json(200, { ...tiered, status: "CLOSED", validTo: "2027-05-31", version: 2 });
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, { ...tiered, version: 1 });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("Close date", "2027-05-31");
    fireEvent.click(screen.getByRole("button", { name: "Close agreement" }));
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Invoices dated after 2027-05-31 will no longer accrue");
    fireEvent.click(screen.getByRole("button", { name: "Close on 2027-05-31" }));
    expect(await screen.findByText(/This agreement is closed\. Invoices dated after 2027-05-31/)).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ version: 1, closeDate: "2027-05-31" });
  });

  it("limits the agreement to a product through the revise form and shows the service refusal after an accrual", async () => {
    const P1 = "0190f0a0-0000-7000-8000-000000000701";
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/rebates/agreements/${A1}:revise`)) {
        const body = JSON.parse(String(init.body)) as { productScope?: { productId?: string } };
        if (body.productScope?.productId === P1) return json(200, { ...tiered, productId: P1, version: 2 });
        return json(422, { code: "PRC-0071", detail: "the product scope cannot change after invoices have accrued" });
      }
      if (url.pathname.endsWith(`/rebates/agreements/${A1}`)) return json(200, { ...tiered, version: 1 });
      if (url.pathname.endsWith("/rebates/accruals")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/pricing/rebates/${A1}`);
    await screen.findByRole("heading", { name: "Acme quarterly" });
    fill("Products", "product");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Enter the product id as a UUID.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fill("Product id", P1);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The agreement is updated.")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ version: 1, productScope: { productId: P1 } });
    expect(await screen.findByText(`Product ${P1}`)).toBeInTheDocument();
    fill("Products", "all");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The product scope cannot change after invoices have accrued")).toBeInTheDocument();
  });
});

describe("Rebate agreements by product scope", () => {
  it("filters the list by product and by product group", async () => {
    const P1 = "0190f0a0-0000-7000-8000-000000000701";
    server.handler = (url) => {
      if (url.pathname.endsWith("/rebates/agreements")) {
        const scoped = url.searchParams.get("productId") === P1;
        return json(200, { items: scoped ? [{ ...tiered, productId: P1 }] : [tiered, { ...tiered, id: "a2", name: "Group scoped", productGroupId: G1 }] });
      }
      return undefined;
    };
    renderAt("/pricing/rebates");
    const table = await findTable({ name: "Rebate agreements" });
    await waitFor(() => expect(table).toHaveTextContent("Group scoped"));
    expect(table).toHaveTextContent(`Product group ${G1}`);
    expect(table).toHaveTextContent("All products");
    fill("Limited to", "product");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("Enter the product id as a UUID.")).toBeInTheDocument();
    fill("Product id", P1);
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(table).not.toHaveTextContent("Group scoped"));
    expect(server.calls.some((c) => c.url.searchParams.get("productId") === P1)).toBe(true);
  });

  it("explains an empty scoped list", async () => {
    const G = G1;
    server.handler = (url) => (url.pathname.endsWith("/rebates/agreements") ? json(200, { items: url.searchParams.has("productGroupId") ? [] : [tiered] }) : undefined);
    renderAt("/pricing/rebates");
    await screen.findByRole("table", { name: "Rebate agreements" });
    fill("Limited to", "group");
    fill("Product group id", G);
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("No agreements for that scope")).toBeInTheDocument();
  });
});
