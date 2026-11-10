import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FreeGoodsRule, ResolveResponse } from "./api/client";
import { describeOffer, describeProduct, describeWho, freeGoodsState } from "./freeGoodsFormat";
import { addDays, today } from "./format";
import { validateRevision } from "./FreeGoodsEditCard";
import { emptyFreeGoods, toFreeGoodsInput, validateFreeGoods } from "./NewFreeGoodsPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "22222222-0000-4000-8000-000000000002";
const BOLTID = "33333333-0000-4000-8000-000000000003";
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
    expect(await findAlert()).toHaveTextContent("AUTH-0003");
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
    // Left on "Legal entity default", no calcRule is sent and the pricing parameter applies; a chosen one is sent.
    expect(toFreeGoodsInput(ok).calcRule).toBeUndefined();
    expect(toFreeGoodsInput({ ...ok, calcRule: "PROPORTIONAL" }).calcRule).toBe("PROPORTIONAL");
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
      productKind: "product",
      productId: "BOLT",
      unit: "EA",
      kind: "INCLUSIVE",
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
    expect(await findAlert()).toHaveTextContent("PRC-0002");
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
    expect(await findAlert()).toHaveTextContent("conflict_version");
    expect(server.calls.find((c) => c.method === "POST")!.body).toEqual({ version: 4 });
  });

  const scheduled: FreeGoodsRule = { ...rule, validFrom: addDays(day, 5), validTo: addDays(day, 60) };

  it("revises the offer of a rule that has not started, at the rule's version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/free-goods-rules/${FG}:revise`)) {
        return json(200, { ...scheduled, ...(init.body ? JSON.parse(String(init.body)) : {}), version: 2 });
      }
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, scheduled);
      return undefined;
    };
    const { container } = renderAt(`/pricing/free-goods/${FG}`);
    expect(await screen.findByRole("heading", { name: "BOLT: 1 free within every 10 EA" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.getByLabelText("For every quantity ordered")).toHaveValue("10");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Change at least one term.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("For every quantity ordered"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Free quantity"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("Calculation"), { target: { value: "PROPORTIONAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "BOLT: 2 free within every 5 EA" })).toBeInTheDocument();
    expect(screen.getByText(/Price resolution uses the new terms/)).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      version: 1,
      calcRule: "PROPORTIONAL",
      everyQuantity: "5",
      freeQuantity: "2",
      minQuantity: "0",
      validFrom: scheduled.validFrom,
      validTo: scheduled.validTo,
    });
    expect(idempotencyKey(post)).toBeTruthy();
  });

  it("keeps a started rule's offer and first day, and lets it end from yesterday", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(`/free-goods-rules/${FG}:revise`)) {
        return json(200, { ...rule, ...(init.body ? JSON.parse(String(init.body)) : {}), version: 2 });
      }
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, rule);
      return undefined;
    };
    renderAt(`/pricing/free-goods/${FG}`);
    await screen.findByLabelText("Free quantity");
    for (const label of ["For every quantity ordered", "Free quantity", "Valid from"]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("readonly");
    }
    expect(screen.getByLabelText("Calculation")).toBeDisabled();
    expect(screen.getByLabelText("Valid to")).not.toHaveAttribute("readonly");
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: addDays(day, -3) } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/or later: the days already priced keep the rule/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: addDays(day, -1) } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Price resolution uses the new terms/)).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")!.body).toMatchObject({ everyQuantity: "10", freeQuantity: "1", validFrom: rule.validFrom, validTo: addDays(day, -1) });
  });

  it("checks a revision before calling the service, like the service does", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/free-goods-rules/${FG}`) ? json(200, scheduled) : undefined);
    renderAt(`/pricing/free-goods/${FG}`);
    await screen.findByLabelText("Free quantity");
    fireEvent.change(screen.getByLabelText("Free quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/must give fewer free units/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    const terms = { calcRule: "WHOLE_UNITS" as const, everyQuantity: "10", freeQuantity: "10", minQuantity: "", validFrom: scheduled.validFrom, validTo: scheduled.validTo };
    expect(validateRevision({ ...scheduled, kind: "EXCLUSIVE" }, terms, day)).toEqual({});
    expect(validateRevision(scheduled, { ...terms, everyQuantity: "0", freeQuantity: "x", minQuantity: "-", validFrom: addDays(day, 9), validTo: addDays(day, 8) }, day)).toMatchObject({
      everyQuantity: expect.any(String),
      freeQuantity: expect.any(String),
      minQuantity: expect.any(String),
      validTo: expect.any(String),
    });
    expect(validateRevision(scheduled, { ...terms, freeQuantity: "1", validFrom: addDays(day, -1) }, day)).toEqual({ validFrom: "The first day cannot be in the past." });
    // A started rule: the offer is fixed; an ended rule keeps its last day.
    expect(Object.keys(validateRevision(rule, { ...terms, freeQuantity: "2", validFrom: rule.validFrom, validTo: rule.validTo }, day))).toEqual(["freeQuantity"]);
    const ended = { ...rule, validFrom: addDays(day, -30), validTo: addDays(day, -10) };
    expect(Object.keys(validateRevision(ended, { ...terms, freeQuantity: "1", validFrom: ended.validFrom, validTo: addDays(day, 10) }, day))).toEqual(["validTo"]);
  });

  it("reports a refused revision, such as overlapping dates, and keeps the form", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "PRC-0002", detail: "overlapping free goods rule r-9" });
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, rule);
      return undefined;
    };
    renderAt(`/pricing/free-goods/${FG}`);
    await screen.findByLabelText("Valid to");
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: addDays(day, 90) } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await findAlert()).toHaveTextContent("overlapping free goods rule r-9");
    expect(screen.getByText("Another rule covers those dates")).toBeInTheDocument();
    expect(screen.getByLabelText("Valid to")).toHaveValue(addDays(day, 90));
  });

  it("offers no edit on a withdrawn rule", async () => {
    server.handler = (url) => (url.pathname.endsWith(`/free-goods-rules/${FG}`) ? json(200, { ...rule, withdrawn: true, version: 2 }) : undefined);
    renderAt(`/pricing/free-goods/${FG}`);
    expect(await screen.findByText(/This rule is withdrawn/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("shows the error state when the rule is unknown", async () => {
    server.handler = () => json(404, { code: "PRC-0047", detail: "no free goods rule x" });
    renderAt(`/pricing/free-goods/${FG}`);
    expect(await findAlert()).toHaveTextContent("PRC-0047");
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
      freeGoods: [{ lineNo: 1, productId: BOLTID, unit: "EA", ruleId: FG, kind: "INCLUSIVE", orderedQuantity: "25", pricedQuantity: "23", freeQuantity: "2" }],
    };
    server.handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/resolve") ? json(200, resolved) : undefined);
    renderAt("/pricing/simulate");
    fill("Legal entity", LE);
    fill("Customer", CUST);
    fill("Product", BOLTID);
    fill("Quantity", "25");
    fireEvent.click(screen.getByRole("button", { name: "Run simulation" }));
    const table = await findTable({ name: "Free goods proposals" });
    expect(table).toHaveTextContent("23 EA");
    expect(table).toHaveTextContent("2 EA");
  });
});

describe("Free goods for a product group", () => {
  const group: FreeGoodsRule = { ...rule, productKind: "product_group", productId: "FASTENERS" };

  it("names the group in the list and on the detail page", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/free-goods-rules")) return json(200, { items: [group] });
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, group);
      return undefined;
    };
    expect(describeProduct(group)).toBe("Group FASTENERS");
    expect(describeProduct(rule)).toBe("BOLT");
    const { unmount } = renderAt("/pricing/free-goods");
    expect(await screen.findByText("Group FASTENERS")).toBeInTheDocument();
    unmount();
    renderAt(`/pricing/free-goods/${FG}`);
    expect(await screen.findByRole("heading", { name: "Group FASTENERS: 1 free within every 10 EA" })).toBeInTheDocument();
    expect(screen.getByText("Product group")).toBeInTheDocument();
  });

  it("creates a product group rule", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/free-goods-rules")) return json(201, group);
      if (url.pathname.endsWith(`/free-goods-rules/${FG}`)) return json(200, group);
      return undefined;
    };
    renderAt("/pricing/free-goods/new");
    fill("Legal entity id", LE);
    fireEvent.change(screen.getByLabelText("Applies to"), { target: { value: "product_group" } });
    fill("Product group", "FASTENERS");
    fill("For every quantity ordered", "10");
    fill("Free quantity", "1");
    fill("Valid from", "2026-01-01");
    fill("Valid to", "2026-12-31");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByRole("heading", { name: "Group FASTENERS: 1 free within every 10 EA" })).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")!.body).toMatchObject({ productKind: "product_group", productId: "FASTENERS" });
  });

  it("asks for the group when the group is empty", () => {
    const v = { ...emptyFreeGoods, legalEntityId: LE, productKind: "product_group" as const, everyQuantity: "10", freeQuantity: "1", validFrom: "2026-01-01", validTo: "2026-12-31" };
    expect(validateFreeGoods(v).productId).toBe("Enter the product group (up to 64 characters).");
  });
});
