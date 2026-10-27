import { afterEach, describe, expect, it, vi } from "vitest";
import { applyPayment, fetchAging, parseAging, ReceivablesApiError } from "./api/client";
import { compareDecimals, fromMicros, money, subtract, sumDecimals, toMicros } from "./format";
import receivables from "./module";
import { buildApply, planFromCandidate } from "./PaymentDetailPage";
import { emptyPayment, toPaymentInput, validatePayment } from "./NewPaymentPage";
import { agingRows } from "./WorkspacePage";

const valid = {
  asOf: "2027-03-31",
  totals: [
    { currency: "USD", bucket: "1-30", amount: "1250.00" },
    { currency: "USD", bucket: "CURRENT", amount: "0.50" },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("receivables module", () => {
  it("registers the workspace, list, create and detail routes with ARView-gated nav", () => {
    expect(receivables.id).toBe("receivables");
    expect(receivables.routes.map((r) => r.path)).toEqual([
      "/receivables",
      "/receivables/open-items",
      "/receivables/payments",
      "/receivables/payments/new",
      "/receivables/payments/:id",
      "/receivables/lockbox",
      "/receivables/lockbox/new",
      "/receivables/bank-statements",
      "/receivables/bank-statements/new",
      "/receivables/worklist",
      "/receivables/disputes",
      "/receivables/disputes/new",
      "/receivables/disputes/:id",
      "/receivables/promises",
      "/receivables/promises/new",
      "/receivables/dunning-runs",
      "/receivables/dunning-runs/new",
      "/receivables/dunning-runs/:id",
      "/receivables/interest-notes",
      "/receivables/interest-notes/new",
      "/receivables/cases",
      "/receivables/cases/new",
      "/receivables/cases/:id",
      "/receivables/balance-confirmations",
      "/receivables/balance-confirmations/new",
      "/receivables/balance-confirmations/:id",
      "/receivables/down-payments",
      "/receivables/down-payments/new",
      "/receivables/down-payments/:id",
      "/receivables/deductions",
      "/receivables/deductions/new",
      "/receivables/deductions/:id",
    ]);
    // Every nav entry points at a registered route; each route has a title for the shell.
    const paths = new Set(receivables.routes.map((r) => r.path));
    expect(receivables.nav.every((n) => paths.has(n.to))).toBe(true);
    expect(receivables.routes.every((r) => typeof r.handle?.title === "string")).toBe(true);
    expect(receivables.nav.find((n) => n.to === "/receivables")?.duty).toBe("ARView");
    expect(receivables.nav.find((n) => n.to === "/receivables/payments/new")?.duty).toBe("ARPaymentPost");
  });
});

describe("parseAging", () => {
  it("accepts a payload that matches the OpenAPI schema", () => {
    expect(parseAging(valid)).toEqual(valid);
  });

  it("rejects amounts that are not decimal strings", () => {
    const bad = { asOf: "2027-03-31", totals: [{ currency: "USD", bucket: "1-30", amount: 1250.5 }] };
    expect(() => parseAging(bad)).toThrow(/amount invalid/);
  });

  it("rejects unknown buckets and currencies", () => {
    expect(() => parseAging({ asOf: "2027-03-31", totals: [{ currency: "USD", bucket: "> 180", amount: "1" }] })).toThrow(/bucket/);
    expect(() => parseAging({ asOf: "2027-03-31", totals: [{ currency: "usd", bucket: "1-30", amount: "1" }] })).toThrow(/currency/);
  });
});

describe("client", () => {
  it("passes asOf through and surfaces problem details with the stable code", async () => {
    const calls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string) => {
        calls.push(input);
        if (calls.length === 1) return new Response(JSON.stringify(valid), { status: 200 });
        return new Response(JSON.stringify({ title: "Unauthorized", status: 401, code: "AUTH-0001", detail: "missing tenant" }), { status: 401 });
      }),
    );
    await expect(fetchAging("2027-03-31")).resolves.toEqual(valid);
    expect(calls[0]).toBe("/api/v1/receivables/aging?asOf=2027-03-31");
    const err = await fetchAging().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ReceivablesApiError);
    expect((err as ReceivablesApiError).code).toBe("AUTH-0001");
  });

  it("sends If-Match with the payment version and the Idempotency-Key on apply", async () => {
    let init: RequestInit = {};
    let url = "";
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: string, i: RequestInit) => {
        url = input;
        init = i;
        return new Response(JSON.stringify({ payment: {}, settlements: [] }), { status: 200 });
      }),
    );
    await applyPayment("p-1", 4, { settlements: [], onAccount: "10.00" }, "key-9");
    const headers = new Headers(init.headers);
    expect(url).toBe("/api/v1/receivables/payments/p-1:apply");
    expect(headers.get("If-Match")).toBe('"4"');
    expect(headers.get("Idempotency-Key")).toBe("key-9");
  });
});

