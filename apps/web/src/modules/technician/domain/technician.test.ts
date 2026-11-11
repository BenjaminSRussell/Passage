import { describe, expect, it } from "vitest";
import { completionBlockers, nextMoves } from "./actions";
import { navigateUrl } from "./geo";
import { fitWithin, fromBase64, toBase64 } from "./image";
import { day, memoryStore, reverseCipher, visit } from "./fixtures";
import { project } from "./projection";
import { MAX_QUEUE, outcomeNote, TechQueue } from "./queue";
import { customerSummary } from "./summary";
import { OfflineError, RefusedError, syncQueue, type SyncClient } from "./sync";
import type { ActionBody, ActionOutcome, CaptureBody } from "./types";

const WO = visit().id;
const TECH = day().technician.id;
const action = (kind: ActionBody["kind"], at: string, extra: Partial<ActionBody> = {}): ActionBody => ({ id: `${kind}-${at}-${Math.random()}`, kind, deviceTime: `2026-10-26T${at}:00Z`, ...extra });

describe("moves and completion blockers", () => {
  it("offers the moves the server's merge accepts", () => {
    expect(nextMoves("DISPATCHED")).toEqual(["TRAVEL_START", "WORK_START"]);
    expect(nextMoves("TRAVELING")).toEqual(["WORK_START"]);
    expect(nextMoves("IN_PROGRESS")).toEqual(["HOLD", "COMPLETE"]);
    expect(nextMoves("ON_HOLD")).toEqual(["RESUME"]);
    expect(nextMoves("COMPLETED")).toEqual([]);
  });

  it("names what blocks completion: checklist, parts and signature (SVC-WO-001, 002, 003)", () => {
    expect(completionBlockers(visit())).toEqual(['Tick "Lock out"', "Record the Seal kit you used", "Get the customer's signature"]);
    const ready = visit({
      checklist: [{ id: "x", text: "Lock out", required: true, done: true }],
      lines: [{ id: "l", kind: "PART", qty: "2", consumedQty: "2" }],
      signature: { signed: true, exempt: false },
    });
    expect(completionBlockers(ready)).toEqual([]);
    expect(completionBlockers(visit({ checklist: [], lines: [], signature: { signed: false, exempt: true } }))).toEqual([]);
  });
});

describe("project: the visit with the queued actions applied", () => {
  const pending = (...a: ActionBody[]) => a.map((action) => ({ workOrderId: WO, action }));

  it("moves forward, adds parts, ticks items and keeps the latest tick", () => {
    const v = project(
      visit(),
      pending(
        action("WORK_START", "15:00"),
        action("PART_USED", "15:10", { lineId: visit().lines[0]!.id, qty: "1" }),
        action("CHECKLIST", "15:20", { itemId: visit().checklist[0]!.id, done: true }),
        action("CHECKLIST", "15:05", { itemId: visit().checklist[0]!.id, done: false }),
        action("NOTE", "15:30", { text: "replaced the seal" }),
        action("SIGNATURE", "15:40", { signedBy: "Pat", signatureRef: "capture:c1" }),
      ),
    );
    expect(v.status).toBe("IN_PROGRESS");
    expect(v.lines[0]!.consumedQty).toBe("1");
    expect(v.checklist[0]!.done).toBe(true);
    expect(v.technicianNote).toBe("replaced the seal");
    expect(v.signature).toMatchObject({ signed: true, signedBy: "Pat" });
  });

  it("does not move backwards, hold when not working, or complete from hold", () => {
    expect(project(visit({ status: "IN_PROGRESS" }), pending(action("TRAVEL_START", "15:00"))).status).toBe("IN_PROGRESS");
    expect(project(visit({ status: "TRAVELING" }), pending(action("HOLD", "15:00"))).status).toBe("TRAVELING");
    expect(project(visit({ status: "ON_HOLD" }), pending(action("COMPLETE", "15:00"))).status).toBe("ON_HOLD");
    expect(project(visit({ status: "ON_HOLD" }), pending(action("RESUME", "15:00"))).status).toBe("IN_PROGRESS");
    const done = project(visit({ status: "IN_PROGRESS" }), pending(action("COMPLETE", "16:00")));
    expect(done.status).toBe("COMPLETED");
    expect(done.completedAt).toBe("2026-10-26T16:00:00Z");
  });

  it("leaves a visit with nothing queued as it is, and other visits alone", () => {
    const v = visit();
    expect(project(v, [])).toBe(v);
    expect(project(v, [{ workOrderId: "other", action: action("WORK_START", "15:00") }])).toBe(v);
  });
});

