import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FreeGoodsRule, ResolveResponse } from "./api/client";
import { describeOffer, describeWho, freeGoodsState } from "./freeGoodsFormat";
import { addDays, today } from "./format";
import { emptyFreeGoods, toFreeGoodsInput, validateFreeGoods } from "./NewFreeGoodsPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const FG = "0190f0a0-0000-7000-8000-000000000f01";
const day = today();

const rule: FreeGoodsRule = {
  id: FG,
  legalEntityId: LE,
  customerKind: "all",
  customerKey: "",
  productId: "BOLT",
  unit: "EA",
  kind: "INCLUSIVE",
  calcRule: "WHOLE_UNITS",
  everyQuantity: "10",
  freeQuantity: "1",
  minQuantity: "0",
  validFrom: addDays(day, -5),
  validTo: addDays(day, 20),
  withdrawn: false,
  version: 1,
};

function fill(label: string, value: string) {
  const field = screen.queryByLabelText(label) ?? screen.getByLabelText(new RegExp(`^${label}`));
  fireEvent.change(field, { target: { value } });
}

describe("free goods formatting", () => {
  it("puts withdrawn before the validity and treats both ends as inclusive", () => {
    const r = { withdrawn: false, validFrom: "2026-03-01", validTo: "2026-03-31" };
    expect(freeGoodsState(r, "2026-02-28")).toBe("scheduled");
    expect(freeGoodsState(r, "2026-03-31")).toBe("live");
    expect(freeGoodsState(r, "2026-04-01")).toBe("ended");
    expect(freeGoodsState({ ...r, withdrawn: true }, "2026-03-15")).toBe("withdrawn");
  });

  it("describes the offer and the customers", () => {
    expect(describeOffer(rule)).toBe("1 free within every 10 EA");
    expect(describeOffer({ ...rule, kind: "EXCLUSIVE", minQuantity: "20" })).toBe("1 free per 10 EA from 20");
    expect(describeWho(rule)).toBe("All customers");
    expect(describeWho({ customerKind: "customer_group", customerKey: "RETAIL" })).toBe("Group RETAIL");
    expect(describeWho({ customerKind: "customer", customerKey: "C1" })).toBe("C1");
  });
});

describe("Free goods rules", () => {
  it("lists rules with their status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/free-goods-rules")) return json(200, { items: [rule, { ...rule, id: "r-2", productId: "NUT", withdrawn: true }] });
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, rule);
      return undefined;
    };
    const { container } = renderAt("/pricing/free-goods");
    const table = await findTable({ name: "Free goods rules" });
    await waitFor(() => expect(table).toHaveTextContent("BOLT"));
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("1 free within every 10 EA");
    expect(rows[1]).toHaveTextContent("Live");
    expect(rows[2]).toHaveTextContent("Withdrawn");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "BOLT" }));
    expect(await screen.findByRole("heading", { name: "BOLT: 1 free within every 10 EA" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state with a retry", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/free-goods");
    expect(await screen.findByText("No free goods rules")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" });
    renderAt("/pricing/free-goods");
    expect(await findAlert()).toHaveTextContent("(AUTH-0003)");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("New free goods rule", () => {
  const ok = { ...emptyFreeGoods, legalEntityId: LE, productId: "BOLT", everyQuantity: "10", freeQuantity: "1", validFrom: "2026-01-01", validTo: "2026-12-31" };

  it("validates like the service", () => {
    expect(validateFreeGoods(ok)).toEqual({});
    expect(Object.keys(validateFreeGoods({ ...ok, freeQuantity: "10" })).sort()).toEqual(["freeQuantity"]);
    expect(validateFreeGoods({ ...ok, kind: "EXCLUSIVE", freeQuantity: "10" })).toEqual({});
    expect(
      Object.keys(validateFreeGoods({ ...ok, legalEntityId: "x", customerKind: "customer", customerKey: "", everyQuantity: "0", validTo: "2025-01-01", minQuantity: "-1" })).sort(),
    ).toEqual(["customerKey", "everyQuantity", "legalEntityId", "minQuantity", "validTo"]);
    expect(toFreeGoodsInput({ ...ok, productId: " BOLT ", customerKind: "all", customerKey: "ignored", minQuantity: "" })).toMatchObject({ productId: "BOLT", customerKey: "", minQuantity: "0" });
  });

  it("checks the form before calling the service", async () => {
    const { container } = renderAt("/pricing/free-goods/new");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findAllByText("Enter a quantity above 0.")).not.toHaveLength(0);
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a rule with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/free-goods-rules")) return json(201, rule);
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, rule);
      return undefined;
    };
    renderAt("/pricing/free-goods/new");
    fill("Legal entity id", LE);
    fill("Product", "BOLT");
    fill("For every quantity ordered", "10");
    fill("Free quantity", "1");
    fill("Valid from", "2026-01-01");
    fill("Valid to", "2026-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByRole("heading", { name: "BOLT: 1 free within every 10 EA" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      legalEntityId: LE,
      customerKind: "all",
      customerKey: "",
      productId: "BOLT",
      unit: "EA",
      kind: "INCLUSIVE",
      calcRule: "WHOLE_UNITS",
      everyQuantity: "10",
      freeQuantity: "1",
      minQuantity: "0",
      validFrom: "2026-01-01",
      validTo: "2026-12-31",
    });
    expect(idempotencyKey(post)).toBeTruthy();
  });

  it("shows an overlapping rule as a form failure", async () => {
    server.handler = () => json(409, { code: "PRC-0002", detail: "overlapping free goods rule x" });
    renderAt("/pricing/free-goods/new");
    fill("Legal entity id", LE);
    fill("Product", "BOLT");
    fill("For every quantity ordered", "10");
    fill("Free quantity", "1");
    fill("Valid from", "2026-01-01");
    fill("Valid to", "2026-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await findAlert()).toHaveTextContent("(PRC-0002)");
  });
});

