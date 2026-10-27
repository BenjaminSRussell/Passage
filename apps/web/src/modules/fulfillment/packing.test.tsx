// Pages of slice 2: container types, packing rules, packaging materials, packaging
// fees with the report, returnable packaging, and auto-pack and packaging fees on
// the shipment and packing slip pages (FR-28.14 to FR-28.16).
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AutoPackResult, ContainerType, PackingSlip, Shipment } from "./api/client";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const SH = "0195f0a0-0000-7000-8000-000000000001";
const LINE = "0195f0a0-0000-7000-8000-000000000002";
const LINE2 = "0195f0a0-0000-7000-8000-000000000012";
const PRODUCT = "0195f0a0-0000-7000-8000-000000000020";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000030";
const SLIP = "0195f0a0-0000-7000-8000-000000000005";
const ID = "0195f0a0-0000-7000-8000-0000000000aa";

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => server.calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));

const pallet: ContainerType = { code: "PALLET", tareKg: "25", returnable: true, packagingMaterial: "WOOD" };
const carton: ContainerType = { code: "C12", tareKg: "0.5", returnable: false, packagingMaterial: "CARDBOARD" };

describe("container types", () => {
  it("lists types, validates and saves one with an Idempotency-Key", async () => {
    let types = [pallet];
    server.handler = (url, init) => {
      if (url.pathname !== "/api/v1/fulfillment/container-types") return undefined;
      if (init.method === "POST") {
        types = [...types, carton];
        return json(201, carton);
      }
      return json(200, { items: types });
    };
    const { container } = renderAt("/fulfillment/container-types");
    const table = await screen.findByRole("table", { name: "Container types" });
    expect((await within(table).findByText("PALLET")).closest("tr")?.textContent).toContain("Returnable");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Save container type" }));
    expect(await screen.findByText(/Enter a code of up to 64/)).toBeInTheDocument();
    expect(screen.getByText(/Enter the empty weight in kg/)).toBeInTheDocument();
    expect(posts("/container-types")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "C12" } });
    fireEvent.change(screen.getByLabelText("Tare weight (kg)"), { target: { value: "0.5" } });
    fireEvent.change(screen.getByLabelText("Packaging material"), { target: { value: "cardboard" } });
    fireEvent.click(screen.getByRole("button", { name: "Save container type" }));
    expect((await screen.findByRole("status")).textContent).toBe("Saved container type C12.");
    const call = posts("/container-types")[0];
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ code: "C12", tareKg: "0.5", returnable: false, packagingMaterial: "CARDBOARD" });
    expect(await within(screen.getByRole("table", { name: "Container types" })).findByText("C12")).toBeInTheDocument();
  });

  it("opens a row into the form for editing", async () => {
    server.handler = () => json(200, { items: [pallet] });
    renderAt("/fulfillment/container-types");
    fireEvent.click(await screen.findByRole("button", { name: "PALLET" }));
    expect(screen.getByLabelText("Code")).toHaveValue("PALLET");
    expect(screen.getByLabelText("Tare weight (kg)")).toHaveValue("25");
    expect(screen.getByLabelText("Returnable transport packaging")).toBeChecked();
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] }));
    renderAt("/fulfillment/container-types");
    expect((await screen.findByRole("alert")).textContent).toContain("COM-0500");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No container types yet")).toBeInTheDocument();
  });
});