describe("TechQueue", () => {
  it("keeps entries in order, seals them, and holds each image under its own key", async () => {
    const store = memoryStore();
    const q = new TechQueue(store, reverseCipher);
    await q.addAction(WO, TECH, action("WORK_START", "15:00"));
    const cap: CaptureBody = { captureId: "c1", kind: "PHOTO", contentType: "image/jpeg", data: "QUJD", capturedAt: "2026-10-26T15:05:00Z" };
    await q.addCapture(WO, TECH, cap, action("PHOTO", "15:05", { captureIds: ["c1"] }));
    expect(q.pending.map((e) => e.kind)).toEqual(["action", "capture", "action"]);
    expect(q.pending.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect([...store.data.keys()].sort()).toEqual(["blob:c1", "technician-queue"]);
    expect(JSON.stringify([...store.data.values()])).not.toContain("WORK_START");

    const again = new TechQueue(store, reverseCipher);
    await again.load();
    expect(again.pending.length).toBe(3);
    expect(await again.blob("c1")).toBe("QUJD");
    await again.remove([2]);
    expect(store.data.has("blob:c1")).toBe(false);
    expect(again.size).toBe(2);
  });

  it("refuses new entries when full instead of dropping old ones", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    for (let i = 0; i < MAX_QUEUE; i++) await q.addAction(WO, TECH, action("NOTE", "15:00"));
    await expect(q.addAction(WO, TECH, action("NOTE", "15:00"))).rejects.toThrow(/full/);
    expect(q.size).toBe(MAX_QUEUE);
  });

  it("keeps at most the last 50 notes", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    for (let i = 0; i < 55; i++) await q.note({ workOrderId: WO, kind: "NOTE", reason: `r${i}`, at: "t" });
    expect(q.attention.length).toBe(50);
    expect(q.attention[0]!.reason).toBe("r5");
    await q.dismiss(0);
    expect(q.attention.length).toBe(49);
  });

  it("words the codes a technician can act on", () => {
    expect(outcomeNote({ id: "1", kind: "COMPLETE", outcome: "REJECTED", code: "SVC-WO-001" })).toMatch(/signature/);
    expect(outcomeNote({ id: "1", kind: "COMPLETE", outcome: "REJECTED", code: "SVC-WO-003" })).toMatch(/checklist/);
    expect(outcomeNote({ id: "1", kind: "NOTE", outcome: "REJECTED", reason: "because" })).toBe("because");
  });
});

class FakeServer implements SyncClient {
  calls: string[] = [];
  down = false;
  refuseCapture = false;
  reject: Record<string, ActionOutcome> = {};
  async postActions(workOrderId: string, _t: string, actions: ActionBody[]): Promise<ActionOutcome[]> {
    if (this.down) throw new OfflineError("offline");
    this.calls.push(`actions:${workOrderId}:${actions.map((a) => a.kind).join(",")}`);
    return actions.map((a) => this.reject[a.id] ?? { id: a.id, kind: a.kind, outcome: "APPLIED" });
  }
  async putCapture(_w: string, _t: string, c: CaptureBody): Promise<void> {
    if (this.down) throw new OfflineError("offline");
    if (this.refuseCapture) throw new RefusedError("That image is too large.", 422, "SVC-CAP-001");
    this.calls.push(`capture:${c.captureId}:${c.data}`);
  }
}

