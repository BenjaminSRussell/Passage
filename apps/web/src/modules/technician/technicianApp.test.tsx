import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { saveSnapshot } from "./domain/dayCache";
import { day, memoryStore, reverseCipher, visit } from "./domain/fixtures";
import { OfflineError, type SyncClient } from "./domain/sync";
import type { ActionBody, ActionOutcome } from "./domain/types";
import { TechnicianApp } from "./TechnicianApp";
import type { TechnicianDeps } from "./useTechnician";

function setup(opts: { offline?: boolean; cached?: boolean } = {}) {
  const store = memoryStore();
  const server = { down: opts.offline ?? false, sent: [] as ActionBody[] };
  const client: SyncClient = {
    async postActions(_w, _t, actions): Promise<ActionOutcome[]> {
      if (server.down) throw new OfflineError("offline");
      server.sent.push(...actions);
      return actions.map((a) => ({ id: a.id, kind: a.kind, outcome: "APPLIED" }));
    },
    async putCapture() {
      if (server.down) throw new OfflineError("offline");
    },
  };
  const deps: TechnicianDeps = {
    store,
    cipher: reverseCipher,
    client,
    loadDay: async () => {
      if (server.down) throw new Error("offline");
      return day([visit(), visit({ id: "00000000-0000-4000-8000-0000000000a2", number: "SWO-000042", assignedStart: "2026-10-26T18:00:00Z", assignedEnd: "2026-10-26T19:00:00Z", address: "88 County Road 12, Harlan, IA", checklist: [], lines: [], signature: { signed: false, exempt: true } })]);
    },
    loadTruck: async () => [{ productId: "00000000-0000-4000-8000-0000000000d1", unit: "EA", minQty: "2", maxQty: "6", onHandQty: "3", inboundQty: "0", short: true }],
    resolveName: async (entity) => (entity === "party" ? "Bluewater Municipal Utilities (C-10021)" : "Seal kit"),
    now: () => new Date("2026-10-26T15:00:00Z"),
    isOnline: () => !server.down,
  };
  return { deps, store, server, ready: opts.cached ? saveSnapshot(store, reverseCipher, { day: day(), names: { "00000000-0000-4000-8000-0000000000c1": "Cached Customer" }, products: {}, truck: [], savedAt: "2026-10-26T12:00:00Z" }) : Promise.resolve() };
}

describe("TechnicianApp", () => {
  it("shows the server's state once the queue is sent, not the day as it was first loaded", async () => {
    const { deps, server } = setup();
    deps.loadDay = async () => day([visit({ status: server.sent.some((a) => a.kind === "WORK_START") ? "IN_PROGRESS" : "DISPATCHED" })]);
    const user = userEvent.setup();
    render(<TechnicianApp deps={deps} />);
    await user.click(await screen.findByRole("button", { name: /Bluewater/ }));
    await user.click(screen.getByRole("button", { name: "Start work" }));
    await waitFor(() => expect(server.sent).toHaveLength(1));
    await waitFor(() => expect(screen.getByText("Everything sent")).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Put on hold" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start work" })).not.toBeInTheDocument();
  });

  it("shows the route with customer names, times in the technician's zone and the job status", async () => {
    const { deps } = setup();
    render(<TechnicianApp deps={deps} />);
    const route = await screen.findByRole("region", { name: "Route" });
    await waitFor(() => expect(within(route).getAllByText("Bluewater Municipal Utilities (C-10021)").length).toBe(2));
    expect(within(route).getByText("9:00 AM")).toBeInTheDocument();
    expect(within(route).getAllByText("Ready to start").length).toBe(2);
    expect(screen.getByText("Everything sent")).toBeInTheDocument();
    expect(screen.getByText("Online")).toBeInTheDocument();
  });

  it("opens from the cache with no connection, says it is offline, and queues work to send later", async () => {
    const { deps, server, ready } = setup({ offline: true, cached: true });
    await ready;
    const user = userEvent.setup();
    render(<TechnicianApp deps={deps} />);
    await user.click(await screen.findByRole("button", { name: /Cached Customer/ }));
    expect(screen.getByText("Offline")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Start work" }));
    await waitFor(() => expect(screen.getByText("1 waiting to send")).toBeInTheDocument());
    expect(screen.getAllByText("Working").length).toBeGreaterThan(0);
    expect(server.sent).toHaveLength(0);

    // Back online: the queued start goes out and the counter clears.
    server.down = false;
    await user.click(screen.getByRole("button", { name: "Send now" }));
    await waitFor(() => expect(screen.getByText("Everything sent")).toBeInTheDocument());
    expect(server.sent.map((a) => a.kind)).toEqual(["WORK_START"]);
  });

  it("blocks completion until the checklist, parts and signature are done, and says what is missing", async () => {
    const { deps, ready } = setup({ offline: true, cached: true });
    await ready;
    const user = userEvent.setup();
    render(<TechnicianApp deps={deps} />);
    await user.click(await screen.findByRole("button", { name: /Cached Customer/ }));
    await user.click(screen.getByRole("button", { name: "Start work" }));
    const complete = await screen.findByRole("button", { name: "Complete the job" });
    expect(complete).toBeDisabled();
    expect(screen.getByText('Tick "Lock out"')).toBeInTheDocument();
    expect(screen.getByText("Record the Seal kit you used")).toBeInTheDocument();
    expect(screen.getByText("Get the customer's signature")).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: /Lock out/ }));
    await user.click(screen.getByRole("button", { name: "Record" }));
    await waitFor(() => expect(screen.queryByText('Tick "Lock out"')).not.toBeInTheDocument());
    expect(screen.queryByText("Record the Seal kit you used")).not.toBeInTheDocument();
    expect(screen.getByText(/2 of 2 EA used/)).toBeInTheDocument();
  });

  it("tells the technician when the server rejected something it had queued", async () => {
    const { deps, server, ready } = setup({ offline: true, cached: true });
    await ready;
    deps.client.postActions = async (_w, _t, actions) => {
      server.sent.push(...actions);
      return actions.map((a) => ({ id: a.id, kind: a.kind, outcome: "REJECTED" as const, code: "SVC-WO-003" }));
    };
    const user = userEvent.setup();
    render(<TechnicianApp deps={deps} />);
    await user.click(await screen.findByRole("button", { name: /Cached Customer/ }));
    await user.click(screen.getByRole("button", { name: "Start work" }));
    const alert = await screen.findByText(/required checklist item was open/);
    expect(alert).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(/required checklist item was open/)).not.toBeInTheDocument();
  });
});
