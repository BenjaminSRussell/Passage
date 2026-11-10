import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ConditionRecord, ResolveResponse } from "./api/client";
import { addDays, today } from "./format";
import pricing from "./module";
import { emptyAgreement, validateAgreement } from "./NewAgreementPage";
import { validateSimulation } from "./SimulatePage";

const LE = "0190f0a0-0000-7000-8000-000000000001";
const R1 = "0190f0a0-0000-7000-8000-000000000201";
const R2 = "0190f0a0-0000-7000-8000-000000000202";
const R3 = "0190f0a0-0000-7000-8000-000000000203";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
function pageViolations(container: HTMLElement) {
  return checkAxeAA(container).filter((v) => !shellRules.has(v.id));
}

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

function json(status: number, body: unknown): Reply {
  return { status, body };
}

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {pricing.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

function idempotencyKey(call: { init: RequestInit }): string | null {
  return new Headers(call.init.headers).get("Idempotency-Key");
}

function record(patch: Partial<ConditionRecord>): ConditionRecord {
  return {
    id: R1,
    legalEntityId: LE,
    type: "PRICE",
    customerKind: "customer",
    customerKey: "C1",
    productKind: "product",
    productKey: "BOLT",
    variant: "",
    currency: "USD",
    unit: "EA",
    fromQuantity: "0",
    amount: "95",
    discPct1: "0",
    discPct2: "0",
    validFrom: "2026-01-01",
    validTo: "2027-12-31",
    searchNext: false,
    exclusive: false,
    status: "ACTIVE",
    version: 2,
    ...patch,
  };
}

const day = today();
const active = [
  record({ id: R1, validTo: addDays(day, 10) }),
  record({ id: R2, type: "LINE_DISC", customerKind: "customer_group", customerKey: "PG-A", productKind: "all", productKey: "", amount: "0", discPct1: "10", discPct2: "5", validTo: addDays(day, 90) }),
];
const draft = record({ id: R3, status: "DRAFT", version: 1, productKey: "SHIRT", currency: "EUR", fromQuantity: "500", amount: "9" });

const tiles = {
  asOf: day,
  month: day.slice(0, 7),
  activeRecords: 140,
  draftRecords: 1,
  expiringIn30Days: 3,
  journalsToPost: 3,
  liveCampaigns: 1,
  claimsAwaitingApproval: 2,
  accrualsThisMonth: [
    { partyRole: "CUSTOMER", currency: "USD", amount: "200" },
    { partyRole: "CUSTOMER", currency: "EUR", amount: "12.5" },
    { partyRole: "VENDOR", currency: "USD", amount: "600" },
  ],
};

describe("Pricing workspace", () => {
  it("shows the service's exact tile counts and accruals per currency, and lists agreements expiring within 30 days", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/workspace")) return json(200, tiles);
      if (url.pathname.endsWith("/condition-records")) {
        const from = url.searchParams.get("validToFrom") ?? "";
        const to = url.searchParams.get("validToTo") ?? "9999-12-31";
        return json(200, { items: active.filter((r) => r.validTo >= from && r.validTo <= to) });
      }
      return undefined;
    };
    const { container } = renderAt("/pricing");
    expect(screen.getByRole("heading", { name: "Pricing workspace" })).toBeInTheDocument();
    const list = screen.getByRole("list", { name: "Pricing tiles" });
    await waitFor(() => expect(list).toHaveTextContent("Active agreements 140"));
    expect(list).toHaveTextContent("Drafts to activate 1");
    expect(list).toHaveTextContent("Expiring in 30 days 3");
    expect(list).toHaveTextContent("Journals to post 3");
    expect(list).toHaveTextContent("Live campaigns 1");
    expect(list).toHaveTextContent("Vendor claims to approve 2");
    expect(list).toHaveTextContent("Customer rebates accrued this month 200 USD · 12.5 EUR");
    expect(list).toHaveTextContent("Vendor rebates accrued this month 600 USD");
    const table = await findTable({ name: "Expiring agreements" });
    expect(within(table).getByText("BOLT")).toBeInTheDocument();
    expect(within(table).queryByText("All products")).toBeNull();
    // Fewer rows than a page: the list is complete, so no "first 100" note.
    expect(screen.queryByText(/agreements that end soonest/)).toBeNull();
    const workspaceCall = calls.find((c) => c.url.pathname === "/api/v1/pricing/workspace");
    expect(workspaceCall?.url.searchParams.get("asOf")).toBe(day);
    const listCall = calls.find((c) => c.url.pathname.endsWith("/condition-records"))?.url.searchParams;
    expect(listCall?.get("status")).toBe("ACTIVE");
    expect(listCall?.get("validToFrom")).toBe(day);
    expect(listCall?.get("validToTo")).toBe(addDays(day, 30));
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the expiring list is one page of a longer count", async () => {
    const page = Array.from({ length: 100 }, (_, i) =>
      record({ id: `0190f0a0-0000-7000-8000-${String(1000 + i).padStart(12, "0")}`, productKey: `P${i}`, validTo: addDays(day, 1 + (i % 30)) }),
    );
    handler = (url) => {
      if (url.pathname.endsWith("/workspace")) return json(200, { ...tiles, expiringIn30Days: 140 });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: page });
      return undefined;
    };
    renderAt("/pricing");
    expect(await screen.findByText(/shows the 100 agreements that end soonest; the tile counts all 140/)).toBeInTheDocument();
  });

  it("shows zero accruals when nothing accrued this month", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/workspace")) return json(200, { ...tiles, activeRecords: 2, accrualsThisMonth: [] });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/pricing");
    const list = screen.getByRole("list", { name: "Pricing tiles" });
    await waitFor(() => expect(list).toHaveTextContent("Customer rebates accrued this month 0"));
    expect(list).toHaveTextContent("Vendor rebates accrued this month 0");
    expect(await screen.findByText("Nothing expires in the next 30 days")).toBeInTheDocument();
    expect(screen.queryByText(/covers the first 100/)).toBeNull();
  });

  it("shows the error with a retry when the service fails", async () => {
    let fail = true;
    handler = (url) => {
      if (url.pathname.endsWith("/workspace")) return fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { ...tiles, accrualsThisMonth: [] });
      if (url.pathname.endsWith("/condition-records")) return json(200, { items: [] });
      return undefined;
    };
    renderAt("/pricing");
    expect(await findAlert()).toHaveTextContent("internal error");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nothing expires in the next 30 days")).toBeInTheDocument();
  });
});

