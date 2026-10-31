import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer, type Server } from "node:http";

import { Dispatcher, MemoryDeliveryStore, httpTransport, type DeliveryStore, type HubEvent } from "../src/dispatcher.ts";
import { verify } from "../src/webhook.ts";

// AC-35.3 / AC-35.4 against a real HTTP receiver on localhost. The receiver is a
// consumer that keeps an order projection: it applies an event only when its
// sequence is the next one for the order and ignores duplicates, so it shows
// at-least-once delivery (duplicates are harmless, losses are not) and whether
// a replay rebuilds the same state. Time is virtual: the dispatcher and the
// receiver's signature check read the same clock, which the driver moves to the
// next due retry.

const SECRET = "whsec_demo";

// Three orders. A's sequence 3 is stamped one second before its sequence 2
// (a producer clock running behind).
const archive: HubEvent[] = [
  { eventId: "a1", aggregateId: "A", sequence: 1, occurredAt: 1000, type: "dev.passage.sales.order.created.v1", data: {} },
  { eventId: "b1", aggregateId: "B", sequence: 1, occurredAt: 1001, type: "dev.passage.sales.order.created.v1", data: {} },
  { eventId: "a3", aggregateId: "A", sequence: 3, occurredAt: 1002, type: "dev.passage.sales.order.line_added.v1", data: { qty: 3 } },
  { eventId: "a2", aggregateId: "A", sequence: 2, occurredAt: 1003, type: "dev.passage.sales.order.line_added.v1", data: { qty: 5 } },
  { eventId: "c1", aggregateId: "C", sequence: 1, occurredAt: 1004, type: "dev.passage.sales.order.created.v1", data: {} },
  { eventId: "b2", aggregateId: "B", sequence: 2, occurredAt: 1005, type: "dev.passage.sales.order.line_added.v1", data: { qty: 10 } },
  { eventId: "c2", aggregateId: "C", sequence: 2, occurredAt: 1006, type: "dev.passage.sales.order.line_added.v1", data: { qty: 2 } },
  { eventId: "a4", aggregateId: "A", sequence: 4, occurredAt: 1007, type: "dev.passage.sales.order.shipped.v1", data: {} },
  { eventId: "b3", aggregateId: "B", sequence: 3, occurredAt: 1008, type: "dev.passage.sales.order.cancelled.v1", data: {} },
];

// Worked by hand from the events above.
const expectedState = {
  A: { seq: 4, status: "shipped", qty: 8 },
  B: { seq: 3, status: "cancelled", qty: 10 },
  C: { seq: 2, status: "open", qty: 2 },
};

type Reply = number | "drop" | "apply-then-500";

class Clock {
  now = 2_000_000_000;
}

class Receiver {
  state: Record<string, { seq: number; status: string; qty: number }> = {};
  attempts: { eventId: string; at: number; replay: boolean }[] = [];
  badSignatures = 0;
  #script: (eventId: string, attempt: number) => Reply;
  #server: Server;
  url = "";

