import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Wave, WaveLine } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { pickListOrder, validatePicker } from "./WaveActions";

useFakeServer();

const WH = "0195f0a0-0000-7000-8000-000000000007";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const WAVE = "0195f0a0-0000-7000-8000-0000000000e1";

const line = (id: string, zone: string | null, product: string, confirmed: boolean): WaveLine => ({
  shipmentLineId: id,
  shipmentId: SH,
  shipmentNumber: "SH-0000001",
  productId: product,
  unit: "EA",
  zone,
  qtyPlanned: "4",
  qtyPicked: confirmed ? "4" : "0",
  pickConfirmed: confirmed,
  shortPickReason: null,
});

const wave = (over: Partial<Wave> = {}, lines: WaveLine[] = [line("l1", "B2", ID, false), line("l2", "A1", ID, false)]): Wave => ({
  id: WAVE,
  waveNumber: "WV-0000001",
  legalEntityId: ID,
  warehouseId: WH,
  groupBy: "CARRIER",
  groupKey: "UPS",
  status: "OPEN",
  lineCount: lines.length,
  orderCount: 1,
  createdAt: "2026-10-27T08:00:00Z",
  version: 1,
  picker: null,
  assignedAt: null,
  closedAt: null,
  shipments: [{ id: SH, shipmentNumber: "SH-0000001", orderId: ID, customerId: ID, status: "PICKING", carrierCode: "UPS", plannedShipDate: "2026-10-27T00:00:00Z", lineCount: lines.length }],
  lines,
  ...over,
});

const posts = (suffix: string) => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

describe("wave picker rules", () => {
  it("validates like the service", () => {
    expect(validatePicker("  ")).toMatch(/Enter/);
    expect(validatePicker("x".repeat(65))).toMatch(/at most 64/);
    expect(validatePicker("a\u0007b")).toMatch(/control/);
    expect(validatePicker(" anna ")).toBeUndefined();
  });

  it("orders the pick list by zone, unzoned last", () => {
    const ordered = pickListOrder([line("l1", null, ID, false), line("l2", "B2", ID, false), line("l3", "A1", ID, false)]);
    expect(ordered.map((l) => l.shipmentLineId)).toEqual(["l3", "l2", "l1"]);
  });
});

describe("assigning a wave (FR-28.13)", () => {
  it("assigns with the wave version, reloads and shows the picker", async () => {
    let current = wave();
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}:assign` && init.method === "POST") {
        current = wave({ picker: "anna", version: 2 });
        return json(200, current);
      }
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}`) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByRole("heading", { name: "Assign the wave" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(await screen.findByText("Enter the picker's user name or badge.")).toBeInTheDocument();
    expect(posts(":assign")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Picker"), { target: { value: " anna " } });
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect(await screen.findByText("Wave WV-0000001 is assigned to anna.")).toBeInTheDocument();
    const call = posts(":assign")[0];
    expect(call.body).toEqual({ picker: "anna" });
    expect(header(call, "If-Match")).toBe('"1"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "Reassign the wave" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("tells the user the wave changed under them (412)", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":assign") && init.method === "POST") return json(412, { code: "conflict_version", detail: "wave is at version 3" });
      return json(200, wave());
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    fireEvent.change(await screen.findByLabelText("Picker"), { target: { value: "ben" } });
    fireEvent.click(screen.getByRole("button", { name: "Assign" }));
    expect((await findAlert()).textContent).toContain("Someone else changed this wave");
  });

  it("offers no assignment on a closed wave", async () => {
    server.handler = () => json(200, wave({ status: "CLOSED", picker: "anna", closedAt: "2026-10-27T10:00:00Z", version: 4 }, [line("l1", "A1", ID, true)]));
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByRole("heading", { name: "Wave WV-0000001" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Assign" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reassign" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Close wave" })).toBeNull();
    expect(screen.getByText("Closed", { selector: "span" })).toBeInTheDocument();
  });

  it("offers no wave pick on a closed wave, even with a line left unconfirmed", async () => {
    server.handler = () => json(200, wave({ status: "CLOSED", version: 4 }, [line("l1", "A1", ID, true), line("l2", "B2", ID, false)]));
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByRole("heading", { name: "Wave WV-0000001" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Confirm picks for the wave" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Pick list" })).toBeInTheDocument();
  });
});

describe("closing a wave", () => {
  it("confirms, sends the version and reloads", async () => {
    let current = wave({ picker: "anna", version: 3 }, [line("l1", "A1", ID, true)]);
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/fulfillment/waves/${WAVE}:close` && init.method === "POST") {
        current = wave({ status: "CLOSED", picker: "anna", version: 4, closedAt: "2026-10-27T10:00:00Z" }, [line("l1", "A1", ID, true)]);
        return json(200, current);
      }
      return json(200, current);
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close wave" }));
    expect(posts(":close")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Yes, close it" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Close wave" })).toBeNull());
    const call = posts(":close")[0];
    expect(header(call, "If-Match")).toBe('"3"');
    expect(idempotencyKey(call)).toBeTruthy();
  });

  it("shows FUL-0055 when a line is still to pick", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":close") && init.method === "POST") return json(422, { code: "FUL-0055", detail: "wave WV-0000001 still has 2 line(s) to pick" });
      return json(200, wave());
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    expect(await screen.findByText("2 lines are still to pick.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Close wave" }));
    fireEvent.click(screen.getByRole("button", { name: "Yes, close it" }));
    expect((await findAlert()).textContent).toContain("FUL-0055");
  });
});

describe("pick list and wave filters", () => {
  it("prints the pick list in zone order under the picker's name", async () => {
    server.handler = () => json(200, wave({ picker: "anna" }));
    let printed = 0;
    window.print = () => {
      printed += 1;
    };
    renderAt(`/fulfillment/waves/${WAVE}`);
    const table = await screen.findByRole("table", { name: "Pick list" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0].textContent).toContain("A1");
    expect(rows[1].textContent).toContain("B2");
    expect(screen.getByText(/picker anna/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Print pick list" }));
    expect(printed).toBe(1);
  });

  it("filters the list by status, picker and unassigned", async () => {
    server.handler = () => json(200, { items: [{ ...wave(), picker: "anna", lines: undefined, shipments: undefined }] });
    renderAt("/fulfillment/waves");
    expect(await screen.findByText("anna")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "OPEN" } });
    fireEvent.change(screen.getByLabelText("Picker"), { target: { value: "anna" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.search.includes("status=OPEN") && c.url.search.includes("picker=anna"))).toBe(true));
    fireEvent.change(screen.getByLabelText("Picker"), { target: { value: "-" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.search.includes("unassigned=true") && !c.url.search.includes("picker="))).toBe(true));
  });

  it("says no wave matches the filter", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/waves");
    fireEvent.change(await screen.findByLabelText("Status"), { target: { value: "CLOSED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("No waves match")).toBeInTheDocument();
  });
});
