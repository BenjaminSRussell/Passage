import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DeviceApp } from "./DeviceApp";
import { PICK_FLOW, RECEIVE_PUTAWAY_FLOW } from "./domain/coreFlows";
import { OfflineQueue } from "./domain/offlineQueue";
import { encodeSgtin96 } from "./domain/epc";
import type { VoiceAdapter } from "./domain/voice";

vi.mock("./domain/offlineStore", async (importActual) => ({
  ...(await importActual<typeof import("./domain/offlineStore")>()),
  loadOrCreateDeviceKey: async () => ({}) as CryptoKey,
  AesGcmSealer: class {},
  IndexedDbStore: class {},
  loadQueue: async (_store: unknown, _cipher: unknown, deviceId: string) => new OfflineQueue(deviceId),
  saveQueue: async () => undefined,
}));

const WAREHOUSE = "0195f0a0-0000-7000-8000-0000000000c1";
const SSCC = "]C100340123456789012340";
const ITEM = "]C1" + "01" + "09501234567891" + "10LOT42" + "\u001D" + "17270630";

type Call = { method: string; path: string; body: unknown };
let calls: Call[];

function stubService(answer: (c: Call) => Response | undefined) {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const c: Call = { method: init.method ?? "GET", path: new URL(input, "http://localhost").pathname, body: init.body ? JSON.parse(String(init.body)) : undefined };
      calls.push(c);
      return answer(c) ?? new Response(JSON.stringify({ code: "TEST-404", detail: "no fake" }), { status: 404 });
    }),
  );
}

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const session = { sessionId: "sess-1", deviceId: "d1", userId: "u1", warehouseId: WAREHOUSE, menuKey: "default", status: "ACTIVE", version: 1 };

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("device wired to the mobile service (32-1)", () => {
  it("starts a session for the provisioned warehouse, beats it, and signs out on close", async () => {
    stubService((c) => {
      if (c.path === "/api/v1/mobile/sessions") return json(201, { session, resumed: false });
      if (c.path.endsWith(":heartbeat")) return json(200, { session, relocked: true });
      if (c.path.endsWith(":end")) return json(200, session);
      return undefined;
    });
    const { unmount } = render(<DeviceApp deviceId="d1" userId="u1" warehouseId={WAREHOUSE} flows={[PICK_FLOW]} />);
    await waitFor(() => expect(calls.some((c) => c.path === "/api/v1/mobile/sessions" && c.method === "POST")).toBe(true));
    expect(calls.find((c) => c.path === "/api/v1/mobile/sessions")?.body).toEqual({ deviceId: "d1", warehouseId: WAREHOUSE, menuKey: "default" });
    unmount();
    await waitFor(() => expect(calls.some((c) => c.path === "/api/v1/mobile/sessions/sess-1:end")).toBe(true));
  });

  it("tells the worker when another user holds the device", async () => {
    stubService((c) => (c.path === "/api/v1/mobile/sessions" ? json(409, { code: "MOB-SESSION-ACTIVE", detail: "busy" }) : undefined));
    render(<DeviceApp deviceId="d1" userId="u1" warehouseId={WAREHOUSE} flows={[PICK_FLOW]} />);
    expect(await screen.findByText(/Another worker is signed in on this device/)).toBeInTheDocument();
  });

  it("makes no session call when no warehouse is provisioned", async () => {
    stubService(() => undefined);
    render(<DeviceApp deviceId="d1" userId="u1" flows={[PICK_FLOW]} />);
    await screen.findByLabelText(/^Scan/);
    expect(calls.some((c) => c.path.includes("/sessions"))).toBe(false);
  });

  it("queues the confirmation with the user who captured it and replays it with the session", async () => {
    stubService((c) => {
      if (c.path === "/api/v1/mobile/sessions") return json(201, { session, resumed: false });
      if (c.path === "/api/v1/mobile/offline:replay") return json(200, { status: "APPLIED" });
      return undefined;
    });
    const user = userEvent.setup();
    render(<DeviceApp deviceId="d1" userId="u1" warehouseId={WAREHOUSE} flows={[PICK_FLOW]} />);
    await waitFor(() => expect(calls.some((c) => c.path === "/api/v1/mobile/sessions")).toBe(true));
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    await user.type(await screen.findByLabelText(/^Scan/), "]C10109501234567891{Enter}");
    await user.type(await screen.findByLabelText("Value"), "5{Enter}");
    await user.click(await screen.findByRole("button", { name: "full" }));
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
    await user.click(await screen.findByRole("button", { name: /Sync 1 queued/ }));
    await waitFor(() => expect(calls.some((c) => c.path === "/api/v1/mobile/offline:replay")).toBe(true));
    const replay = calls.find((c) => c.path === "/api/v1/mobile/offline:replay");
    expect(replay?.body).toMatchObject({ idempotencyKey: "d1:1", userId: "u1", sessionId: "sess-1", payload: { userId: "u1", flowCode: "PICK" } });
    expect((replay?.body as { capturedAt: string }).capturedAt).toMatch(/^\d{4}-\d\d-\d\dT/);
    expect(await screen.findByText(/Synced 1; 0 waiting/)).toBeInTheDocument();
  });

  it("says why sync paused when another user queued the entries", async () => {
    stubService((c) => {
      if (c.path === "/api/v1/mobile/offline:replay") return json(403, { code: "MOB-NOT-OWNER", detail: "no" });
      return undefined;
    });
    const user = userEvent.setup();
    render(<DeviceApp deviceId="d1" userId="u1" flows={[PICK_FLOW]} />);
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    await user.type(await screen.findByLabelText(/^Scan/), "]C10109501234567891{Enter}");
    await user.type(await screen.findByLabelText("Value"), "5{Enter}");
    await user.click(await screen.findByRole("button", { name: "full" }));
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
    await user.click(await screen.findByRole("button", { name: /Sync 1 queued/ }));
    expect(await screen.findByText(/queued by another user/)).toBeInTheDocument();
  });
});

