import { render, screen, waitFor, act } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { day, memoryStore, reverseCipher, visit } from "./domain/fixtures";
import { createSseParser, ROUTE_EVENTS_URL, subscribeRoute, type RouteSubscription } from "./api/client";
import type { SyncClient } from "./domain/sync";
import { TechnicianApp } from "./TechnicianApp";
import type { TechnicianDeps } from "./useTechnician";

const enc = new TextEncoder();

/** A response whose body yields the given chunks, then stays open until `end()`. */
function streamResponse(chunks: string[], status = 200) {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
      for (const chunk of chunks) c.enqueue(enc.encode(chunk));
    },
  });
  return { res: new Response(status === 200 ? body : null, { status }), push: (t: string) => controller.enqueue(enc.encode(t)), end: () => controller.close() };
}

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("createSseParser", () => {
  it("reads events across chunk boundaries and ignores comments and other fields", () => {
    const parse = createSseParser();
    expect(parse("retry: 5000\nevent: ready\ndata: {\"technicianId\":\"t1\"}\n\n")).toEqual([{ event: "ready", data: '{"technicianId":"t1"}' }]);
    expect(parse(":\n\n")).toEqual([]);
    expect(parse("event: ro")).toEqual([]);
    expect(parse("ute\ndata: {}\r\n\r")).toEqual([]);
    expect(parse("\nevent: route\ndata: a\ndata: b\n\n")).toEqual([
      { event: "route", data: "{}" },
      { event: "route", data: "a\nb" },
    ]);
    expect(parse("data: bare\n\n")).toEqual([{ event: "message", data: "bare" }]);
  });
});

describe("subscribeRoute", () => {
  it("reads route events through the given fetch with the bearer-adding request function, and reports open", async () => {
    const stream = streamResponse(["event: ready\ndata: {}\n\n"]);
    const calls: { url: string; init: RequestInit }[] = [];
    let routes = 0;
    const sub = subscribeRoute(() => routes++, { fetchImpl: async (url, init) => (calls.push({ url, init }), stream.res), sleep: () => new Promise(() => undefined) });
    await tick();
    await tick();
    expect(calls[0]?.url).toBe(ROUTE_EVENTS_URL);
    expect((calls[0]?.init.headers as Record<string, string>).Accept).toBe("text/event-stream");
    expect(sub.open).toBe(true);
    expect(routes).toBe(0); // the first open follows the day that was just read; "ready" is not a route change
    stream.push("event: route\ndata: {}\n\n");
    await tick();
    await tick();
    expect(routes).toBe(1);
    sub.close();
    expect(sub.open).toBe(false);
  });

  it("reconnects with a backoff when the stream drops, reads the day again on the reconnect, and stops on close", async () => {
    const first = streamResponse(["event: ready\ndata: {}\n\n"]);
    const second = streamResponse(["event: ready\ndata: {}\n\n"]);
    const answers = [first.res, new Response(null, { status: 503 }), second.res];
    const waits: number[] = [];
    let routes = 0;
    const sub = subscribeRoute(() => routes++, {
      fetchImpl: async () => answers.shift() ?? new Response(null, { status: 503 }),
      sleep: async (ms) => {
        waits.push(ms);
      },
    });
    await tick();
    first.end();
    for (let i = 0; i < 20; i += 1) await tick();
    expect(waits.slice(0, 2)).toEqual([1000, 2000]); // after the first stream, then after the refused reconnect
    expect(routes).toBeGreaterThanOrEqual(1); // the second open is a reconnect
    sub.close();
  });

  it("is never open when the service has no feed (503)", async () => {
    const sub = subscribeRoute(() => undefined, { fetchImpl: async () => new Response(null, { status: 503 }), sleep: () => new Promise(() => undefined) });
    await tick();
    await tick();
    expect(sub.open).toBe(false);
    sub.close();
  });
});

describe("TechnicianApp route push", () => {
  it("reads the day again when the service says the route changed, and closes the stream on leaving", async () => {
    let loads = 0;
    let push: (() => void) | undefined;
    let closed = false;
    const client: SyncClient = { postActions: async () => [], putCapture: async () => undefined };
    const deps: TechnicianDeps = {
      store: memoryStore(),
      cipher: reverseCipher,
      client,
      loadDay: async () => {
        loads++;
        return day([visit({ address: loads === 1 ? "12 Mill Road, Duluth, MN" : "77 Lake Street, Harlan, IA" })]);
      },
      loadTruck: async () => [],
      subscribeRoute: (onRoute): RouteSubscription => {
        push = onRoute;
        return { open: true, close: () => (closed = true) };
      },
      resolveName: async () => "Bluewater Municipal Utilities (C-10021)",
      now: () => new Date("2026-10-26T15:00:00Z"),
      isOnline: () => true,
    };
    const view = render(<TechnicianApp deps={deps} />);
    expect(await screen.findByText("12 Mill Road, Duluth, MN", { exact: false })).toBeInTheDocument();
    expect(loads).toBe(1);
    await act(async () => push?.());
    await waitFor(() => expect(screen.getByText("77 Lake Street, Harlan, IA", { exact: false })).toBeInTheDocument());
    expect(loads).toBe(2);
    view.unmount();
    expect(closed).toBe(true);
  });
});
