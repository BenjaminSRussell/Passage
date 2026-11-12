import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushLabelOutbox, printPalletLabel } from "./api/palletLabel";
import { DeviceApp } from "./DeviceApp";
import { RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import { LabelOutbox, MAX_WAITING, flushOutbox, type OutboxStorage } from "./domain/labelOutbox";
import { OfflineQueue } from "./domain/offlineQueue";
import { gs1Date, labelFromPallet, type PalletLabel } from "./domain/receiveLabel";

vi.mock("./domain/offlineStore", async (importActual) => ({
  ...(await importActual<typeof import("./domain/offlineStore")>()),
  loadOrCreateDeviceKey: async () => ({}) as CryptoKey,
  AesGcmSealer: class {},
  IndexedDbStore: class {},
  loadQueue: async (_store: unknown, _cipher: unknown, deviceId: string) => new OfflineQueue(deviceId),
  saveQueue: async () => undefined,
}));

const SSCC = "306141410000000013";
const label: PalletLabel = { sscc: SSCC, lpId: SSCC, itemId: "BOLT-M8", lot: "L-9", expiryDate: "2027-03-31", receiptId: "r1", lineNo: 1, workId: "w1", received: true };

function memory(): OutboxStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return { data, getItem: (k) => data.get(k) ?? null, setItem: (k, v) => void data.set(k, v) };
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
type Call = { method: string; path: string; body: unknown; key: string | null };
let calls: Call[];
function stub(answer: (c: Call) => Response | undefined) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const c: Call = { method: init.method ?? "GET", path: new URL(input, "http://localhost").pathname, body: init.body ? JSON.parse(String(init.body)) : undefined, key: new Headers(init.headers).get("Idempotency-Key") };
      calls.push(c);
      return answer(c) ?? json(404, { code: "TEST-404", detail: "no fake" });
    }),
  );
}
const printed = () => json(201, { id: "j1", status: "PRINTED", printerCode: "DOCK-1", queueCode: "DOCK", templateCode: "LP_LABEL", format: "ZPL", attempts: 1 });
const labelPath = `/api/v1/warehouse/pallets/${SSCC}/label`;

