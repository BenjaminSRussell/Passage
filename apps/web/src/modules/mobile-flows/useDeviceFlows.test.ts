import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PICK_FLOW, RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import type { FlowStep } from "./domain/flowGraph";
import { useDeviceFlows } from "./useDeviceFlows";

const published = {
  id: "default",
  flows: [{ ...RECEIVE_PUTAWAY_FLOW, flowId: "RECEIVE_PUTAWAY", version: 1, status: "PUBLISHED" }],
};

const v2 = {
  id: "default",
  flows: [
    {
      ...RECEIVE_PUTAWAY_FLOW,
      flowId: "RECEIVE_PUTAWAY",
      version: 2,
      status: "PUBLISHED",
      steps: [
        ...RECEIVE_PUTAWAY_FLOW.steps.slice(0, 2),
        { id: "confirm_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["LOT"], next: { default: "confirm_putaway" } } satisfies FlowStep,
        RECEIVE_PUTAWAY_FLOW.steps[2],
      ],
    },
  ],
};
v2.flows[0].steps[1] = { ...v2.flows[0].steps[1], next: { default: "confirm_lot" } };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("useDeviceFlows", () => {
  it("loads published flows from the menu and falls back to the bundled core set", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify(published), { status: 200, headers: { "Content-Type": "application/json" } })),
    );
    const { result } = renderHook(() => useDeviceFlows({ fallback: [PICK_FLOW] }));
    await waitFor(() => expect(result.current.flows.map((f) => f.code)).toEqual(["RECEIVE_PUTAWAY"]));
    expect(result.current.source).toBe("menu");

    vi.stubGlobal("fetch", vi.fn(async () => new Response("not found", { status: 404 })));
    const missing = renderHook(() => useDeviceFlows({ fallback: [PICK_FLOW] }));
    await waitFor(() => expect(missing.result.current.source).toBe("bundled"));
    expect(missing.result.current.flows).toEqual([PICK_FLOW]);
  });

  it("reloads the menu when a flow.published event arrives", async () => {
    const sources: FakeEventSource[] = [];
    class FakeEventSource {
      listeners = new Map<string, (ev: MessageEvent) => void>();
      constructor() {
        sources.push(this);
      }
      addEventListener(type: string, listener: (ev: MessageEvent) => void) {
        this.listeners.set(type, listener);
      }
      close() {}
    }
    let menu = published;
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(menu), { status: 200 })));
    const { result } = renderHook(() =>
      useDeviceFlows({ fallback: [RECEIVE_PUTAWAY_FLOW], EventSource: FakeEventSource as unknown as typeof EventSource }),
    );
    await waitFor(() => expect(result.current.source).toBe("menu"));
    menu = v2;
    act(() => {
      sources[0]?.listeners.get("flow.published")?.({ data: JSON.stringify({ flow_id: "RECEIVE_PUTAWAY", code: "RECEIVE_PUTAWAY", version: 2 }) } as MessageEvent);
    });
    await waitFor(() => expect(result.current.flows[0]?.steps.map((s) => s.id)).toContain("confirm_lot"));
  });

  it("ignores a stale menu response that finishes after a newer load", async () => {
    let releaseFirst: (value: Response) => void = () => undefined;
    const first = new Promise<Response>((resolve) => {
      releaseFirst = resolve;
    });
    const sources: FakeEventSource[] = [];
    class FakeEventSource {
      listeners = new Map<string, (ev: MessageEvent) => void>();
      constructor() {
        sources.push(this);
      }
      addEventListener(type: string, listener: (ev: MessageEvent) => void) {
        this.listeners.set(type, listener);
      }
      close() {}
    }
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(() => first)
      .mockImplementation(async () => new Response(JSON.stringify(v2), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() =>
      useDeviceFlows({ fallback: [PICK_FLOW], EventSource: FakeEventSource as unknown as typeof EventSource }),
    );
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(result.current.source).toBe("bundled");
    act(() => {
      sources[0]?.listeners.get("flow.published")?.({
        data: JSON.stringify({ flow_id: "RECEIVE_PUTAWAY", code: "RECEIVE_PUTAWAY", version: 2 }),
      } as MessageEvent);
    });
    await waitFor(() => expect(result.current.flows[0]?.steps.map((s) => s.id)).toContain("confirm_lot"));
    await act(async () => {
      releaseFirst(new Response(JSON.stringify(published), { status: 200 }));
    });
    expect(result.current.flows[0]?.steps.map((s) => s.id)).toContain("confirm_lot");
  });
});