describe("Trade agreements", () => {
  it("lists records from the service, filters by status and opens a record", async () => {
    handler = (url) => {
      if (url.pathname.endsWith("/condition-records")) {
        const status = url.searchParams.get("status");
        return json(200, { items: status === "DRAFT" ? [draft] : status ? active : [...active, draft] });
      }
      if (url.pathname.endsWith(`/condition-records/${R3}`)) return json(200, draft);
      return undefined;
    };
    const { container } = renderAt("/pricing/agreements");
    const table = await findTable({ name: "Trade agreements" });
    await waitFor(() => expect(table).toHaveTextContent("SHIRT"));
    expect(table).toHaveTextContent("Group PG-A");
    expect(table).toHaveTextContent("10% + 5%");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "DRAFT" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("DRAFT"));
    await waitFor(() => expect(within(table).queryByText("BOLT")).toBeNull());

    fireEvent.click(within(table).getByRole("button", { name: "Price" }));
    expect(await screen.findByRole("heading", { name: "Price: SHIRT" })).toBeInTheDocument();
  });

  it("filters by the last valid day and shows the service's refusal of a reversed range", async () => {
    handler = (url) => {
      if (!url.pathname.endsWith("/condition-records")) return undefined;
      const from = url.searchParams.get("validToFrom");
      const to = url.searchParams.get("validToTo");
      if (from && to && to < from) return json(422, { code: "COM-0422", detail: "validation failed", errors: { validToTo: "must not be before validToFrom" } });
      return json(200, { items: to ? active.filter((r) => r.validTo <= to) : active });
    };
    renderAt("/pricing/agreements");
    const table = await findTable({ name: "Trade agreements" });
    await waitFor(() => expect(table).toHaveTextContent("Group PG-A"));
    fireEvent.change(screen.getByLabelText(/Ends on or before/), { target: { value: addDays(day, 30) } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("validToTo")).toBe(addDays(day, 30)));
    await waitFor(() => expect(table).not.toHaveTextContent("Group PG-A"));
    expect(table).toHaveTextContent("BOLT");
    fireEvent.change(screen.getByLabelText(/Ends on or after/), { target: { value: addDays(day, 60) } });
    expect(await findAlert()).toHaveTextContent("Could not load the trade agreements");
  });

  it("shows the empty state when nothing matches", async () => {
    handler = (url) => (url.pathname.endsWith("/condition-records") ? json(200, { items: [] }) : undefined);
    renderAt("/pricing/agreements");
    expect(await screen.findByText("No trade agreements match")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" });
    renderAt("/pricing/agreements");
    expect(await findAlert()).toHaveTextContent("missing duty pricing.view");
    expect(screen.queryByRole("table")).toBeNull();
  });
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("New trade agreement", () => {
  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/pricing/agreements/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the customer or customer group.")).toBeInTheDocument();
    expect(screen.getByText("Enter the price.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves a draft with an Idempotency-Key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/condition-records")) return json(201, draft);
      if (url.pathname.endsWith(`/condition-records/${R3}`)) return json(200, draft);
      return undefined;
    };
    renderAt("/pricing/agreements/new");
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Product", "SHIRT");
    fill("Currency", "EUR");
    fill("From quantity", "500");
    fill("Price", "9.00");
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fireEvent.click(screen.getByLabelText("Search next"));
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByRole("heading", { name: "Price: SHIRT" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({
      ...emptyAgreement,
      legalEntityId: LE,
      customerKey: "C1",
      productKey: "SHIRT",
      currency: "EUR",
      fromQuantity: "500",
      amount: "9.00",
      discPct1: "0",
      discPct2: "0",
      validFrom: "2027-01-01",
      validTo: "2027-12-31",
      searchNext: true,
    });
  });

  it("puts the service's field errors next to the fields", async () => {
    handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { currency: "must be an ISO 4217 code in capitals" } });
    renderAt("/pricing/agreements/new");
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Product", "BOLT");
    fill("Price", "1");
    fill("Valid from", "2027-01-01");
    fill("Valid to", "2027-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("must be an ISO 4217 code in capitals")).toBeInTheDocument();
  });

  it("copies an existing agreement into the form", async () => {
    handler = (url) => (url.pathname.endsWith(`/condition-records/${R2}`) ? json(200, active[1]) : undefined);
    renderAt(`/pricing/agreements/new?copy=${R2}`);
    await waitFor(() => expect(screen.getByLabelText("Customer group")).toHaveValue("PG-A"));
    expect(screen.getByLabelText("Type")).toHaveValue("LINE_DISC");
    expect(screen.getByLabelText("Percentage 1")).toHaveValue("10");
    expect(screen.getByLabelText("Amount off")).toHaveValue("");
  });
});

