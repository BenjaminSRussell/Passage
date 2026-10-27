import { afterEach, describe, expect, it, vi } from "vitest";
import { loadModules } from "../registry";
import {
  allowedCommands,
  journalTypesFor,
  createOrder,
  getCosts,
  listOrders,
  nextCommand,
  ProductionApiError,
  resetTargets,
  runCommand,
  updateOrder,
  type ProductionStatus,
} from "./api/client";
import { amount } from "./labels";
import production from "./module";
import { json, order, problem, summary } from "./testData";

afterEach(() => vi.unstubAllGlobals());

const calls = (mock: ReturnType<typeof vi.fn>) => mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>;

describe("production module", () => {
  it("registers list, create and detail routes and is discovered by the shell", () => {
    expect(production.id).toBe("production");
    expect(production.routes.map((r) => r.path)).toEqual(["/production", "/production/new", "/production/posting-profiles", "/production/:id"]);
    expect(production.nav).toEqual([
      { label: "Production orders", to: "/production", duty: "production.order.read" },
      { label: "New production order", to: "/production/new", duty: "production.order.write" },
      { label: "Posting profiles", to: "/production/posting-profiles", duty: "production.setup.read" },
    ]);
    expect(loadModules().some((m) => m.id === "production")).toBe(true);
  });
});

describe("journal helpers", () => {
  it("offers route cards on Released orders and every journal once Started (FR-21.5 to FR-21.7)", () => {
    expect(journalTypesFor("Estimated")).toEqual([]);
    expect(journalTypesFor("Released")).toEqual(["route-card"]);
    expect(journalTypesFor("Started")).toEqual(["picking-list", "route-card", "report-as-finished"]);
    expect(journalTypesFor("ReportedAsFinished")).toHaveLength(3);
    expect(journalTypesFor("Ended")).toEqual([]);
  });
});

describe("state machine helpers", () => {
  it("offers the forward command for each status and none at the end of the line", () => {
    const expected: Record<ProductionStatus, string | null> = {
      Created: "estimate",
      Estimated: "schedule",
      Scheduled: "release",
      Released: "start",
      Started: "report-finished",
      ReportedAsFinished: "end",
      Ended: null,
      Cancelled: null,
    };
    for (const [status, command] of Object.entries(expected)) {
      expect(nextCommand(status as ProductionStatus)).toBe(command);
      const allowed = allowedCommands(status as ProductionStatus);
      expect(command === null ? allowed.length === 0 : allowed[0] === command).toBe(true);
    }
  });

  it("allows cancel only before Started and re-estimate only until Started (BR-21.1, FR-21.3)", () => {
    expect(allowedCommands("Released")).toContain("cancel");
    expect(allowedCommands("Started")).not.toContain("cancel");
    expect(allowedCommands("Released")).toContain("estimate");
    expect(allowedCommands("Started")).not.toContain("estimate");
  });

  it("lists every earlier status as a reset target, latest first", () => {
    expect(resetTargets("ReportedAsFinished")).toEqual(["Started", "Released", "Scheduled", "Estimated", "Created"]);
    expect(resetTargets("Created")).toEqual([]);
    expect(resetTargets("Ended")).toEqual([]);
  });

  it("pads amounts to cents without floating point", () => {
    expect(amount("5000")).toBe("5000.00");
    expect(amount("12.5")).toBe("12.50");
    expect(amount("0.1250")).toBe("0.1250");
    expect(amount("123456789012.99")).toBe("123456789012.99");
    expect(amount(undefined)).toBe("0.00");
  });
});

describe("production API client", () => {
  it("lists with status, cursor and limit", async () => {
    const fetchMock = vi.fn(async () => json({ items: [summary()], nextCursor: "c-2" }));
    vi.stubGlobal("fetch", fetchMock);
    const page = await listOrders({ status: "Released", cursor: "c-1", limit: 25 });
    expect(page.nextCursor).toBe("c-2");
    expect(calls(fetchMock)[0][0]).toBe("/api/v1/production/orders?limit=25&status=Released&cursor=c-1");
  });

  it("posts a command with Idempotency-Key, quoted If-Match and the body", async () => {
    const fetchMock = vi.fn(async () => json(order({ status: "ReportedAsFinished", version: 7 })));
    vi.stubGlobal("fetch", fetchMock);
    const got = await runCommand("abc", "report-finished", 6, "key-1", { quantity: "60" });
    expect(got.version).toBe(7);
    const [url, init] = calls(fetchMock)[0];
    expect(url).toBe("/api/v1/production/orders/abc:report-finished");
    expect(init?.method).toBe("POST");
    expect(init?.headers).toMatchObject({ "Idempotency-Key": "key-1", "If-Match": '"6"' });
    expect(JSON.parse(String(init?.body))).toEqual({ quantity: "60" });
  });

  it("creates with an Idempotency-Key and patches with If-Match", async () => {
    const fetchMock = vi.fn(async () => json(order()));
    vi.stubGlobal("fetch", fetchMock);
    await createOrder({ productId: "p", siteId: "s", warehouseId: "w", currency: "USD", quantity: "1", bom: [], operations: [] }, "k-9");
    await updateOrder("abc", 3, { deliveryDate: null });
    const [createUrl, createInit] = calls(fetchMock)[0];
    expect(createUrl).toBe("/api/v1/production/orders");
    expect(createInit?.headers).toMatchObject({ "Idempotency-Key": "k-9" });
    const [patchUrl, patchInit] = calls(fetchMock)[1];
    expect(patchUrl).toBe("/api/v1/production/orders/abc");
    expect(patchInit?.method).toBe("PATCH");
    expect(patchInit?.headers).toMatchObject({ "If-Match": '"3"' });
    expect(JSON.parse(String(patchInit?.body))).toEqual({ deliveryDate: null });
  });

  it("turns a problem response into a ProductionApiError with code and field errors", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => problem(400, "COM-0001", "request failed validation", { "bom[0].itemId": "must be a UUID" })));
    const error = await getCosts("abc").catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ProductionApiError);
    expect(error).toMatchObject({ status: 400, code: "COM-0001", message: "request failed validation", errors: { "bom[0].itemId": "must be a UUID" } });
  });

  it("falls back to a generic code when the body is not a problem", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("bad gateway", { status: 502 })));
    await expect(listOrders()).rejects.toMatchObject({ status: 502, code: "COM-0001", message: "Request failed (502)" });
  });
});