  constructor(clock: Clock, script: (eventId: string, attempt: number) => Reply = () => 200) {
    this.#script = script;
    this.#server = createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        const body = Buffer.concat(chunks).toString("utf8");
        if (!verify(String(req.headers["passage-signature"] ?? ""), SECRET, body, clock.now)) {
          this.badSignatures += 1;
          res.writeHead(401).end();
          return;
        }
        const ce = JSON.parse(body) as { id: string; subject: string; type: string; passagesequence: number; data: { qty?: number } };
        const attempt = this.attempts.filter((a) => a.eventId === ce.id).length + 1;
        this.attempts.push({ eventId: ce.id, at: clock.now, replay: req.headers["ce-passagereplay"] === "true" });
        const reply = this.#script(ce.id, attempt);
        if (reply === "drop") {
          req.socket.destroy();
          return;
        }
        if (reply === 200 || reply === "apply-then-500") {
          this.#apply(ce);
        }
        res.writeHead(reply === "apply-then-500" ? 500 : reply).end();
      });
    });
  }

  #apply(ce: { subject: string; type: string; passagesequence: number; data: { qty?: number } }): void {
    const cur = this.state[ce.subject] ?? { seq: 0, status: "", qty: 0 };
    if (ce.passagesequence !== cur.seq + 1) {
      return; // a duplicate, or a gap the consumer waits to have filled
    }
    const kind = ce.type.split(".")[4];
    const next = { ...cur, seq: ce.passagesequence };
    if (kind === "created") next.status = "open";
    if (kind === "line_added") next.qty += ce.data.qty ?? 0;
    if (kind === "shipped") next.status = "shipped";
    if (kind === "cancelled") next.status = "cancelled";
    this.state[ce.subject] = next;
  }

  async start(): Promise<void> {
    await new Promise<void>((resolve) => this.#server.listen(0, "127.0.0.1", resolve));
    const addr = this.#server.address();
    this.url = `http://127.0.0.1:${typeof addr === "object" && addr ? addr.port : 0}/hook`;
  }

  async stop(): Promise<void> {
    this.#server.closeAllConnections();
    await new Promise<void>((resolve) => this.#server.close(() => resolve()));
  }

  times(eventId: string): number[] {
    return this.attempts.filter((a) => a.eventId === eventId).map((a) => a.at);
  }
}

async function drain(d: Dispatcher, clock: Clock): Promise<void> {
  for (let i = 0; i < 500; i++) {
    const due = d.nextDueAt();
    if (due === null) return;
    clock.now = Math.max(clock.now, due);
    await d.tick();
  }
  throw new Error("dispatcher did not drain");
}

function dispatcher(receiver: Receiver, clock: Clock, store: DeliveryStore = new MemoryDeliveryStore()): Dispatcher {
  return new Dispatcher({
    subscription: { id: "sub-1", url: receiver.url, secret: SECRET },
    store,
    transport: httpTransport(2000),
    clock: () => clock.now,
  });
}

test("AC-35.3: failures, a dropped connection and a lost 2xx are retried on the schedule; every event arrives at least once, in order per aggregate", async () => {
  const clock = new Clock();
  const receiver = new Receiver(clock, (id, attempt) => {
    if (id === "a2") return attempt <= 2 ? 503 : 200; // two failures, then delivered
    if (id === "b2") return attempt === 1 ? "drop" : attempt === 2 ? "apply-then-500" : 200; // the 2nd attempt's 2xx is lost
    if (id === "c1") return attempt === 1 ? 429 : 200;
    return 200;
  });
  await receiver.start();
  try {
    const store = new MemoryDeliveryStore();
    const start = clock.now;
    const d = dispatcher(receiver, clock, store);
    for (const e of archive) d.enqueue(e);
    await drain(d, clock);

    assert.deepEqual(receiver.state, expectedState);
    assert.equal(receiver.badSignatures, 0);
    assert.deepEqual(store.pending("sub-1"), []);
    assert.deepEqual(store.deadLetters("sub-1"), []);
    // Attempts at +0, +10 s, +10+60 s (BR-35.7 schedule).
    assert.deepEqual(receiver.times("a2").map((t) => t - start), [0, 10, 70]);
    assert.deepEqual(receiver.times("b2").map((t) => t - start), [0, 10, 70]);
    // b2 reached the receiver three times and was applied once.
    assert.equal(receiver.times("b2").length, 3);
    for (const e of archive) assert.ok(receiver.times(e.eventId).length >= 1, `${e.eventId} never delivered`);
    // A's sequence 3 is not attempted before sequence 2 has been delivered.
    assert.ok(receiver.times("a3")[0] >= receiver.times("a2")[2]);
    assert.ok(receiver.times("a4")[0] >= receiver.times("a3")[0]);
    // A failing a2 does not hold back the other orders.
    assert.equal(receiver.times("c1")[0], start);
  } finally {
    await receiver.stop();
  }
});

test("AC-35.3: a dispatcher that crashes after the receiver's 2xx but before recording it sends the event again", async () => {
  const clock = new Clock();
  const receiver = new Receiver(clock);
  await receiver.start();
  try {
    const store = new MemoryDeliveryStore();
    let crashed = false;
    const crashing: DeliveryStore = {
      pending: (id) => store.pending(id),
      put: (id, dl) => store.put(id, dl),
      remove: (id, eventId) => {
        if (eventId === "c2" && !crashed) {
          crashed = true;
          throw new Error("process killed");
        }
        store.remove(id, eventId);
      },
      deadLetter: (id, letter) => store.deadLetter(id, letter),
      deadLetters: (id) => store.deadLetters(id),
      isPaused: (id) => store.isPaused(id),
      setPaused: (id, paused) => store.setPaused(id, paused),
    };
    const first = dispatcher(receiver, clock, crashing);
    for (const e of archive) first.enqueue(e);
    await assert.rejects(drain(first, clock), /process killed/);
    assert.equal(receiver.times("c2").length, 1);

    // A new process on the same store finishes the work.
    await drain(dispatcher(receiver, clock, store), clock);
    assert.equal(receiver.times("c2").length, 2);
    assert.deepEqual(receiver.state, expectedState);
    assert.deepEqual(store.pending("sub-1"), []);
  } finally {
    await receiver.stop();
  }
});

test("AC-35.3/AC-35.4: an event refused nine times is dead-lettered (HUB-0115); a replay after the fix rebuilds the full state", async () => {
  const clock = new Clock();
  let fixed = false;
  const receiver = new Receiver(clock, (id) => (id === "b2" && !fixed ? 503 : 200));
  await receiver.start();
  try {
    const store = new MemoryDeliveryStore();
    const start = clock.now;
    const d = dispatcher(receiver, clock, store);
    for (const e of archive) d.enqueue(e);
    await drain(d, clock);

    const dead = store.deadLetters("sub-1");
    assert.equal(dead.length, 1);
    assert.equal(dead[0].event.eventId, "b2");
    assert.equal(dead[0].attempts, 9);
    assert.equal(dead[0].lastStatus, 503);
    assert.equal(dead[0].code, "HUB-0115");
    // First attempt, then the 8 retries: 10 s, 1 m, 5 m, 30 m, 2 h, 6 h, 12 h, 24 h after each other.
    assert.deepEqual(receiver.times("b2").map((t) => t - start), [0, 10, 70, 370, 2170, 9370, 30970, 74170, 160570]);
    // b3 was delivered once b2 left the queue; the consumer could not apply it over the gap.
    assert.ok(receiver.times("b3").length === 1);
    assert.deepEqual(receiver.state.B, { seq: 1, status: "open", qty: 0 });
    assert.deepEqual(receiver.state.A, expectedState.A);
    assert.deepEqual(receiver.state.C, expectedState.C);

    fixed = true;
    const before = receiver.attempts.length;
    assert.equal(d.replay(archive, 0, 4), archive.length);
    await drain(d, clock);
    assert.deepEqual(receiver.state, expectedState);
    assert.ok(receiver.attempts.slice(before).every((a) => a.replay), "replayed deliveries carry ce-passagereplay: true");
    assert.equal(receiver.attempts.length - before, archive.length);
  } finally {
    await receiver.stop();
  }
});

test("AC-35.4: replaying the archive into an empty consumer reproduces the live state; replay from T into a consumer holding the state before T does too", async () => {
  const clock = new Clock();
  const live = new Receiver(clock, (id, attempt) => (id === "a2" && attempt === 1 ? 500 : 200));
  const fresh = new Receiver(clock);
  const partial = new Receiver(clock);
  await Promise.all([live.start(), fresh.start(), partial.start()]);
  try {
    const d1 = dispatcher(live, clock);
    for (const e of archive) d1.enqueue(e);
    await drain(d1, clock);
    assert.deepEqual(live.state, expectedState);

    const d2 = dispatcher(fresh, clock);
    d2.replay(archive, 0, 2); // five pages of at most two events
    await drain(d2, clock);
    assert.deepEqual(fresh.state, live.state);

    // The consumer already applied everything stamped before 1005; replay from 1005.
    const d3 = dispatcher(partial, clock);
    for (const e of archive.filter((x) => x.occurredAt < 1005)) d3.enqueue(e);
    await drain(d3, clock);
    assert.deepEqual(partial.state.B, { seq: 1, status: "open", qty: 0 });
    d3.replay(archive, 1005);
    await drain(d3, clock);
    assert.deepEqual(partial.state, live.state);
  } finally {
    await Promise.all([live.stop(), fresh.stop(), partial.stop()]);
  }
});

test("410 Gone pauses the subscription (HUB-0114): nothing more is sent until it is resumed", async () => {
  const clock = new Clock();
  let gone = true;
  const receiver = new Receiver(clock, () => (gone ? 410 : 200));
  await receiver.start();
  try {
    const d = dispatcher(receiver, clock);
    for (const e of archive) d.enqueue(e);
    const first = await d.tick();
    assert.equal(first.paused, true);
    assert.equal(first.attempted, 1);
    assert.equal(d.nextDueAt(), null);
    clock.now += 86_400;
    assert.deepEqual(await d.tick(), { attempted: 0, delivered: 0, retried: 0, deadLettered: 0, paused: true });
    assert.equal(receiver.attempts.length, 1);

    gone = false;
    d.resume();
    await drain(d, clock);
    assert.deepEqual(receiver.state, expectedState);
  } finally {
    await receiver.stop();
  }
});