describe("Trade agreement detail", () => {
  it("activates a draft with its version and an Idempotency-Key", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":activate")) return json(200, { ...draft, status: "ACTIVE", version: 2 });
      if (url.pathname.endsWith(`/condition-records/${R3}`)) return json(200, draft);
      return undefined;
    };
    const { container } = renderAt(`/pricing/agreements/${R3}`);
    expect(await screen.findByRole("heading", { name: "Price: SHIRT" })).toBeInTheDocument();
    expect(screen.getByText("Draft")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Activate" }));
    expect(await screen.findByText(/The agreement is active/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/pricing/condition-records/${R3}:activate`);
    expect(post?.body).toEqual({ version: 1 });
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Withdraw" })).toBeInTheDocument();
  });

  it("names the overlapping agreement when activation is refused", async () => {
    handler = (_url, init) => {
      if (init.method === "POST") return json(409, { code: "PRC-0002", detail: `overlapping active record ${R1} for the same key, currency and unit` });
      return json(200, draft);
    };
    renderAt(`/pricing/agreements/${R3}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Another active agreement overlaps this one");
    expect(alert).toHaveTextContent(R1);
    expect(screen.getByText("Draft")).toBeInTheDocument();
  });

  it("withdraws an active agreement with the reason", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":withdraw")) return json(200, { ...active[0], status: "WITHDRAWN", version: 3 });
      return json(200, active[0]);
    };
    renderAt(`/pricing/agreements/${R1}`);
    fireEvent.change(await screen.findByLabelText("Reason"), { target: { value: "superseded" } });
    fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));
    expect(await screen.findByText(/no longer prices documents/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ version: 2, reason: "superseded" });
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
  });

  it("shows a load error with a retry", async () => {
    handler = () => json(404, { code: "PRC-0030", detail: `no condition record ${R1}` });
    renderAt(`/pricing/agreements/${R1}`);
    expect(await findAlert()).toHaveTextContent("PRC-0030");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

const resolved: ResolveResponse = {
  freeGoods: [],
  currency: "USD",
  lines: [
    {
      lineNo: 1,
      unit: "EA",
      quantity: "3",
      unitPrice: "100",
      netUnitPrice: "85.5",
      lineNet: "256.5",
      applied: [
        { type: "PRICE", recordId: R1, access: 2 },
        { type: "LINE_DISC", recordId: R2, access: 9 },
      ],
      trace: [
        {
          condition: "PRICE",
          selected: R1,
          accesses: [
            { seq: 1, access: "customer x variant", evaluated: true, candidates: [], reason: "no candidate at this access" },
            { seq: 2, access: "customer x product", evaluated: true, candidates: [R1], winner: R1, reason: "winner at this access: search stops" },
          ],
        },
      ],
    },
    { lineNo: 2, unit: "EA", quantity: "1", unitPrice: "0", netUnitPrice: "0", lineNet: "0", applied: [], errorCode: "PRC-0001", errorDetail: "no price for product NUT on 2027-03-10" },
  ],
  subtotal: "256.5",
  totalDiscount: "0",
  surcharge: "0",
  orderNet: "256.5",
  applied: [],
  explainRef: `sha256:${"a".repeat(64)}`,
};

describe("Price simulation", () => {
  it("checks the tuple before calling the service", async () => {
    renderAt("/pricing/simulate");
    fill("Quantity", "0");
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByText("Enter a quantity above 0.")).toBeInTheDocument();
    expect(screen.getByText("Enter the product.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("prices the lines, shows the totals and explains each access", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/resolve") ? json(200, resolved) : undefined);
    const { container } = renderAt("/pricing/simulate");
    expect(screen.getByText("No simulation run yet")).toBeInTheDocument();
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Pricing date", "2027-03-10");
    fill("Product", "BOLT");
    fill("Quantity", "3");
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    fireEvent.change(screen.getAllByLabelText("Product")[1], { target: { value: "NUT" } });
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await screen.findByRole("list", { name: "Simulation totals" })).toHaveTextContent("Order net 256.5 USD");
    const lines = screen.getByRole("table", { name: "Priced lines" });
    expect(lines).toHaveTextContent("Price (access 2), Line discount (access 9)");
    expect(lines).toHaveTextContent("PRC-0001: no price for product NUT on 2027-03-10");
    const explain = screen.getByRole("table", { name: "Explain line 1" });
    expect(explain).toHaveTextContent("winner at this access: search stops");
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.search).toBe("?explain=true");
    expect(post?.body).toEqual({
      legalEntityId: LE,
      customerId: "C1",
      currency: "USD",
      pricingDate: "2027-03-10",
      lines: [
        { productId: "BOLT", unit: "EA", quantity: "3" },
        { productId: "NUT", unit: "EA", quantity: "1" },
      ],
    });
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the service error and keeps the inputs", async () => {
    handler = () => json(422, { code: "PRC-0009", detail: "request exceeds 500 lines" });
    renderAt("/pricing/simulate");
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Product", "BOLT");
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    expect(await findAlert()).toHaveTextContent("request exceeds 500 lines");
    expect(screen.getByLabelText("Product")).toHaveValue("BOLT");
  });
});

