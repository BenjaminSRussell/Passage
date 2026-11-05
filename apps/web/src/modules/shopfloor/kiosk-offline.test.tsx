import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import shopfloor from "./module";
import { MemoryQueueStore, OfflineQueue, setKioskQueue } from "./offlineQueue";
import { OeeLiveTile } from "./OeeLiveTile";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const RESOURCE = "0195f0a0-0000-7000-8000-000000000010";

let down = false;
let posted: { deviceSentAt?: string; items: { id: string; kind: string; offline?: boolean }[] }[];

beforeEach(() => {
  down = false;
  posted = [];
  setKioskQueue(new OfflineQueue(new MemoryQueueStore()));
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      if (url.pathname.endsWith("/sessions:badge")) {
        return Response.json({ workerId: WORKER, partyId: WORKER, name: "Ana Silva", userId: null, clockedInAt: null, defaultResourceId: RESOURCE, openJobs: [], idleTimeoutMinutes: 10 });
      }
      if (url.pathname.endsWith("/jobs")) return Response.json({ items: [] });
      if (url.pathname.endsWith("/registrations") && init.method === "POST") {
        if (down) throw new TypeError("Failed to fetch");
        const body = JSON.parse(String(init.body)) as (typeof posted)[number];
        posted.push(body);
        return Response.json({ items: body.items.map((it) => ({ id: it.id, status: "accepted", flags: [], autoStopped: [], warnings: [] })) });
      }
      return Response.json({ code: "TEST-404" }, { status: 404 });
    }),
  );
});

afterEach(() => {
  setKioskQueue(null);
  vi.unstubAllGlobals();
});

function renderKiosk() {
  return render(
    <MemoryRouter initialEntries={["/shopfloor/kiosk"]}>
      <Routes>
        {shopfloor.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("Kiosk offline queue", () => {
  it("saves punches on the kiosk while the network is down and syncs them in order when it is back", async () => {
    renderKiosk();
    fireEvent.change(screen.getByLabelText("Badge"), { target: { value: "B-42" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await screen.findByRole("heading", { name: "Ana Silva" });

    down = true;
    fireEvent.click(screen.getByRole("button", { name: "Clock in" }));
    expect(await screen.findByText(/Saved on this kiosk; it syncs when the network is back\./)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Clock out" }));
    expect(await screen.findByText("2 actions waiting to sync")).toBeInTheDocument();
    expect(posted).toHaveLength(0);

    down = false;
    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    expect(await screen.findByText("Synced 2 offline actions.")).toBeInTheDocument();
    expect(posted).toHaveLength(1);
    expect(posted[0].items.map((it) => it.kind)).toEqual(["clock-in", "clock-out"]);
    expect(posted[0].items.every((it) => it.offline === true)).toBe(true);
    expect(posted[0].deviceSentAt).toMatch(/^\d{4}-\d\d-\d\dT/);
  });
});

class FakeEventSource {
  static last: FakeEventSource | null = null;
  readonly listeners = new Map<string, (e: MessageEvent<string>) => void>();
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  constructor(readonly url: string) {
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, fn);
  }
  removeEventListener(type: string) {
    this.listeners.delete(type);
  }
  close() {
    this.closed = true;
  }
  emit(data: unknown) {
    this.listeners.get("oee")?.(new MessageEvent("oee", { data: JSON.stringify(data) }));
  }
}

const bucket = (good: number, total: number, oee: string) => ({
  resourceId: RESOURCE, bucketStart: "2026-10-21T06:00:00Z", plannedMinutes: "10", runMinutes: "10", downMinutes: "0",
  totalCount: total, goodCount: good, availability: "1", performance: "0.8", quality: "0.8", oee, overRate: false, sourceEvents: 2,
});

describe("OeeLiveTile", () => {
  it("subscribes to the resource's stream and shows each pushed bucket", async () => {
    const { unmount } = render(<OeeLiveTile resourceId={RESOURCE} machineLabel="M-1" bucket="15m" EventSource={FakeEventSource as unknown as typeof EventSource} />);
    const es = FakeEventSource.last!;
    expect(es.url).toBe(`/api/v1/shopfloor/oee/stream?resourceId=${RESOURCE}&bucket=15m`);
    expect(screen.getByText("Connecting")).toBeInTheDocument();

    act(() => es.emit({ resourceId: RESOURCE, bucketStart: "2026-10-21T06:00:00Z", bucketEnd: "2026-10-21T06:15:00Z", computedAt: "2026-10-21T06:10:00Z", bucket: bucket(4, 5, "0.64") }));
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(screen.getByText("64.0 %")).toBeInTheDocument();
    expect(screen.getByText("4 / 5")).toBeInTheDocument();

    act(() => es.emit({ resourceId: RESOURCE, bucketStart: "2026-10-21T06:00:00Z", bucketEnd: "2026-10-21T06:15:00Z", computedAt: "2026-10-21T06:10:02Z", bucket: bucket(7, 8, "0.7") }));
    await waitFor(() => expect(screen.getByText("7 / 8")).toBeInTheDocument());

    act(() => es.emit({ resourceId: RESOURCE, bucketStart: "2026-10-21T06:15:00Z", bucketEnd: "2026-10-21T06:30:00Z", computedAt: "2026-10-21T06:15:01Z", bucket: null }));
    expect(screen.getByText(/No planned time in this bucket yet/)).toBeInTheDocument();

    act(() => es.onerror?.());
    expect(screen.getByText("Reconnecting")).toBeInTheDocument();
    unmount();
    expect(es.closed).toBe(true);
  });
});
