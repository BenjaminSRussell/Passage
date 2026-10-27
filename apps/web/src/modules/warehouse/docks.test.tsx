import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Appointment, DockDoor } from "./api/client";
import { localInstant, today, validateAppointmentDraft } from "./DockPages";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";
const D1: DockDoor = { id: "0190f0a0-0000-7000-8000-000000000801", warehouseId: WH, code: "D1", createdAt: "2026-10-27T08:00:00Z" };
const DAY = today();

const booked: Appointment = {
  id: "0190f0a0-0000-7000-8000-000000000811",
  warehouseId: WH,
  doorId: D1.id,
  doorCode: "D1",
  direction: "INBOUND",
  reference: "ASN-1",
  start: localInstant(DAY, "09:00"),
  end: localInstant(DAY, "09:45"),
  createdAt: "2026-10-27T08:00:00Z",
};

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("DockSchedulePage", () => {
  it("lists the day's appointments in local time and the doors", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/warehouse/dock-doors") return json(200, { items: [D1] });
      if (url.pathname === "/api/v1/warehouse/dock-appointments") return json(200, { items: [booked] });
      return undefined;
    };
    const { container } = renderAt(`/warehouse/docks?warehouse=${WH}`);
    const table = await screen.findByRole("table", { name: "Appointments" });
    await waitFor(() => expect(table).toHaveTextContent("09:00–09:45"));
    expect(table).toHaveTextContent("ASN-1");
    expect(table).toHaveTextContent("Inbound");
    expect(await screen.findByRole("table", { name: "Dock doors" })).toHaveTextContent("D1");
    const call = server.calls.find((c) => c.url.pathname.endsWith("dock-appointments"));
    expect(call?.url.searchParams.get("from")).toBe(localInstant(DAY, "00:00"));
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Door"), { target: { value: D1.id } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("doorId")).toBe(D1.id));
  });

  it("shows the empty states and adds a door", async () => {
    let doors: DockDoor[] = [];
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/warehouse/dock-doors" && init.method === "POST") {
        doors = [D1];
        return json(201, D1);
      }
      if (url.pathname === "/api/v1/warehouse/dock-doors") return json(200, { items: doors });
      if (url.pathname === "/api/v1/warehouse/dock-appointments") return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/warehouse/docks?warehouse=${WH}`);
    expect(await screen.findByText("No appointments")).toBeInTheDocument();
    expect(await screen.findByText("No doors")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add door" }));
    expect(await screen.findByText("Enter the door code, at most 20 characters.")).toBeInTheDocument();
    fill("New door code", " D1 ");
    fireEvent.click(screen.getByRole("button", { name: "Add door" }));
    await waitFor(() => expect(screen.getByRole("table", { name: "Dock doors" })).toHaveTextContent("D1"));
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ warehouseId: WH, code: "D1" });
    expect(post && idempotencyKey(post)).toBeTruthy();
  });

  it("shows a schedule load failure", async () => {
    server.handler = (url) =>
      url.pathname.endsWith("dock-appointments") ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] });
    renderAt(`/warehouse/docks?warehouse=${WH}`);
    expect(await screen.findByText("internal error (COM-0500)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("NewAppointmentPage", () => {
  it("validates the window, books the door and returns to the schedule", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/warehouse/dock-doors") return json(200, { items: [D1] });
      if (url.pathname === "/api/v1/warehouse/dock-appointments" && init.method === "POST") return json(201, booked);
      if (url.pathname === "/api/v1/warehouse/dock-appointments") return json(200, { items: [booked] });
      return undefined;
    };
    const { container } = renderAt(`/warehouse/docks/new?warehouse=${WH}`);
    await screen.findByRole("option", { name: "D1" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Book appointment" }));
    expect(await screen.findByText("Choose a door.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Door"), { target: { value: D1.id } });
    fill("Reference", "ASN-1");
    fill("Start", "09:10");
    fill("End", "09:45");
    fireEvent.click(screen.getByRole("button", { name: "Book appointment" }));
    expect(await screen.findByText("Enter a start on the quarter hour, such as 09:00 or 09:15.")).toBeInTheDocument();
    fill("Start", "09:00");
    fireEvent.click(screen.getByRole("button", { name: "Book appointment" }));

    expect(await screen.findByRole("heading", { name: "Dock schedule" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({
      warehouseId: WH,
      doorId: D1.id,
      direction: "INBOUND",
      reference: "ASN-1",
      start: localInstant(DAY, "09:00"),
      end: localInstant(DAY, "09:45"),
    });
    expect(post && idempotencyKey(post)).toBeTruthy();
  });

  it("shows an overlap refusal (AC-31.8)", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/warehouse/dock-doors") return json(200, { items: [D1] });
      if (init.method === "POST") return json(409, { code: "WMS-0108", detail: "Door D1 is already booked 09:00–09:45 (DOOR_SLOT_OVERLAP, appointment ASN-1)." });
      return undefined;
    };
    renderAt(`/warehouse/docks/new?warehouse=${WH}`);
    await screen.findByRole("option", { name: "D1" });
    fireEvent.change(screen.getByLabelText("Door"), { target: { value: D1.id } });
    fireEvent.change(screen.getByLabelText("Direction"), { target: { value: "OUTBOUND" } });
    fill("Reference", "LOAD-2");
    fill("Start", "09:30");
    fill("End", "10:00");
    fireEvent.click(screen.getByRole("button", { name: "Book appointment" }));
    const alert = await screen.findByText(/DOOR_SLOT_OVERLAP/);
    expect(alert).toHaveTextContent("(WMS-0108)");
    expect(screen.queryByText("Choose a door.")).toBeNull();
    expect(screen.getByRole("heading", { name: "Book a dock appointment" })).toBeInTheDocument();
  });

  it("refuses an end that is not after the start", () => {
    const errors = validateAppointmentDraft({ warehouseId: WH, doorId: D1.id, direction: "INBOUND", reference: "A", date: DAY, start: "10:00", end: "09:45" });
    expect(errors.end).toBe("The end must be after the start on the same day.");
    expect(errors.start).toBeUndefined();
  });
});