describe("form validation", () => {
  it("mirrors the service rules for agreements", () => {
    const ok = { ...emptyAgreement, legalEntityId: LE, customerKey: "C1", productKey: "P1", amount: "10", validFrom: "2027-01-01", validTo: "2027-12-31" };
    expect(validateAgreement(ok)).toEqual({});
    expect(validateAgreement({ ...ok, amount: "-1" }).amount).toMatch(/negative/);
    expect(validateAgreement({ ...ok, discPct1: "101" }).discPct1).toBeDefined();
    expect(validateAgreement({ ...ok, validTo: "2026-12-31" }).validTo).toMatch(/on or after/);
    expect(validateAgreement({ ...ok, currency: "usd" }).currency).toBeDefined();
    expect(validateAgreement({ ...ok, customerKind: "all", customerKey: "" })).toEqual({});
    expect(validateAgreement({ ...ok, type: "LINE_DISC", amount: "" }).amount).toMatch(/amount, a percentage/);
    expect(validateAgreement({ ...ok, type: "LINE_DISC", amount: "", discPct1: "10" })).toEqual({});
    expect(validateAgreement({ ...ok, productKind: "variant" }).variant).toBeDefined();
    expect(validateAgreement({ ...ok, type: "TOTAL_DISC", discPct1: "2" }).productKind).toMatch(/All products/);
    expect(validateAgreement({ ...ok, type: "SURCHARGE", amount: "15" }).productKind).toMatch(/surcharge applies to the whole order/);
    expect(validateAgreement({ ...ok, type: "SURCHARGE", productKind: "all", productKey: "", amount: "15", discPct2: "5" }).discPct2).toMatch(/one percentage/);
    expect(validateAgreement({ ...ok, type: "MULTILINE_DISC", discPct1: "3" }).productKind).toMatch(/Product group/);
    expect(validateAgreement({ ...ok, type: "MULTILINE_DISC", productKind: "product_group", productKey: "PG-A", amount: "", discPct1: "3" })).toEqual({});
    expect(validateAgreement({ ...ok, type: "TOTAL_DISC", discPct1: "2", productKind: "all", productKey: "" })).toEqual({});
  });

  it("mirrors the resolve validation for simulations", () => {
    const header = { legalEntityId: LE, customerId: "C1", customerGroupId: "", currency: "USD", pricingDate: "2027-03-10", salesChannel: "", procedure: "" };
    expect(validateSimulation(header, [{ productId: "P1", productGroupId: "", unit: "EA", quantity: "2.5" }])).toEqual({});
    expect(Object.keys(validateSimulation({ ...header, legalEntityId: "x", currency: "us" }, [{ productId: "", productGroupId: "", unit: "", quantity: "-1" }])).sort()).toEqual(
      ["currency", "legalEntityId", "lines[0].productId", "lines[0].quantity", "lines[0].unit"],
    );
    expect(Object.keys(validateSimulation({ ...header, salesChannel: "web", procedure: "ax std" }, [{ productId: "P1", productGroupId: "", unit: "EA", quantity: "1" }])).sort()).toEqual(["procedure", "salesChannel"]);
  });
});
