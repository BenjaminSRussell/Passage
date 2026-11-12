import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Manifest, ManifestCandidate } from "./api/client";
import { groupCandidates } from "./ManifestsPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const ID = "0195f0a0-0000-7000-8000-0000000000e1";
const U = (n: number) => `0195f0a0-0000-7000-8000-0000000002${String(n).padStart(2, "0")}`;
const LIST = "/api/v1/fulfillment/manifests";
const CANDIDATES = "/api/v1/fulfillment/manifest-candidates";
const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

function manifest(over: Partial<Manifest> = {}): Manifest {
  return {
    id: ID, manifestNumber: "MF-0000001", status: "CLOSED", legalEntityId: U(1), warehouseId: U(2), carrier: "ups", pickupDate: "2026-11-08", providerManifestId: null,
    parcelCount: 2, shipmentCount: 1, parcels: [], version: 1, createdAt: "2026-11-08T17:00:00Z", cancelledAt: null, ...over,
  };
}

function candidate(n: number, over: Partial<ManifestCandidate> = {}): ManifestCandidate {
  return {
    shipmentId: U(10 + n), shipmentNumber: `SH-000000${n}`, legalEntityId: U(1), warehouseId: U(2), orderId: U(20 + n), shipmentStatus: "SHIPPED", carrier: "ups",
    trackingNumber: `1Z00${n}`, labelledAt: "2026-11-08T10:00:00Z", ready: true, ...over,
  };
}

describe("carrier manifests list", () => {
  it("lists manifests and unmanifested parcels, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === LIST) return json(200, { items: [manifest(), manifest({ id: U(90), manifestNumber: "MF-0000002", status: "CANCELLED", providerManifestId: "UPS-9" })] });
      if (url.pathname === CANDIDATES) return json(200, { items: [candidate(1), candidate(2, { ready: false, shipmentStatus: "PACKED" })] });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/manifests", ["fulfillment.warehouse.view"]);
    const table = await findTable({ name: "Carrier manifests" });
    expect(within(table).getByText("MF-0000002")).toBeInTheDocument();
    expect(within(table).getByText("Cancelled")).toBeInTheDocument();
    const parcels = await findTable({ name: "Unmanifested parcels" });
    expect(within(parcels).getByText("1Z001")).toBeInTheDocument();
    expect(within(parcels).getByText("Waiting for packing slip")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.queryByRole("button", { name: "Close manifest" })).toBeNull();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "CLOSED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === LIST && c.url.searchParams.get("status") === "CLOSED")).toBe(true));
    fireEvent.change(screen.getByLabelText("Carrier"), { target: { value: "UPS" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === LIST && c.url.searchParams.get("carrier") === "ups")).toBe(true));
  });

  it("shows the empty states and the load errors", async () => {
    server.handler = (url) => (url.pathname === LIST || url.pathname === CANDIDATES ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/manifests");
    expect(await screen.findByText("No manifests yet")).toBeInTheDocument();
    expect(await screen.findByText("Every labelled parcel is on a manifest")).toBeInTheDocument();
  });

  it("explains a load failure", async () => {
    server.handler = (url) => (url.pathname === LIST ? json(403, { code: "AUTH-0003", detail: "missing duty" }) : url.pathname === CANDIDATES ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/manifests");
    expect(await findAlert()).toHaveTextContent(/You can't view manifests/);
  });

  it("closes a manifest for the chosen group with one key and reloads", async () => {
    let closed = false;
    server.handler = (url, init) => {
      if (init.method === "POST") {
        closed = true;
        return json(201, manifest({ parcelCount: 2, providerManifestId: "UPS-77" }));
      }
      if (url.pathname === LIST) return json(200, { items: closed ? [manifest()] : [] });
      if (url.pathname === CANDIDATES) return json(200, { items: closed ? [] : [candidate(1), candidate(2), candidate(3, { carrier: "dhl", warehouseId: U(3) })] });
      return undefined;
    };
    renderAt("/fulfillment/manifests", ["fulfillment.warehouse.view", "fulfillment.warehouse.ship"]);
    await screen.findByText("Parcels on no manifest");
    await waitFor(() => expect(within(screen.getByLabelText("Warehouse and carrier")).getAllByRole("option")).toHaveLength(2));
    fireEvent.change(screen.getByLabelText("Pickup date"), { target: { value: "2026-11-09" } });
    fireEvent.change(screen.getByLabelText("Carrier manifest reference"), { target: { value: " UPS-77 " } });
    fireEvent.click(screen.getByRole("button", { name: "Close manifest" }));
    expect(await screen.findByText(/Closed MF-0000001 with 2 parcels for ups/)).toBeInTheDocument();
    const post = calls("POST", "/manifests")[0];
    expect(idempotencyKey(post)).toBeTruthy();
    expect(post.body).toEqual({ warehouseId: U(2), legalEntityId: U(1), carrier: "ups", pickupDate: "2026-11-09", providerManifestId: "UPS-77" });
    await waitFor(() => expect(screen.getByRole("button", { name: "Close manifest" })).toBeDisabled());
  });

  it("sends the group that was picked", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, manifest({ carrier: "dhl" }));
      if (url.pathname === LIST) return json(200, { items: [] });
      if (url.pathname === CANDIDATES) return json(200, { items: [candidate(1), candidate(3, { carrier: "dhl", warehouseId: U(3) })] });
      return undefined;
    };
    renderAt("/fulfillment/manifests", ["fulfillment.warehouse.ship"]);
    await waitFor(() => expect(within(screen.getByLabelText("Warehouse and carrier")).getAllByRole("option")).toHaveLength(2));
    fireEvent.change(screen.getByLabelText("Warehouse and carrier"), { target: { value: `${U(3)}|${U(1)}|dhl` } });
    fireEvent.click(screen.getByRole("button", { name: "Close manifest" }));
    await waitFor(() => expect(calls("POST", "/manifests")).toHaveLength(1));
    expect(calls("POST", "/manifests")[0].body).toMatchObject({ warehouseId: U(3), carrier: "dhl" });
  });

  it("disables closing when only parcels waiting for their slip exist, and explains a refusal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0076", detail: "nothing" });
      if (url.pathname === LIST) return json(200, { items: [] });
      if (url.pathname === CANDIDATES) return json(200, { items: [candidate(1, { ready: false, shipmentStatus: "PACKED" })] });
      return undefined;
    };
    renderAt("/fulfillment/manifests", ["fulfillment.warehouse.ship"]);
    await screen.findByText("1Z001");
    expect(screen.getByRole("button", { name: "Close manifest" })).toBeDisabled();
  });

  it("explains FUL-0076 when another user took the parcels first", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0076", detail: "nothing" });
      if (url.pathname === LIST) return json(200, { items: [] });
      if (url.pathname === CANDIDATES) return json(200, { items: [candidate(1)] });
      return undefined;
    };
    renderAt("/fulfillment/manifests", ["fulfillment.warehouse.ship"]);
    await waitFor(() => expect(screen.getByRole("button", { name: "Close manifest" })).toBeEnabled());
    fireEvent.click(screen.getByRole("button", { name: "Close manifest" }));
    expect(await findAlert()).toHaveTextContent(/No shipped parcel of this carrier is waiting/);
  });

  it("groups parcels by warehouse, legal entity and carrier", () => {
    const groups = groupCandidates([candidate(1), candidate(2, { ready: false }), candidate(3, { carrier: "dhl" })]);
    expect(groups).toHaveLength(2);
    expect(groups.find((g) => g.carrier === "ups")).toMatchObject({ ready: 1, waiting: 1 });
    expect(groups.find((g) => g.carrier === "dhl")).toMatchObject({ ready: 1, waiting: 0 });
  });
});