describe("packing rules", () => {
  it("lists rules, offers the container types and saves a general rule", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/container-types") return json(200, { items: [carton, pallet] });
      if (url.pathname !== "/api/v1/fulfillment/packing-rules") return undefined;
      if (init.method === "POST") return json(201, { id: ID, productId: PRODUCT, customerId: null, containerType: "C12", qtyPerContainer: "12", maxWeightKg: "15" });
      return json(200, {
        items: [
          { id: ID, productId: PRODUCT, customerId: null, containerType: "C12", qtyPerContainer: "12", maxWeightKg: "15" },
          { id: SH, productId: PRODUCT, customerId: CUSTOMER, containerType: "PALLET", qtyPerContainer: "48", maxWeightKg: "800" },
        ],
      });
    };
    const { container } = renderAt("/fulfillment/packing-rules");
    const table = await screen.findByRole("table", { name: "Packing rules" });
    expect((await within(table).findByText("C12")).closest("tr")?.textContent).toContain("All customers");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Product id", { selector: "#rule-product" }), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Container type"), { target: { value: "C12" } });
    fireEvent.change(screen.getByLabelText("Quantity per container"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Max gross weight (kg)"), { target: { value: "15" } });
    fireEvent.click(screen.getByRole("button", { name: "Save packing rule" }));
    expect(await screen.findByText(/how many units fit/)).toBeInTheDocument();
    expect(posts("/packing-rules")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Quantity per container"), { target: { value: "12" } });
    fireEvent.click(screen.getByRole("button", { name: "Save packing rule" }));
    expect((await screen.findByRole("status")).textContent).toContain("12 per C12");
    expect(posts("/packing-rules")[0].body).toEqual({ productId: PRODUCT, customerId: null, containerType: "C12", qtyPerContainer: "12", maxWeightKg: "15" });
  });

  it("filters by product and asks for a container type first when there is none", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/fulfillment/container-types") return json(200, { items: [] });
      return json(200, { items: [] });
    };
    renderAt("/fulfillment/packing-rules");
    expect(await screen.findByText("Add a container type first")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product id", { selector: "#rules-product" }), { target: { value: PRODUCT } });
    expect(await screen.findByText("No rules for this product")).toBeInTheDocument();
    await waitFor(() => expect(server.calls.some((c) => c.url.searchParams.get("productId") === PRODUCT)).toBe(true));
  });

  it("shows the service's error code when the type is unknown", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/container-types") return json(200, { items: [carton] });
      if (init.method === "POST") return json(422, { code: "FUL-0050", detail: "request failed validation", errors: { containerType: "is not a known container type" } });
      return json(200, { items: [] });
    };
    renderAt("/fulfillment/packing-rules");
    await screen.findByText("No packing rules yet");
    fireEvent.change(screen.getByLabelText("Product id", { selector: "#rule-product" }), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Container type"), { target: { value: "C12" } });
    fireEvent.change(screen.getByLabelText("Quantity per container"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("Max gross weight (kg)"), { target: { value: "15" } });
    fireEvent.click(screen.getByRole("button", { name: "Save packing rule" }));
    expect((await screen.findByRole("alert")).textContent).toContain("FUL-0050");
  });
});

describe("packaging materials", () => {
  it("lists packaging per product and saves a weight", async () => {
    server.handler = (_url, init) => {
      if (init.method === "POST") return json(201, { productId: PRODUCT, materialCode: "CARDBOARD", weightPerUnitKg: "0.2" });
      return json(200, { items: [{ productId: PRODUCT, materialCode: "PLASTIC", weightPerUnitKg: "0.05" }] });
    };
    const { container } = renderAt("/fulfillment/packaging-materials");
    expect(await within(await screen.findByRole("table", { name: "Packaging per product unit" })).findByText("PLASTIC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Product id", { selector: "#pp-product" }), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Material code"), { target: { value: "cardboard" } });
    fireEvent.change(screen.getByLabelText("Weight per unit (kg)"), { target: { value: "0.2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save packaging" }));
    expect((await screen.findByRole("status")).textContent).toContain("0.2 kg CARDBOARD");
    expect(posts("/product-packaging")[0].body).toEqual({ productId: PRODUCT, materialCode: "CARDBOARD", weightPerUnitKg: "0.2" });
  });

  it("shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/packaging-materials");
    expect(await screen.findByText("No packaging recorded")).toBeInTheDocument();
  });
});

describe("packaging fees", () => {
  it("lists rates, saves one and shows the period report (AC-28.12)", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/packaging-report") {
        return json(200, { countryCode: "DE", period: "2026-10", legalEntityId: null, items: [{ materialCode: "CARDBOARD", weightKg: "20", feeAmount: "10", currency: "EUR" }] });
      }
      if (init.method === "POST") return json(201, { countryCode: "DE", materialCode: "CARDBOARD", currency: "EUR", ratePerKg: "0.50", validFrom: "2026-01-01" });
      return json(200, { items: [{ countryCode: "FR", materialCode: "GLASS", currency: "EUR", ratePerKg: "0.10", validFrom: "2026-01-01" }] });
    };
    const { container } = renderAt("/fulfillment/packaging-fees");
    expect(await within(await screen.findByRole("table", { name: "Packaging fee rates" })).findByText("GLASS")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Country", { selector: "#rate-country" }), { target: { value: "de" } });
    fireEvent.change(screen.getByLabelText("Material code"), { target: { value: "CARDBOARD" } });
    fireEvent.change(screen.getByLabelText("Rate per kg"), { target: { value: "0.50" } });
    fireEvent.change(screen.getByLabelText("Valid from"), { target: { value: "2026-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save fee rate" }));
    expect((await screen.findByRole("status")).textContent).toContain("0.50 EUR per kg");
    const call = posts("/packaging-fee-rates")[0];
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ countryCode: "DE", materialCode: "CARDBOARD", currency: "EUR", ratePerKg: "0.50", validFrom: "2026-01-01" });

    fireEvent.change(screen.getByLabelText("Country", { selector: "#report-country" }), { target: { value: "DE" } });
    fireEvent.change(screen.getByLabelText("Month"), { target: { value: "2026-10" } });
    fireEvent.click(screen.getByRole("button", { name: "Show report" }));
    const report = await screen.findByRole("table", { name: "Packaging report DE 2026-10" });
    const row = (await within(report).findByText("CARDBOARD")).closest("tr");
    expect(row?.textContent).toContain("20");
    expect(row?.textContent).toContain("10.00 EUR");
    const reportCall = server.calls.find((c) => c.url.pathname === "/api/v1/fulfillment/packaging-report");
    expect(reportCall?.url.searchParams.get("period")).toBe("2026-10");
  });

  it("validates the report query before calling the service", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/packaging-fees");
    await screen.findByText("No fee rates yet");
    fireEvent.change(screen.getByLabelText("Month"), { target: { value: "2026-13" } });
    fireEvent.click(screen.getByRole("button", { name: "Show report" }));
    expect(await screen.findByText("Enter the month as YYYY-MM.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname.endsWith("/packaging-report"))).toBe(false);
  });
});