let onLine = true;
beforeEach(() => {
  onLine = true;
  vi.spyOn(window.navigator, "onLine", "get").mockImplementation(() => onLine);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("the label from what the warehouse holds", () => {
  it("builds the LP label from the receipt's item, lot and expiry date, with the date in GS1 form", () => {
    expect(labelFromPallet(label, { warehouseId: "wh-1" })).toEqual({
      ok: true,
      request: { templateCode: "LP_LABEL", documentType: "LP_LABEL", warehouseId: "wh-1", data: { lp: SSCC, item: "BOLT-M8", lot: "L-9", expiry: "270331", sscc: SSCC } },
    });
    expect(gs1Date("2027-03-31")).toBe("270331");
    expect(gs1Date("31/03/2027")).toBeNull();
  });

  it("says what the receipt is missing, so the worker knows to scan the item label", () => {
    expect(labelFromPallet({ ...label, lot: "", expiryDate: undefined })).toEqual({
      ok: false,
      reason: "The receipt has no lot or expiry date for this pallet, so the label cannot be printed from it.",
    });
    expect(labelFromPallet({ ...label, expiryDate: "soon" })).toEqual({ ok: false, reason: "The expiry date on the receipt is not a date." });
  });
});

describe("the label outbox", () => {
  const request = { templateCode: "LP_LABEL", documentType: "LP_LABEL", data: { lp: "a", item: "b", lot: "c", expiry: "270331", sscc: "a" } } as const;

  it("keeps one entry per pallet, survives a reload, and caps what it holds", () => {
    const storage = memory();
    const outbox = new LabelOutbox(storage);
    outbox.enqueue("111", undefined, 1);
    outbox.enqueue("111", request, 2); // the same pallet gains its request and keeps its place
    expect(outbox.list()).toEqual([{ sscc: "111", request, queuedAt: 1, attempts: 0 }]);
    expect(new LabelOutbox(storage).waiting()).toBe(1);
    for (let i = 0; i < MAX_WAITING - 1; i++) outbox.enqueue(`p${i}`, undefined, 3);
    const over = outbox.enqueue("last", undefined, 4);
    expect(over.dropped).toBe("111");
    expect(outbox.list()).toHaveLength(MAX_WAITING);
    storage.data.set("passage.labelOutbox", "not json");
    expect(outbox.list()).toEqual([]);
  });

  it("prints in order, stops at a transient failure, and blocks a permanent one without stopping the rest", async () => {
    const outbox = new LabelOutbox(memory());
    for (const s of ["1", "2", "3", "4"]) outbox.enqueue(s, request, 1);
    const order: string[] = [];
    const report = await flushOutbox(outbox, {
      resolve: async () => ({ ok: false, reason: "unused", transient: false }),
      print: async (sscc) => {
        order.push(sscc);
        if (sscc === "2") return { printed: false, transient: false, reason: "No printer is set up for LP labels." };
        if (sscc === "4") return { printed: false, transient: true, jobFailed: true, reason: "printer offline" };
        return { printed: true };
      },
    });
    expect(order).toEqual(["1", "2", "3", "4"]);
    expect(report).toEqual({ printed: ["1", "3"], blocked: [{ sscc: "2", reason: "No printer is set up for LP labels." }], stopped: true });
    expect(outbox.list().map((e) => [e.sscc, e.blocked ?? null, e.attempts])).toEqual([["2", "No printer is set up for LP labels.", 0], ["4", null, 1]]);
    expect(outbox.waiting()).toBe(1);
  });
});

describe("printing from the warehouse's data", () => {
  it("prints at once when the device is online, with one key per pallet", async () => {
    stub((c) => (c.path === labelPath ? json(200, label) : c.path === "/api/v1/print/jobs" ? printed() : undefined));
    const outbox = new LabelOutbox(memory());
    await expect(printPalletLabel(SSCC, outbox, "wh-1")).resolves.toEqual({ kind: "printed" });
    const job = calls.find((c) => c.path === "/api/v1/print/jobs")!;
    expect(job.key).toBe(`lp-label:${SSCC}`);
    expect(job.body).toMatchObject({ templateCode: "LP_LABEL", warehouseId: "wh-1", data: { lp: SSCC, item: "BOLT-M8", lot: "L-9", expiry: "270331", sscc: SSCC } });
    expect(outbox.waiting()).toBe(0);
  });

  it("saves the label while the device is offline, and prints it when the connection is back, reading the label data then", async () => {
    const outbox = new LabelOutbox(memory());
    onLine = false;
    stub(() => undefined);
    await expect(printPalletLabel(SSCC, outbox)).resolves.toEqual({ kind: "queued" });
    expect(calls).toHaveLength(0);
    expect(outbox.list()).toMatchObject([{ sscc: SSCC, attempts: 0 }]);
    onLine = true;
    stub((c) => (c.path === labelPath ? json(200, label) : c.path === "/api/v1/print/jobs" ? printed() : undefined));
    const report = await flushLabelOutbox(outbox);
    expect(report).toEqual({ printed: [SSCC], blocked: [], stopped: false });
    expect(outbox.waiting()).toBe(0);
  });

  it("queues a label whose print job failed under a new key, because the failed job is stored under the first", async () => {
    stub((c) => (c.path === labelPath ? json(200, label) : c.path === "/api/v1/print/jobs" ? json(201, { id: "j1", status: "FAILED", lastError: "MOB-0111", queueCode: "DOCK", templateCode: "LP_LABEL", format: "ZPL", attempts: 3 }) : undefined));
    const outbox = new LabelOutbox(memory());
    await expect(printPalletLabel(SSCC, outbox)).resolves.toEqual({ kind: "queued" });
    expect(outbox.list()).toMatchObject([{ sscc: SSCC, attempts: 1 }]);
    stub((c) => (c.path === "/api/v1/print/jobs" ? printed() : undefined));
    await flushLabelOutbox(outbox);
    expect(calls[0]?.key).toBe(`lp-label:${SSCC}:1`);
    expect(outbox.waiting()).toBe(0);
  });

  it("falls back to the item scan for a pallet the warehouse has no label data for, and never queues it", async () => {
    stub((c) => (c.path === labelPath ? json(404, { code: "WMS-PALLET-NOT-FOUND", detail: "not found" }) : undefined));
    const outbox = new LabelOutbox(memory());
    await expect(printPalletLabel(SSCC, outbox)).resolves.toEqual({ kind: "manual", reason: "The warehouse has no pallet with this SSCC." });
    stub((c) => (c.path === labelPath ? json(200, { ...label, lot: "" }) : undefined));
    await expect(printPalletLabel(SSCC, outbox)).resolves.toMatchObject({ kind: "manual" });
    expect(outbox.waiting()).toBe(0);
  });

  it("queues a label when the warehouse cannot be reached", async () => {
    stub(() => json(503, { code: "COM-0503", detail: "down" }));
    const outbox = new LabelOutbox(memory());
    await expect(printPalletLabel(SSCC, outbox)).resolves.toEqual({ kind: "queued" });
    expect(outbox.list()).toMatchObject([{ sscc: SSCC }]);
  });
});

describe("the device prints the label after put-away without a second scan", () => {
  const SCANNED = `]C100${SSCC}`;

  async function putAway() {
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/^Scan/), `${SCANNED}{Enter}`);
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
  }

  it("prints from the pallet's label data and says so", async () => {
    stub((c) => (c.path === labelPath ? json(200, label) : c.path === "/api/v1/print/jobs" ? printed() : undefined));
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await putAway();
    expect(await screen.findByText("The label printed.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Item label (GTIN, lot, expiry)")).toBeNull();
  });

  it("saves the label when the print service is away, shows the count, and prints it when the device is back online", async () => {
    localStorage.clear();
    let up = false;
    stub((c) => {
      if (!up) return json(503, { code: "COM-0503", detail: "down" });
      return c.path === labelPath ? json(200, label) : c.path === "/api/v1/print/jobs" ? printed() : undefined;
    });
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await putAway();
    expect(await screen.findByText("The label is saved on this device and prints when it is back online.")).toBeInTheDocument();
    expect(screen.getByText(/1 label waiting to print/)).toBeInTheDocument();
    up = true;
    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    await waitFor(() => expect(screen.getByText("1 saved label printed.")).toBeInTheDocument());
    expect(screen.queryByText(/waiting to print/)).toBeNull();
  });
});