describe("carrier manifest detail", () => {
  const parcels = [
    { shipmentId: U(11), shipmentNumber: "SH-0000001", orderId: U(21), trackingNumber: "1Z001", active: true },
    { shipmentId: U(11), shipmentNumber: "SH-0000001", orderId: U(21), trackingNumber: "1Z002", active: true },
  ];

  it("shows the parcels and cancels with the version read", async () => {
    let current = manifest({ parcels });
    server.handler = (url, init) => {
      if (init.method === "POST") {
        current = manifest({ parcels: parcels.map((p) => ({ ...p, active: false })), status: "CANCELLED", version: 2, cancelledAt: "2026-11-08T18:00:00Z" });
        return json(200, current);
      }
      if (url.pathname === `${LIST}/${ID}`) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/manifests/${ID}`, ["fulfillment.warehouse.view", "fulfillment.warehouse.ship"]);
    const table = await findTable({ name: "Manifest parcels" });
    expect(within(table).getByText("1Z002")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Cancel manifest" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel manifest", hidden: false }));
    expect(await screen.findByText(/This manifest was cancelled/)).toBeInTheDocument();
    const post = calls("POST", ":cancel")[0];
    expect(header(post, "If-Match")).toBe('"1"');
    expect(idempotencyKey(post)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel manifest" })).toBeNull();
  });

  it("explains a stale version", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(412, { code: "conflict_version", detail: "stale" });
      if (url.pathname === `${LIST}/${ID}`) return json(200, manifest({ parcels }));
      return undefined;
    };
    renderAt(`/fulfillment/manifests/${ID}`, ["fulfillment.warehouse.ship"]);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel manifest" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel manifest" }));
    expect(await findAlert()).toHaveTextContent(/Someone else changed this manifest/);
  });

  it("explains that a manifest the carrier collected from stays", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0078", detail: "collected" });
      if (url.pathname === `${LIST}/${ID}`) return json(200, manifest({ parcels }));
      return undefined;
    };
    renderAt(`/fulfillment/manifests/${ID}`, ["fulfillment.warehouse.ship"]);
    fireEvent.click(await screen.findByRole("button", { name: "Cancel manifest" }));
    fireEvent.click(await screen.findByRole("button", { name: "Cancel manifest" }));
    expect(await findAlert()).toHaveTextContent(/carrier has already collected parcels/);
  });

  it("hides the cancel from a viewer and shows a load error", async () => {
    server.handler = (url) => (url.pathname === `${LIST}/${ID}` ? json(200, manifest({ parcels })) : undefined);
    renderAt(`/fulfillment/manifests/${ID}`, ["fulfillment.warehouse.view"]);
    expect(await screen.findByText(/Cancelling a manifest needs the shipping duty/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel manifest" })).toBeNull();
  });

  it("explains a missing manifest", async () => {
    server.handler = (url) => (url.pathname === `${LIST}/${ID}` ? json(404, { code: "FUL-0077", detail: "not found" }) : undefined);
    renderAt(`/fulfillment/manifests/${ID}`);
    expect(await findAlert()).toHaveTextContent(/Could not load the manifest/);
  });
});