describe("label on receive (32-3)", () => {
  async function receive(user: ReturnType<typeof userEvent.setup>) {
    await user.type(await screen.findByLabelText(/^Scan/), `${SSCC}{Enter}`);
    await user.type(await screen.findByLabelText(/^Scan/), "A-01-02{Enter}");
    await user.click(await screen.findByRole("button", { name: "Confirm" }));
  }

  it("prints the LP label after put-away from the item's GS1 label", async () => {
    stubService((c) => (c.path === "/api/v1/print/jobs" ? json(201, { id: "j1", status: "PRINTED", printerCode: "DOCK-1", queueCode: "DOCK", templateCode: "LP_LABEL", format: "ZPL", attempts: 1 }) : undefined));
    const user = userEvent.setup();
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await receive(user);
    await user.type(await screen.findByLabelText("Item label (GTIN, lot, expiry)"), `${ITEM}{Enter}`);
    expect(await screen.findByText("Label printed on DOCK-1.")).toBeInTheDocument();
    const job = calls.find((c) => c.path === "/api/v1/print/jobs");
    expect(job?.body).toEqual({
      templateCode: "LP_LABEL",
      documentType: "LP_LABEL",
      data: { lp: "340123456789012340", item: "09501234567891", lot: "LOT42", expiry: "2027-06-30", sscc: "340123456789012340" },
    });
  });

  it("explains a missing routing rule and lets the worker skip the label", async () => {
    stubService((c) => (c.path === "/api/v1/print/jobs" ? json(422, { code: "MOB-0112", detail: "no printer" }) : undefined));
    const user = userEvent.setup();
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await receive(user);
    await user.type(await screen.findByLabelText("Item label (GTIN, lot, expiry)"), `${ITEM}{Enter}`);
    expect(await screen.findByText(/No printer is set up for LP labels/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Skip label" }));
    expect(screen.queryByLabelText("Item label (GTIN, lot, expiry)")).not.toBeInTheDocument();
  });

  it("rejects an item label without a lot before calling the print service", async () => {
    stubService(() => undefined);
    const user = userEvent.setup();
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await receive(user);
    await user.type(await screen.findByLabelText("Item label (GTIN, lot, expiry)"), "]C10109501234567891{Enter}");
    expect(await screen.findByText(/has no lot or expiry date/)).toBeInTheDocument();
    expect(calls.some((c) => c.path === "/api/v1/print/jobs")).toBe(false);
  });
});

describe("RFID and voice (32-4)", () => {
  it("answers a scan step from a forwarded tag read", async () => {
    stubService(() => undefined);
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await screen.findByLabelText(/^Scan/);
    // An SSCC-96 tag is the license plate; the same tag read again in the field is not a second scan.
    const round = { reads: [{ epc: "3174257BF4499602D2000000", rssi: -50 }] };
    act(() => void window.dispatchEvent(new CustomEvent("passage:rfid", { detail: round })));
    expect(await screen.findByText("Scan the put-away location")).toBeInTheDocument();
    act(() => void window.dispatchEvent(new CustomEvent("passage:rfid", { detail: round })));
    expect(screen.getByText("Scan the put-away location")).toBeInTheDocument();
  });

  it("reports an EPC it cannot read as MOB-0118", async () => {
    stubService(() => undefined);
    render(<DeviceApp deviceId="d1" userId="u1" flows={[RECEIVE_PUTAWAY_FLOW]} />);
    await screen.findByLabelText(/^Scan/);
    act(() => void window.dispatchEvent(new CustomEvent("passage:rfid", { detail: { reads: [{ epc: "E280689400004012345ABCDE" }] } })));
    expect(await screen.findByRole("alert")).toHaveTextContent("MOB-0118");
  });

  it("speaks the prompt and takes spoken digits and commands as the step's answers", async () => {
    stubService(() => undefined);
    let heard: (text: string) => void = () => undefined;
    const spoken: string[] = [];
    const voice: VoiceAdapter = {
      speak: (t) => spoken.push(t),
      onUtterance: (h) => {
        heard = h;
        return () => undefined;
      },
    };
    render(<DeviceApp deviceId="d1" userId="u1" flows={[PICK_FLOW]} voice={voice} />);
    await waitFor(() => expect(spoken).toContain("Scan the pick location"));
    act(() => heard("mumble"));
    expect(spoken).toContain("Say again.");
    // A spoken check string is not a location, so the scan rule refuses it like any other bad scan.
    act(() => heard("four two"));
    expect(await screen.findByRole("alert")).toHaveTextContent("MOB-0101");
  });

  it("builds an SGTIN round trip the device accepts", () => {
    expect(encodeSgtin96("80614141123458", 6789n, 7, 3)).toBe("3074257BF7194E4000001A85");
  });
});
