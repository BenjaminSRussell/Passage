import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Directive, Location } from "./api/client";
import { generateNames, toLocationInputs, validateLocationDraft, type LocationDraft } from "./SetupPages";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";

const location: Location = {
  id: "0190f0a0-0000-7000-8000-000000000101",
  name: "R-05-02-01",
  zone: "RESERVE",
  pickSequence: 5021,
  status: "ACTIVE",
  capacity: { maxWeight: "20", maxLps: 1 },
  used: { weight: "14.4", volume: "0", lps: 1 },
  stock: { "BOLT-M8": "1200" },
  version: 2,
};

const directive: Directive = {
  id: "0190f0a0-0000-7000-8000-000000000201",
  sequence: 10,
  workType: "PURCHASE_PUTAWAY",
  direction: "PUT",
  warehouseId: WH,
  query: { item_group: "FASTENERS" },
  lines: [
    { seq: 2, fromQty: "100", actions: [{ seq: 1, strategy: "empty_location", zone: "RESERVE" }] },
    { seq: 1, fromQty: "0", toQty: "100", actions: [{ seq: 1, strategy: "consolidate", zone: "FWD" }] },
  ],
};

/** jsdom here has no localStorage; the pages work without it, and this gives them one. */
function stubLocalStorage() {
  const data = new Map<string, string>();
  vi.stubGlobal("localStorage", { getItem: (k: string) => data.get(k) ?? null, setItem: (k: string, v: string) => void data.set(k, v) });
}

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("LocationsPage", () => {
  it("asks for a warehouse before loading anything", async () => {
    const { container } = renderAt("/warehouse/locations");
    expect(await screen.findByText("Choose a warehouse")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("lists the warehouse's locations with capacity, use and stock", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/warehouse/locations" ? json(200, { items: [location] }) : undefined);
    const { container } = renderAt(`/warehouse/locations?warehouse=${WH}`);
    const table = await findTable({ name: "Locations" });
    await waitFor(() => expect(within(table).getByText("R-05-02-01")).toBeInTheDocument());
    expect(table).toHaveTextContent("≤ 20 kg · ≤ 1 LP");
    expect(table).toHaveTextContent("14.4 kg · 0 m³ · 1 LP");
    expect(table).toHaveTextContent("BOLT-M8 1200");
    expect(server.calls[0].url.searchParams.get("warehouseId")).toBe(WH);
    expect(server.calls[0].url.searchParams.get("limit")).toBe("100");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and filters by zone", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt(`/warehouse/locations?warehouse=${WH}`);
    expect(await screen.findByText("No locations yet")).toBeInTheDocument();
    fill("Zone", "HAZ");
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("zone")).toBe("HAZ"));
  });

  it("reports a load failure and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(503, { code: "COM-0500", detail: "database unavailable" }) : json(200, { items: [location] }));
    renderAt(`/warehouse/locations?warehouse=${WH}`);
    const alert = await screen.findByText("Could not load the locations");
    expect(alert.closest("[role=alert]")).toHaveTextContent("database unavailable (COM-0500)");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("R-05-02-01")).toBeInTheDocument();
  });

  it("remembers the last warehouse for the other pages", async () => {
    stubLocalStorage();
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/warehouse/locations");
    fill("Warehouse id", "not-a-uuid");
    expect(screen.getByText("Enter the warehouse id, a UUID.")).toBeInTheDocument();
    fill("Warehouse id", WH);
    expect(await screen.findByText("No locations yet")).toBeInTheDocument();
    first.unmount();
    renderAt("/warehouse/work");
    expect(await screen.findByLabelText("Warehouse id")).toHaveValue(WH);
  });

  it("loads the next page from the cursor", async () => {
    const second = { ...location, id: "0190f0a0-0000-7000-8000-000000000102", name: "R-05-02-02" };
    server.handler = (url) => (url.searchParams.get("cursor") === "c1" ? json(200, { items: [second] }) : json(200, { items: [location], nextCursor: "c1" }));
    renderAt(`/warehouse/locations?warehouse=${WH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Load more locations" }));
    expect(await screen.findByText("R-05-02-02")).toBeInTheDocument();
    expect(screen.getByText("R-05-02-01")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more locations" })).not.toBeInTheDocument();
  });
});

describe("location name format", () => {
  it("builds two-digit components and skips blank ranges", () => {
    expect(generateNames("A", [{ from: "1", to: "2" }, { from: "", to: "" }, { from: "3", to: "3" }])).toEqual(["A-01-03", "A-02-03"]);
    expect(generateNames("A", [{ from: "2", to: "1" }])).toBeNull();
    expect(generateNames("A", [{ from: "1", to: "100" }])).toBeNull();
  });

  it("caps a request at 1,000 locations and numbers the pick sequence", () => {
    const draft: LocationDraft = {
      warehouseId: WH,
      zone: "R",
      prefix: "R",
      aisles: { from: "0", to: "10" },
      racks: { from: "0", to: "10" },
      shelves: { from: "0", to: "9" },
      firstPickSequence: "5",
      status: "ACTIVE",
      maxWeight: "",
      maxVolume: "",
      maxLps: "1",
      maxItems: "",
      allowMixedItems: false,
    };
    expect(validateLocationDraft(draft).count).toMatch("1210 locations");
    const inputs = toLocationInputs({ ...draft, aisles: { from: "1", to: "1" }, racks: { from: "", to: "" }, shelves: { from: "1", to: "2" } });
    expect(inputs.map((l) => [l.name, l.pickSequence])).toEqual([
      ["R-01-01", 5],
      ["R-01-02", 6],
    ]);
    expect(inputs[0].capacity).toEqual({ maxLps: 1, allowMixedItems: false });
  });
});

describe("NewLocationsPage", () => {
  it("validates, previews the names and creates them with an idempotency key", async () => {
    server.handler = (url, init) =>
      url.pathname === "/api/v1/warehouse/locations:bulk" && init.method === "POST" ? json(201, { items: [location, location, location, location] }) : undefined;
    const { container } = renderAt(`/warehouse/locations/new?warehouse=${WH}`);
    fireEvent.click(screen.getByRole("button", { name: "Create locations" }));
    expect(await screen.findByText("Enter the zone.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);

    fill("Zone", "RESERVE");
    fill("Name prefix", "R");
    fill("Aisles to", "2");
    fill("Racks from", "1");
    fill("Racks to", "2");
    fill("Maximum weight (kg)", "20");
    fireEvent.click(screen.getByLabelText("Allow mixed items"));
    expect(screen.getByText("Creates 4 locations: R-01-01 to R-02-02.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create locations" }));
    expect(await screen.findByText("4 locations created.")).toBeInTheDocument();
    const call = server.calls[0];
    expect(idempotencyKey(call)).toBeTruthy();
    const body = call.body as { warehouseId: string; locations: { name: string; zone: string; capacity: object }[] };
    expect(body.warehouseId).toBe(WH);
    expect(body.locations.map((l) => l.name)).toEqual(["R-01-01", "R-01-02", "R-02-01", "R-02-02"]);
    expect(body.locations[0].capacity).toEqual({ maxWeight: "20", allowMixedItems: true });
    expect(pageViolations(container)).toEqual([]);
  });

  it("keeps the values and the key when the service refuses, so a retry is safe", async () => {
    server.handler = () => json(409, { code: "WMS-DUPLICATE", detail: "a record with the same name or sequence already exists" });
    renderAt(`/warehouse/locations/new?warehouse=${WH}`);
    fill("Zone", "A");
    fill("Name prefix", "A");
    fireEvent.click(screen.getByRole("button", { name: "Create locations" }));
    expect(await screen.findByText(/already exists \(WMS-DUPLICATE\)/)).toBeInTheDocument();
    expect(screen.getByLabelText("Zone")).toHaveValue("A");
    fireEvent.click(screen.getByRole("button", { name: "Create locations" }));
    await waitFor(() => expect(server.calls).toHaveLength(2));
    expect(idempotencyKey(server.calls[1])).toBe(idempotencyKey(server.calls[0]));
  });
});

describe("DirectivesPage", () => {
  it("lists directives with their scope and bands in order", async () => {
    server.handler = () => json(200, { items: [directive] });
    const { container } = renderAt(`/warehouse/directives?warehouse=${WH}`);
    const table = await findTable({ name: "Location directives" });
    await waitFor(() => expect(table).toHaveTextContent("item_group = FASTENERS"));
    expect(table).toHaveTextContent("0–100: consolidate in FWD; 100–∞: empty_location in RESERVE");
    expect(table).toHaveTextContent("Purchase putaway");
    expect(pageViolations(container)).toEqual([]);
  });

  it("filters by work type and explains an empty list", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt(`/warehouse/directives?warehouse=${WH}`);
    expect(await screen.findByText("No directives yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Work type"), { target: { value: "SALES_PICK" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("workType")).toBe("SALES_PICK"));
  });
});

describe("NewDirectivePage", () => {
  it("creates the directive and returns to the list", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, directive);
      if (url.pathname === "/api/v1/warehouse/location-directives") return json(200, { items: [directive] });
      return undefined;
    };
    renderAt(`/warehouse/directives/new?warehouse=${WH}`);
    fill("Applies to", "item_group=FASTENERS");
    fill("Sequence", "20");
    fireEvent.click(screen.getByRole("button", { name: "Create directive" }));
    expect(await findTable({ name: "Location directives" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post && idempotencyKey(post)).toBeTruthy();
    expect(post?.body).toMatchObject({ warehouseId: WH, workType: "PURCHASE_PUTAWAY", direction: "PUT", sequence: 20, query: { item_group: "FASTENERS" } });
    expect((post?.body as { lines: unknown[] }).lines).toHaveLength(2);
  });

  it("puts the service's field errors on the bands", async () => {
    server.handler = () =>
      json(400, { code: "COM-0001", detail: "request failed validation", errors: { "lines[1].fromQty": "overlaps the quantity range of lines[0]" } });
    renderAt(`/warehouse/directives/new?warehouse=${WH}`);
    fireEvent.click(screen.getByRole("button", { name: "Create directive" }));
    expect(await screen.findByText("lines[1].fromQty overlaps the quantity range of lines[0]")).toBeInTheDocument();
    expect(screen.getByText("request failed validation (COM-0001)")).toBeInTheDocument();
  });

  it("refuses bad JSON before calling the service", async () => {
    renderAt(`/warehouse/directives/new?warehouse=${WH}`);
    fill("Quantity bands (JSON)", "[");
    fireEvent.click(screen.getByRole("button", { name: "Create directive" }));
    expect(await screen.findByText("Enter the quantity bands as a JSON array with at least one band.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });
});