describe("Free goods rule detail", () => {
  it("withdraws the rule at its version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/free-goods-rules/${FG}:withdraw`)) return json(200, { ...rule, withdrawn: true, version: 2 });
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, rule);
      return undefined;
    };
    const { container } = renderAt(`/pricing/free-goods/${FG}`);
    expect(await screen.findByRole("heading", { name: "BOLT: 1 free within every 10 EA" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Withdraw rule" }));
    expect(await screen.findByText(/This rule is withdrawn/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("leaves the next price resolution");
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ version: 1 });
    expect(idempotencyKey(post)).toBeTruthy();
  });

  it("reports a refused withdraw and keeps the page", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "free goods rule version does not match" });
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, { ...rule, version: 4 });
      return undefined;
    };
    renderAt(`/pricing/free-goods/${FG}`);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw rule" }));
    expect(await findAlert()).toHaveTextContent("(conflict_version)");
    expect(server.calls.find((c) => c.method === "POST")!.body).toEqual({ version: 4 });
  });

  it("shows the error state when the rule is unknown", async () => {
    server.handler = () => json(404, { code: "PRC-0047", detail: "no free goods rule x" });
    renderAt(`/pricing/free-goods/${FG}`);
    expect(await findAlert()).toHaveTextContent("(PRC-0047)");
  });
});

describe("Simulation with free goods", () => {
  it("shows the proposed zero-priced lines", async () => {
    const resolved: ResolveResponse = {
      currency: "USD",
      lines: [{ lineNo: 1, unit: "EA", quantity: "25", unitPrice: "9", netUnitPrice: "9", lineNet: "207", applied: [], trace: [] }],
      subtotal: "207",
      totalDiscount: "0",
      surcharge: "0",
      orderNet: "207",
      applied: [],
      explainRef: "sha256:" + "a".repeat(64),
      freeGoods: [{ lineNo: 1, productId: "BOLT", unit: "EA", ruleId: FG, kind: "INCLUSIVE", orderedQuantity: "25", pricedQuantity: "23", freeQuantity: "2" }],
    };
    server.handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/resolve") ? json(200, resolved) : undefined);
    renderAt("/pricing/simulate");
    fill("Legal entity id", LE);
    fill("Customer", "C1");
    fill("Product", "BOLT");
    fill("Quantity", "25");
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    const table = await findTable({ name: "Free goods proposals" });
    expect(table).toHaveTextContent("23 EA");
    expect(table).toHaveTextContent("2 EA");
  });
});