describe("syncQueue", () => {
  it("sends an image before the action that names it, and consecutive actions of one work order together", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    await q.addAction(WO, TECH, action("WORK_START", "15:00"));
    await q.addAction(WO, TECH, action("NOTE", "15:01"));
    await q.addCapture(WO, TECH, { captureId: "c1", kind: "PHOTO", contentType: "image/jpeg", data: "QUJD", capturedAt: "2026-10-26T15:02:00Z" }, action("PHOTO", "15:02", { captureIds: ["c1"] }));
    await q.addAction("other", TECH, action("WORK_START", "15:03"));
    const server = new FakeServer();
    const report = await syncQueue(q, server);
    expect(server.calls).toEqual([`actions:${WO}:WORK_START,NOTE`, "capture:c1:QUJD", `actions:${WO}:PHOTO`, "actions:other:WORK_START"]);
    expect(report).toEqual({ sent: 5, rejected: 0, left: 0, offline: false });
    expect(q.size).toBe(0);
  });

  it("keeps everything when the connection is lost and sends it on the next run", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    await q.addAction(WO, TECH, action("WORK_START", "15:00"));
    const server = new FakeServer();
    server.down = true;
    expect(await syncQueue(q, server)).toMatchObject({ offline: true, left: 1 });
    server.down = false;
    expect(await syncQueue(q, server)).toMatchObject({ offline: false, left: 0, sent: 1 });
  });

  it("takes a rejected action out of the queue and tells the technician why", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    const complete = action("COMPLETE", "16:00");
    await q.addAction(WO, TECH, complete);
    const server = new FakeServer();
    server.reject[complete.id] = { id: complete.id, kind: "COMPLETE", outcome: "REJECTED", code: "SVC-WO-001" };
    const report = await syncQueue(q, server);
    expect(report).toMatchObject({ rejected: 1, left: 0 });
    expect(q.attention[0]!.reason).toMatch(/signature/);
  });

  it("drops an image the server refuses, with a note, and goes on", async () => {
    const q = new TechQueue(memoryStore(), reverseCipher);
    await q.addCapture(WO, TECH, { captureId: "c1", kind: "PHOTO", contentType: "image/jpeg", data: "QUJD", capturedAt: "2026-10-26T15:02:00Z" }, action("PHOTO", "15:02", { captureIds: ["c1"] }));
    const server = new FakeServer();
    server.refuseCapture = true;
    const report = await syncQueue(q, server);
    expect(report.left).toBe(0);
    expect(report.rejected).toBe(1);
    expect(q.attention[0]!.reason).toBe("That image is too large.");
  });
});

describe("images, navigation and the customer summary", () => {
  it("scales the long edge to 1,600 px and never enlarges", () => {
    expect(fitWithin(4000, 3000)).toEqual({ width: 1600, height: 1200 });
    expect(fitWithin(3000, 4000)).toEqual({ width: 1200, height: 1600 });
    expect(fitWithin(800, 600)).toEqual({ width: 800, height: 600 });
    expect(fitWithin(1600, 1600)).toEqual({ width: 1600, height: 1600 });
    expect(fitWithin(10000, 1)).toEqual({ width: 1600, height: 1 });
  });

  it("round-trips bytes through base64", () => {
    const bytes = new Uint8Array(70_000).map((_, i) => i % 251);
    expect(fromBase64(toBase64(bytes))).toEqual(bytes);
  });

  it("links navigation by position, else by address, else nothing", () => {
    expect(navigateUrl(visit())).toContain("destination=46.78,-92.1");
    expect(navigateUrl(visit({ latitude: null, longitude: null }))).toContain("destination=1200%20Harbor");
    expect(navigateUrl(visit({ latitude: null, longitude: null, address: " " }))).toBeUndefined();
  });

  it("summarizes the visit for the customer", () => {
    const s = customerSummary(
      visit({ status: "COMPLETED", technicianNote: "Replaced the seal.", signature: { signed: true, exempt: false, signedBy: "Pat Customer" }, lines: [{ id: "l", kind: "PART", description: "Seal kit", unit: "EA", qty: "2", consumedQty: "1" }] }),
      "Bluewater Utilities",
    );
    expect(s.rows).toContainEqual({ label: "Customer", value: "Bluewater Utilities" });
    expect(s.rows).toContainEqual({ label: "Status", value: "Done" });
    expect(s.parts).toEqual([{ text: "1 EA Seal kit" }]);
    expect(s.signedBy).toBe("Pat Customer");
    expect(customerSummary(visit(), "X").signedBy).toBeNull();
  });
});
