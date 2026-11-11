import { render, screen, waitFor, act } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { day, memoryStore, reverseCipher, visit } from "./domain/fixtures";
import { ROUTE_EVENTS_URL, subscribeRoute, type RouteEventSource, type RouteSubscription } from "./api/client";
import type { SyncClient } from "./domain/sync";
import { TechnicianApp } from "./TechnicianApp";
import type { TechnicianDeps } from "./useTechnician";

class FakeSource implements RouteEventSource {
  static last: FakeSource | undefined;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  listeners: Record<string, () => void> = {};
  constructor(readonly url: string) {
    FakeSource.last = this;
  }
  addEventListener(type: string, listener: () => void) {
    this.listeners[type] = listener;
  }
  close() {
    this.closed = true;
  }
}

describe("subscribeRoute", () => {
  it("opens the route stream, reports route events and reconnects, and closes", () => {
    let routes = 0;
    const sub = subscribeRoute(() => routes++, FakeSource);
    const source = FakeSource.last!;
    expect(source.url).toBe(ROUTE_EVENTS_URL);
    expect(sub.open).toBe(false);
    source.onopen?.();
    expect(sub.open).toBe(true);
    expect(routes).toBe(0); // the first open follows the day that was just read
    source.listeners.route?.();
    expect(routes).toBe(1);
    source.onerror?.();
    expect(sub.open).toBe(false);
    source.onopen?.();
    expect(routes).toBe(2); // a reconnect reads the day again
    sub.close();
    expect(source.closed).toBe(true);
  });

  it("is not open without EventSource or when it cannot be created", () => {
    expect(subscribeRoute(() => undefined, undefined).open).toBe(false);
    class Broken {
      constructor() {
        throw new Error("blocked");
      }
    }
    expect(subscribeRoute(() => undefined, Broken as unknown as typeof FakeSource).open).toBe(false);
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
