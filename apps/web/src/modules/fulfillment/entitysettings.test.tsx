import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LegalEntitySetting, Shipment } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE1 = "0195f0a0-0000-7000-8000-0000000000a1";
const LE2 = "0195f0a0-0000-7000-8000-0000000000a2";
const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";
const LIST = "/api/v1/fulfillment/legal-entity-settings";

const calls = (method: string, suffix: string) => server.calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const rows: LegalEntitySetting[] = [
  { legalEntityId: LE1, cogsAtShipment: false, asnEnabled: false, version: 0, updatedAt: null },
  { legalEntityId: LE2, cogsAtShipment: true, asnEnabled: false, version: 2, updatedAt: "2026-10-27T08:00:00Z" },
];

describe("posting settings page", () => {
  it("lists the legal entities and saves the flags with the version read", async () => {
    let current = rows;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        current = [rows[0], { ...rows[1], asnEnabled: true, version: 3 }];
        return json(200, current[1]);
      }
      if (url.pathname === LIST) return json(200, { items: current });
      return undefined;
    };
    const { container } = renderAt("/fulfillment/legal-entity-settings");
    const table = await findTable({ name: "Posting settings" });
    expect(within(table).getByText("At shipment (Dr 5000)")).toBeInTheDocument();
    expect(within(table).getByText("Delivered not invoiced (Dr 1405)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: LE2 }));
    expect(screen.getByLabelText("Post cost of goods sold at shipment")).toBeChecked();
    // A warehouse supervisor without the ledger setup duty cannot change the COGS policy, only the ASN.
    expect(screen.getByLabelText("Post cost of goods sold at shipment")).toBeDisabled();
    fireEvent.click(screen.getByLabelText("Send an advance ship notice (ASN)"));
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/Saved\./)).toBeInTheDocument();
    const put = calls("PUT", LE2)[0];
    expect(header(put, "If-Match")).toBe('"2"');
    expect(idempotencyKey(put)).toBeTruthy();
    expect(put.body).toEqual({ cogsAtShipment: true, asnEnabled: true });
    const after = await findTable({ name: "Posting settings" });
    await waitFor(() => expect(within(after).getByText("Sent with each post")).toBeInTheDocument());
  });

  it("checks the legal entity id and shows a stale version and a scope refusal", async () => {
    let status = 412;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        return status === 412
          ? json(412, { type: "about:blank", title: "Precondition failed", status: 412, code: "conflict_version", detail: "stale" })
          : json(422, { type: "about:blank", title: "Unprocessable", status: 422, code: "FUL-0058", detail: "outside your scope" });
      }
      if (url.pathname === LIST) return json(200, { items: rows });
      return undefined;
    };
    renderAt("/fulfillment/legal-entity-settings");
    const table = await findTable({ name: "Posting settings" });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/Enter the legal entity id/)).toBeInTheDocument();
    expect(calls("PUT", "")).toHaveLength(0);
    fireEvent.click(within(table).getByRole("button", { name: LE2 }));
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/Someone else changed this setting/)).toBeInTheDocument();
    status = 422;
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/outside your legal-entity scope/)).toBeInTheDocument();
  });

  it("lets a holder of the ledger setup duty change the COGS policy and explains the refusal without it", async () => {
    let refuse = false;
    server.handler = (url, init) => {
      if (init.method === "PUT") {
        return refuse
          ? json(403, { type: "about:blank", title: "Forbidden", status: 403, code: "AUTH-0003", detail: "missing duty" })
          : json(200, { ...rows[0], cogsAtShipment: true, version: 1 });
      }
      if (url.pathname === LIST) return json(200, { items: rows });
      return undefined;
    };
    renderAt("/fulfillment/legal-entity-settings", ["fulfillment.warehouse.view", "fulfillment.warehouse.release", "ledger.setup.write"]);
    const table = await findTable({ name: "Posting settings" });
    fireEvent.click(within(table).getByRole("button", { name: LE1 }));
    const cogs = screen.getByLabelText("Post cost of goods sold at shipment");
    expect(cogs).toBeEnabled();
    fireEvent.click(cogs);
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/Saved\./)).toBeInTheDocument();
    expect(calls("PUT", LE1)[0].body).toEqual({ cogsAtShipment: true, asnEnabled: false });
    expect(header(calls("PUT", LE1)[0], "If-Match")).toBe('"0"');

    refuse = true;
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/needs the ledger setup duty/)).toBeInTheDocument();
  });

  it("shows the empty state and the error with retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/fulfillment/legal-entity-settings");
    expect((await findAlert()).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No legal entities yet")).toBeInTheDocument();
  });
});

describe("post form presets from the legal entity setting", () => {
  const packed: Shipment = {
    id: SH,
    shipmentNumber: "SH-0000042",
    legalEntityId: LE2,
    warehouseId: ID,
    orderId: ID,
    customerId: ID,
    status: "PACKED",
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    carrierCode: "UPS",
    version: 6,
    lines: [{ id: LINE, orderLineId: ID, productId: ID, unit: "EA", qtyPlanned: "10", qtyPicked: "10", qtyPacked: "10", qtyShipped: "0", pickConfirmed: true, zone: "A1" }],
    containers: [],
  };

  function serve(setting: LegalEntitySetting | null) {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":post-packing-slip")) return json(500, { code: "TEST", detail: "stop" });
      if (url.pathname === `${LIST}/${LE2}`) return setting ? json(200, setting) : json(404, { code: "COM-0404", detail: "none" });
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, packed);
      if (url.pathname.endsWith("/tracking")) return json(200, { items: [] });
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
  }

  async function post() {
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Cost snapshot id"), { target: { value: "snap-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    await waitFor(() => expect(calls("POST", ":post-packing-slip")).toHaveLength(1));
    return calls("POST", ":post-packing-slip")[0].body as Record<string, unknown>;
  }

  it("shows the COGS policy without sending it, presets the ASN and sends the poster's ASN choice", async () => {
    serve({ legalEntityId: LE2, cogsAtShipment: true, asnEnabled: true, version: 1, updatedAt: null });
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText(/posts at shipment \(Dr 5000\), the legal entity's policy/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Post cost of goods sold at shipment")).not.toBeInTheDocument();
    const asn = screen.getByLabelText(/Send an advance ship notice/);
    await waitFor(() => expect(asn).toBeChecked());
    fireEvent.click(asn);
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    expect(within(screen.getByRole("table", { name: "Journal preview" })).getByText("5000")).toBeInTheDocument();
    const body = await post();
    expect(body).toMatchObject({ asnEnabled: false });
    expect(body).not.toHaveProperty("cogsAtShipment");
  });

  it("says the policy could not be read and still leaves cogsAtShipment to the service", async () => {
    serve(null);
    renderAt(`/fulfillment/shipments/${SH}`);
    expect(await screen.findByText(/policy, which could not be read/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Send an advance ship notice/)).not.toBeChecked();
    fireEvent.change(await screen.findByLabelText("Unit cost"), { target: { value: "1" } });
    expect(within(screen.getByRole("table", { name: "Journal preview" })).getByText("1405 / 5000")).toBeInTheDocument();
    const body = await post();
    expect(body).toMatchObject({ asnEnabled: false });
    expect(body).not.toHaveProperty("cogsAtShipment");
  });
});