describe("returnable packaging", () => {
  it("lists balances and refuses a return above the balance with FUL-0015 (AC-28.13)", async () => {
    let balance = "6";
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/fulfillment/container-types") return json(200, { items: [pallet, carton] });
      if (init.method === "POST") {
        const body = JSON.parse(String(init.body)) as { qty: string };
        if (Number(body.qty) > Number(balance)) return json(422, { code: "FUL-0015", detail: "returnable packaging balance would become negative" });
        balance = String(Number(balance) - Number(body.qty));
        return json(201, { customerId: CUSTOMER, containerType: "PALLET", balance, lastShippedAt: null, lastReturnedAt: "2026-10-27T08:00:00Z" });
      }
      return json(200, { items: [{ customerId: CUSTOMER, containerType: "PALLET", balance, lastShippedAt: "2026-10-26T08:00:00Z", lastReturnedAt: null }] });
    };
    const { container } = renderAt("/fulfillment/returnable-packaging");
    const table = await screen.findByRole("table", { name: "Returnable packaging balances" });
    expect((await within(table).findByText("PALLET")).closest("tr")?.textContent).toContain("6");
    expect(pageViolations(container)).toEqual([]);

    // Only returnable types are offered; opening the row fills customer and type.
    const typeSelect = screen.getByLabelText("Container type");
    expect(within(typeSelect).queryByRole("option", { name: "C12" })).toBeNull();
    fireEvent.click(within(table).getByRole("button", { name: CUSTOMER.slice(0, 8) }));
    expect(screen.getByLabelText("Customer id", { selector: "#rtp-customer" })).toHaveValue(CUSTOMER);
    expect(typeSelect).toHaveValue("PALLET");

    fireEvent.change(screen.getByLabelText("Quantity returned"), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Record return" }));
    expect(await screen.findByText(/a whole number of 1 or more/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Quantity returned"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Record return" }));
    expect((await screen.findByRole("alert")).textContent).toContain("FUL-0015");

    fireEvent.change(screen.getByLabelText("Quantity returned"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Record return" }));
    expect((await screen.findByRole("status")).textContent).toContain("now holds 2 PALLET");
    const calls = posts("/returnable-packaging:return");
    expect(calls.at(-1)?.body).toEqual({ customerId: CUSTOMER, containerType: "PALLET", qty: "4" });
    expect(idempotencyKey(calls[0])).not.toBe(idempotencyKey(calls[1]));
  });

  it("shows the empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/fulfillment/returnable-packaging");
    expect(await screen.findByText("No returnable packaging out")).toBeInTheDocument();
  });
});

function picked(version = 4): Shipment {
  const line = { orderLineId: ID, productId: PRODUCT, unit: "EA", qtyPacked: "0", qtyShipped: "0", pickConfirmed: true };
  return {
    id: SH,
    shipmentNumber: "SH-0000042",
    legalEntityId: ID,
    warehouseId: ID,
    orderId: ID,
    customerId: CUSTOMER,
    status: "PICKED",
    plannedShipDate: "2026-10-26T00:00:00Z",
    actualShipDate: null,
    version,
    lines: [
      { ...line, id: LINE, qtyPlanned: "30", qtyPicked: "30" },
      { ...line, id: LINE2, qtyPlanned: "5", qtyPicked: "5" },
    ],
    containers: [],
  };
}

