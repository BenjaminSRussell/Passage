import { afterEach, describe, expect, it, vi } from "vitest";
import {
  getPegging,
  getPlannedOrder,
  listMessages,
  listPlannedOrders,
  listVersions,
  PlanningApiError,
  runPlan,
} from "./client";

const runBody = { asOf: "2026-11-02", siteId: "00000000-0000-0000-0000-0000000000d1", items: [] };

function jsonResponse(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, statusText: "", json: async () => body } as Response;
}

/** Typed fetch mock: records (url, init) so tests can assert on calls. */
function mockFetch(respond: () => Response) {
  return vi.fn(async (_input: string, _init?: RequestInit) => respond());
}

const order = {
  id: "00000000-0000-0000-0000-0000000000a1",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000001",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "purchase",
  qty: "500",
  orderDate: "2026-11-02",
  deliveryDate: "2026-11-09",
  futuresDate: null,
  delayDays: 0,
  lotRule: "min 500",
  status: "unprocessed",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("planning api client", () => {
  it("lists planned orders with the plan code in the query and parses them", async () => {
    const fetchMock = mockFetch(() => jsonResponse(200, { items: [order] }));
    vi.stubGlobal("fetch", fetchMock);

    const items = await listPlannedOrders({ planCode: "MAIN", limit: 10 });

    expect(items).toHaveLength(1);
    expect(items[0].number).toBe("PLN-000001");
    const url = fetchMock.mock.calls[0][0];
    expect(url).toBe("/api/v1/planning/planned-orders?planCode=MAIN&limit=10");
  });

  it("maps a problem response to PlanningApiError with its stable code", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({
        ok: false,
        status: 409,
        statusText: "Conflict",
        json: async () => ({ code: "PLAN-0001", detail: "A run of plan MAIN is already in progress." }),
      }) as Response),
    );

    const err = await runPlan("MAIN", "key-1", runBody).catch((e: unknown) => e);

    expect(err).toBeInstanceOf(PlanningApiError);
    expect((err as PlanningApiError).status).toBe(409);
    expect((err as PlanningApiError).code).toBe("PLAN-0001");
  });

  it("sends the Idempotency-Key header on run requests", async () => {
    const version = {
      id: "00000000-0000-0000-0000-0000000000b1",
      planCode: "MAIN",
      number: 1,
      status: "current",
      asOf: "2026-11-02",
      itemsPlanned: 1,
      plannedOrders: 1,
      messages: 0,
      durationMs: 3,
      planHash: "abc",
    };
    const fetchMock = mockFetch(() => jsonResponse(201, version));
    vi.stubGlobal("fetch", fetchMock);

    const out = await runPlan("MAIN", "key-1", runBody);

    expect(out.status).toBe("current");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("POST");
    const headers = init.headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toBe("key-1");
    // Caller headers are merged with, not substituted for, the defaults.
    expect(headers.Accept).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual(runBody);
  });

  it("reads run history and one planned order from their OpenAPI paths", async () => {
    const fetchMock = mockFetch(() => jsonResponse(200, { items: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await listVersions("MAIN", 5);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/planning/plans/MAIN/versions?limit=5");

    const one = mockFetch(() => jsonResponse(200, order));
    vi.stubGlobal("fetch", one);
    expect((await getPlannedOrder(order.id)).number).toBe("PLN-000001");
    expect(one.mock.calls[0][0]).toBe(`/api/v1/planning/planned-orders/${order.id}`);
  });

  it("rejects a response that does not match the schema", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(200, { items: [{ ...order, orderType: "kanban" }] })));
    await expect(listPlannedOrders({ planCode: "MAIN" })).rejects.toThrow();
  });

  it("encodes the action filter on message queries", async () => {
    const fetchMock = mockFetch(() => jsonResponse(200, { items: [] }));
    vi.stubGlobal("fetch", fetchMock);

    await listMessages("MAIN", "delay");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/planning/messages?planCode=MAIN&action=delay");
  });

  it("fetches the pegging response the service returns and keeps the tree intact", async () => {
    const pegging = {
      orderId: order.id,
      number: "PLN-000001",
      qty: "110",
      tree: {
        ref: "PLN-000001",
        refType: "planned-order",
        qty: "110",
        children: [{ ref: "SO-1", refType: "sales-line", qty: "110", children: [] }],
      },
    };
    const fetchMock = mockFetch(() => jsonResponse(200, pegging));
    vi.stubGlobal("fetch", fetchMock);

    const out = await getPegging(order.id, 7);

    expect(out.tree.children[0].ref).toBe("SO-1");
    expect(fetchMock.mock.calls[0][0]).toBe(`/api/v1/planning/planned-orders/${order.id}/pegging?depth=7`);
  });
});
