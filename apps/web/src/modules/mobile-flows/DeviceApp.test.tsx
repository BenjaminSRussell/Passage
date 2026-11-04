import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { applyOne, DeviceApp } from "./DeviceApp";
import { RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import type { FlowDraft } from "./domain/flowGraph";
import { OfflineQueue } from "./domain/offlineQueue";

vi.mock("./domain/offlineStore", async (importActual) => ({
  ...(await importActual<typeof import("./domain/offlineStore")>()),
  loadOrCreateDeviceKey: async () => ({}) as CryptoKey,
  AesGcmSealer: class {},
  IndexedDbStore: class {},
  loadQueue: async (_store: unknown, _cipher: unknown, deviceId: string) => new OfflineQueue(deviceId),
  saveQueue: async () => undefined,
}));

const SSCC = "]C100340123456789012340";

const withLotStep: FlowDraft = {
  ...RECEIVE_PUTAWAY_FLOW,
  steps: [
    RECEIVE_PUTAWAY_FLOW.steps[0],
    { ...RECEIVE_PUTAWAY_FLOW.steps[1], next: { default: "confirm_lot" } },
    { id: "confirm_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["ANY"], next: { default: "confirm_putaway" } },
    RECEIVE_PUTAWAY_FLOW.steps[2],
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("DeviceApp publish pickup (AC-32.4, BR-32.9)", () => {
  it("finishes the in-flight run on its own graph and starts the next run on the published one", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("down", { status: 503 })));
    const user = userEvent.setup();
    const { rerender } = render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);

    await user.type(await screen.findByLabelText(/^Scan/), `${SSCC}{Enter}`);
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    expect(await screen.findByText("Confirm put-away")).toBeInTheDocument();

    rerender(<DeviceApp deviceId="d1" userId="u1" flows={[withLotStep]} />);
    expect(screen.getByText("Confirm put-away")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Confirm" }));
    expect(await screen.findByText("Confirmation queued.")).toBeInTheDocument();

    await user.type(await screen.findByLabelText(/^Scan/), `${SSCC}{Enter}`);
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    expect(await screen.findByText("Scan the lot")).toBeInTheDocument();
  });
});

describe("applyOne replay outcomes", () => {
  const tx = { idempotencyKey: "d1:1", deviceId: "d1", seq: 1, command: "ConfirmWorkLine", payload: {}, capturedAt: 1, replayStatus: "PENDING" } as never;

  it.each([401, 408, 429, 503])("treats %i as transient so the queue keeps the transaction", async (status) => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status })));
    await expect(applyOne(tx)).rejects.toThrow("server unavailable");
  });

  it("rejects permanently on other client errors and applies on success", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 422 })));
    await expect(applyOne(tx)).resolves.toMatchObject({ status: "REJECTED" });
    vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 200 })));
    await waitFor(async () => expect(await applyOne(tx)).toEqual({ status: "APPLIED" }));
  });
});