describe("auto-pack on the shipment page (FR-28.15)", () => {
  it("sends If-Match and unit weights, and shows the FUL-0012 warning for a line without a rule", async () => {
    let current = picked();
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":auto-pack")) {
        const sscc = ["006141410000000011", "006141410000000028", "006141410000000035"];
        const gross: Record<string, string> = { "12": "12.5", "6": "6.5" };
        const containers = ["12", "12", "6"].map((qty, i) => ({
          id: `${ID.slice(0, -1)}${i}`,
          containerType: "C12",
          sscc: sscc[i],
          status: "CLOSED" as const,
          tareKg: "0.5",
          grossKg: gross[qty],
          returnable: false,
          packagingMaterial: "CARDBOARD",
          lines: [{ shipmentLineId: LINE, qty }],
        }));
        current = { ...picked(5), containers, lines: [{ ...picked().lines[0], qtyPacked: "30" }, picked().lines[1]] };
        const result: AutoPackResult = {
          shipment: current,
          containers,
          unpackedLineIds: [LINE2],
          warnings: [{ code: "FUL-0012", shipmentLineId: LINE2, productId: PRODUCT, message: `no packing rule for product ${PRODUCT}; line left unpacked` }],
        };
        return json(201, result);
      }
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, current);
      if (url.pathname === "/api/v1/fulfillment/packing-slips") return json(200, { items: [] });
      return undefined;
    };
    const { container } = renderAt(`/fulfillment/shipments/${SH}`);
    const weights = await screen.findAllByLabelText(/unit weight \(kg\)$/);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(weights[0], { target: { value: "1,5" } });
    fireEvent.click(screen.getByRole("button", { name: "Auto-pack" }));
    expect(await screen.findByText(/Enter the unit weight in kg/)).toBeInTheDocument();
    expect(posts(":auto-pack")).toHaveLength(0);

    fireEvent.change(weights[0], { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Auto-pack" }));
    const notice = await screen.findByText("Auto-pack closed 3 containers");
    expect(notice.closest("[role=status]")?.textContent).toContain("FUL-0012");
    const call = posts(":auto-pack")[0];
    expect(header(call, "If-Match")).toBe('"4"');
    expect(idempotencyKey(call)).toBeTruthy();
    expect(call.body).toEqual({ unitWeights: { [LINE]: "1" } });
    const table = await screen.findByRole("table", { name: "Containers" });
    expect((await within(table).findByText("006141410000000035")).closest("tr")?.textContent).toContain("6.5");
  });

  it("sends the ship-to country with the packing slip post", async () => {
    const packed: Shipment = { ...picked(6), status: "PACKED", lines: picked().lines.map((l) => ({ ...l, qtyPacked: l.qtyPicked })) };
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "FUL-0014", detail: "CARDBOARD: packaging fee rate missing for EUR in FR on 2026-10-27" });
      if (url.pathname === `/api/v1/fulfillment/shipments/${SH}`) return json(200, packed);
      return json(200, { items: [] });
    };
    renderAt(`/fulfillment/shipments/${SH}`);
    for (const field of await screen.findAllByLabelText("Unit cost")) fireEvent.change(field, { target: { value: "1" } });
    for (const field of screen.getAllByLabelText("Cost snapshot id")) fireEvent.change(field, { target: { value: "s" } });
    fireEvent.change(screen.getByLabelText("Ship-to country"), { target: { value: "fra" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    expect(await screen.findByText(/2-letter country code/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ship-to country"), { target: { value: "fr" } });
    fireEvent.click(screen.getByRole("button", { name: "Post packing slip" }));
    expect((await screen.findByRole("alert")).textContent).toContain("FUL-0014");
    expect(posts(":post-packing-slip")[0].body).toMatchObject({ countryCode: "FR" });
  });
});

describe("packing slip packaging fees (BR-28.12)", () => {
  it("shows the statistical fee lines apart from the journal", async () => {
    const slip: PackingSlip = {
      id: SLIP,
      packingSlipNumber: "PS-0000001",
      shipmentId: SH,
      orderId: ID,
      status: "POSTED",
      postingDate: "2026-10-26T00:00:00Z",
      currency: "EUR",
      totalCost: "100",
      reversalOfId: null,
      lines: [{ orderLineId: ID, shipmentLineId: LINE, qty: "100", costAmount: "100", costSnapshotId: "snap" }],
      journal: [
        { account: "1405", amount: "100", currency: "EUR", orderLineId: ID },
        { account: "1400", amount: "-100", currency: "EUR", orderLineId: ID },
      ],
      packagingFees: [{ countryCode: "DE", period: "2026-10", materialCode: "CARDBOARD", weightKg: "20", ratePerKg: "0.5", feeAmount: "10", currency: "EUR" }],
    };
    server.handler = () => json(200, slip);
    const { container } = renderAt(`/fulfillment/packing-slips/${SLIP}`);
    const fees = await screen.findByRole("table", { name: "Packaging fees" });
    expect(within(fees).getByText("CARDBOARD").closest("tr")?.textContent).toContain("10.00 EUR");
    expect(within(screen.getByRole("table", { name: "Journal" })).queryByText("CARDBOARD")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });
});