describe("decimal arithmetic", () => {
  it("adds and subtracts without float error", () => {
    expect(sumDecimals(["0.10", "0.20"])).toBe("0.30");
    expect(subtract("1000.00", "20.00")).toBe("980.00");
    expect(subtract("10", "10.000001")).toBe("-0.000001");
    expect(fromMicros(toMicros("1250.5"))).toBe("1250.50");
    expect(compareDecimals("1.00", "1")).toBe(0);
    expect(money("12.5", "EUR")).toBe("12.50 EUR");
  });

  it("rejects text that is not a decimal", () => {
    expect(() => toMicros("1e3")).toThrow(RangeError);
  });
});

describe("aging rows", () => {
  it("fills empty buckets with zero and totals each currency", () => {
    const rows = agingRows({
      asOf: "2027-03-31",
      totals: [
        { currency: "USD", bucket: "1-30", amount: "1250.00" },
        { currency: "USD", bucket: "CURRENT", amount: "0.5" },
        { currency: "EUR", bucket: "OVER-180", amount: "7" },
      ],
    });
    expect(rows.map((r) => r.currency)).toEqual(["EUR", "USD"]);
    expect(rows[1].cells["31-60"]).toBe("0.00");
    expect(rows[1].total).toBe("1250.50");
    expect(rows[0].total).toBe("7.00");
  });
});

describe("record payment form", () => {
  it("flags missing and malformed fields", () => {
    const errors = validatePayment({ ...emptyPayment("2027-03-05"), legalEntityId: "le", amount: "0", currency: "usd", customerId: "x" });
    expect(Object.keys(errors).sort()).toEqual(["amount", "currency", "customerId", "legalEntityId"]);
  });

  it("drops blank remittance lines and sends blanks as null", () => {
    const input = toPaymentInput({
      ...emptyPayment("2027-03-05"),
      legalEntityId: " 0190f0a0-0000-7000-8000-000000000001 ",
      amount: "980.00",
      remittance: [
        { reference: "INV-30001", amount: "" },
        { reference: "", amount: "" },
      ],
    });
    expect(input.customerId).toBeNull();
    expect(input.sourceRef).toBeNull();
    expect(input.legalEntityId).toBe("0190f0a0-0000-7000-8000-000000000001");
    expect(input.remittance).toEqual([{ reference: "INV-30001", amount: "0" }]);
  });
});

describe("apply plan", () => {
  const item = {
    id: "i-1",
    legalEntityId: "le",
    customerId: "c",
    sourceType: "INVOICE",
    documentNumber: "INV-30001",
    documentDate: "2027-03-01",
    dueDate: "2027-03-31",
    currency: "USD",
    amount: "1000.00",
    openAmount: "1000.00",
    cashDiscountDate: "2027-03-11",
    cashDiscountAmount: "20.00",
    dunningLevel: 0,
    dunningBlock: false,
    status: "OPEN",
    version: 1,
  };

  it("takes the cash discount when the matcher scored R3 (AC-30.2)", () => {
    expect(planFromCandidate({ openItemIds: ["i-1"], score: 93, rules: ["R1", "R3", "R6"] }, [item])).toEqual({
      "i-1": { amount: "980.00", cashDiscount: "20.00", writeOff: "" },
    });
    expect(planFromCandidate({ openItemIds: ["i-1"], score: 65, rules: ["R1", "R6"] }, [item])["i-1"].amount).toBe("1000.00");
  });

  it("puts the rest on account and refuses a plan above the unapplied amount", () => {
    expect(buildApply({ "i-1": { amount: "980.00", cashDiscount: "20.00", writeOff: "" } }, "1000.00")).toEqual({
      settlements: [{ openItemId: "i-1", amount: "980.00", cashDiscount: "20.00", writeOff: "0" }],
      onAccount: "20.00",
    });
    expect(buildApply({ "i-1": { amount: "1000.01", cashDiscount: "", writeOff: "" } }, "1000.00")).toHaveProperty("error");
    expect(buildApply({ "i-1": { amount: "abc", cashDiscount: "", writeOff: "" } }, "1000.00")).toHaveProperty("error");
  });
});
