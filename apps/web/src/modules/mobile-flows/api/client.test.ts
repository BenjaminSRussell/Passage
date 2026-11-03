import { afterEach, describe, expect, it, vi } from "vitest";
import { RECEIVE_PUTAWAY_FLOW } from "../domain/coreFlows";
import {
  DEFAULT_MENU_ID,
  fetchMenu,
  FlowApiError,
  parseFlowPublished,
  subscribePublishes,
} from "./client";

const MENU = {
  id: "default",
  flows: [{ ...RECEIVE_PUTAWAY_FLOW, flowId: "RECEIVE_PUTAWAY", version: 1, status: "PUBLISHED" as const }],
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("fetchMenu", () => {
  it("loads the published flows for a menu id", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(MENU), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(fetchMenu("wh-1")).resolves.toEqual(MENU);
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/mobile/menus/wh-1", {
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      signal: undefined,
    });
  });

  it("uses the default menu id when none is given", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(MENU), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await fetchMenu();
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/mobile/menus/${DEFAULT_MENU_ID}`, {
      headers: { Accept: "application/json" },
      credentials: "same-origin",
      signal: undefined,
    });
  });

  it("rejects a body that is not a menu of flows", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ id: "x" }), { status: 200 })));
    await expect(fetchMenu()).rejects.toMatchObject({ name: "FlowApiError", code: "COM-0001" });
  });

  it("surfaces a missing mobile service as FlowApiError 404", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("not found", { status: 404 })));
    await expect(fetchMenu()).rejects.toBeInstanceOf(FlowApiError);
    await expect(fetchMenu()).rejects.toMatchObject({ status: 404 });
  });
});

describe("parseFlowPublished", () => {
  it("reads the SSE payload the designer publish emits", () => {
    expect(parseFlowPublished(JSON.stringify({ flow_id: "f1", code: "RECEIVE_PUTAWAY", version: 2, warehouses: [] }))).toEqual({
      flowId: "f1",
      code: "RECEIVE_PUTAWAY",
      version: 2,
    });
  });

  it("returns null for a payload that is not a publish", () => {
    expect(parseFlowPublished("{")).toBeNull();
    expect(parseFlowPublished(JSON.stringify({ type: "other" }))).toBeNull();
  });
});

describe("subscribePublishes", () => {
  it("forwards flow.published frames and closes the EventSource on unsubscribe", () => {
    const sources: FakeEventSource[] = [];
    class FakeEventSource {
      url: string;
      listeners = new Map<string, (ev: MessageEvent) => void>();
      closed = false;
      constructor(url: string) {
        this.url = url;
        sources.push(this);
      }
      addEventListener(type: string, listener: (ev: MessageEvent) => void) {
        this.listeners.set(type, listener);
      }
      close() {
        this.closed = true;
      }
      emit(type: string, data: string) {
        this.listeners.get(type)?.({ data } as MessageEvent);
      }
    }
    const seen: { code: string; version: number }[] = [];
    const stop = subscribePublishes((e) => seen.push({ code: e.code, version: e.version }), {
      EventSource: FakeEventSource as unknown as typeof EventSource,
    });
    expect(sources[0]?.url).toBe("/api/v1/mobile/events");
    sources[0]?.emit("flow.published", JSON.stringify({ flow_id: "f1", code: "PICK", version: 3 }));
    expect(seen).toEqual([{ code: "PICK", version: 3 }]);
    stop();
    expect(sources[0]?.closed).toBe(true);
  });
});
