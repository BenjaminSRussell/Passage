import { findAlert } from "../../test-utils";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { WarehouseException } from "./api/client";
import { validateNote } from "./ExceptionDetailPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ID = "0195f0a0-0000-7000-8000-0000000000e1";
const ORDER = "0195f0a0-0000-7000-8000-0000000000a1";
const LINE = "0195f0a0-0000-7000-8000-0000000000a2";
const SHIPMENT = "0195f0a0-0000-7000-8000-0000000000a3";
const SLIP = "0195f0a0-0000-7000-8000-0000000000a4";

const exception = (over: Partial<WarehouseException> = {}): WarehouseException => ({
  id: ID,
  legalEntityId: ORDER,
  kind: "SLIP_POSTED_BEYOND_KEPT",
  status: "OPEN",
  orderId: ORDER,
  orderLineId: LINE,
  shipmentId: SHIPMENT,
  packingSlipId: SLIP,
  qty: "2.000000",
  keptQty: "4.000000",
  detail: "A packing slip shipped 6 of the order line after sales cancelled the remainder.",
  version: 1,
  createdAt: "2026-11-03T08:00:00Z",
  resolvedAt: null,
  resolvedBy: null,
  resolutionNote: null,
  ...over,
});

describe("exception note rule", () => {
  it("validates like the service", () => {
    expect(validateNote("  ")).toMatch(/Say what/);
    expect(validateNote("x".repeat(501))).toMatch(/at most 500/);
    expect(validateNote(" returned ")).toBeUndefined();
  });
});

describe("warehouse exceptions list", () => {
  it("shows the open cases with a link to each, and passes the status filter", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/fulfillment/exceptions" ? json(200, { items: [exception(), exception({ id: SLIP, kind: "PACKED_BEYOND_KEPT", status: "RESOLVED" })] }) : undefined);
    const { container } = renderAt("/fulfillment/exceptions", ["WarehouseView"]);
    expect(await screen.findByRole("link", { name: "Shipped beyond the kept quantity" })).toHaveAttribute("href", `/fulfillment/exceptions/${ID}`);
    expect(screen.getByRole("link", { name: "Packed beyond the kept quantity" })).toBeInTheDocument();
    expect(server.calls[0].url.searchParams.get("status")).toBe("OPEN");
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "" } });
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname.endsWith("/exceptions") && !c.url.searchParams.has("status"))).toBe(true));
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when nothing needs the supervisor", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/exceptions", ["WarehouseView"]);
    expect(await screen.findByText("No open exceptions")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "boom" }) : json(200, { items: [] }));
    renderAt("/fulfillment/exceptions", ["WarehouseView"]);
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No open exceptions")).toBeInTheDocument();
  });
});

describe("warehouse exception detail", () => {
  it("resolves with the version and a note, then reloads", async () => {
    let current = exception();
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":resolve") && init.method === "POST") {
        current = exception({ status: "RESOLVED", version: 2, resolvedAt: "2026-11-03T09:00:00Z", resolvedBy: "sup", resolutionNote: "returned" });
        return json(200, current);
      }
      return json(200, current);
    };
    const { container } = renderAt(`/fulfillment/exceptions/${ID}`, ["WarehouseView", "WarehouseRelease"]);
    expect(await screen.findByRole("heading", { name: "Shipped beyond the kept quantity" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the packing slip" })).toHaveAttribute("href", `/fulfillment/packing-slips/${SLIP}`);
    fireEvent.click(screen.getByRole("button", { name: "Mark resolved" }));
    expect(await screen.findByText("Say what was done about it.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("What was done"), { target: { value: " returned through RMA " } });
    fireEvent.click(screen.getByRole("button", { name: "Mark resolved" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Mark resolved" })).toBeNull());
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ note: "returned through RMA" });
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"1"');
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(await screen.findByText(/by sup/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers no resolve without WarehouseRelease, and none once resolved", async () => {
    server.handler = () => json(200, exception());
    const view = renderAt(`/fulfillment/exceptions/${ID}`, ["WarehouseView"]);
    expect(await screen.findByRole("heading", { name: "Shipped beyond the kept quantity" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark resolved" })).toBeNull();
    view.unmount();
    server.handler = () => json(200, exception({ status: "RESOLVED", version: 2, resolvedAt: "2026-11-03T09:00:00Z", resolvedBy: "sup", resolutionNote: "returned" }));
    renderAt(`/fulfillment/exceptions/${ID}`, ["WarehouseView", "WarehouseRelease"]);
    expect(await screen.findByText("returned")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark resolved" })).toBeNull();
  });

  it("names a stale version and shows a load error", async () => {
    server.handler = (url, init) =>
      url.pathname.endsWith(":resolve") && init.method === "POST" ? json(412, { code: "conflict_version", detail: "at version 2" }) : json(200, exception());
    renderAt(`/fulfillment/exceptions/${ID}`, ["WarehouseView", "WarehouseRelease"]);
    fireEvent.change(await screen.findByLabelText("What was done"), { target: { value: "done" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark resolved" }));
    expect((await findAlert()).textContent).toContain("Someone else changed this exception");
  });

  it("shows the problem when the exception cannot be loaded", async () => {
    server.handler = () => json(404, { code: "FUL-0067", detail: "exception not found" });
    renderAt(`/fulfillment/exceptions/${ID}`, ["WarehouseView"]);
    expect((await findAlert()).textContent).toContain("FUL-0067");
  });
});
