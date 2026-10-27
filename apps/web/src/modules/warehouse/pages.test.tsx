import { findStatus, findTable } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { DirectiveSimulatorPage, exampleDraft, fieldErrors, hazmatDraft, toRequest, validateDraft } from "./pages";

type Reply = { status: number; body: unknown };

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
let calls: { url: string; init: RequestInit; body: unknown }[];
let reply: () => Reply | Promise<never>;

beforeEach(() => {
  calls = [];
  reply = () => ({ status: 200, body: { blocked: true, code: "WMS-0101", trace: [] } });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit = {}) => {
      calls.push({ url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const r = await reply();
      return { ok: r.status < 400, status: r.status, json: async () => r.body } as Response;
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const hit = {
  blocked: false,
  locationId: "loc-r1",
  location: "R-05-02-01",
  directive: "DIR-10",
  trace: [{ directive: "DIR-10", band: 3, action: 1, outcome: "hit", reason: "action 1 empty_location: 1 candidates, chose R-05-02-01" }],
};

const simulate = () => fireEvent.click(screen.getByRole("button", { name: "Simulate" }));

describe("DirectiveSimulatorPage", () => {
  it("starts with the worked example and an empty result", () => {
    const { container } = render(<DirectiveSimulatorPage />);
    expect(screen.getByRole("heading", { level: 2, name: "Location directive simulator" })).toBeInTheDocument();
    expect(screen.getByLabelText("Item")).toHaveValue("BOLT-M8");
    expect(screen.getByLabelText("Quantity")).toHaveValue("1200");
    expect(screen.getByText("No simulation run yet")).toBeInTheDocument();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("posts the typed request and shows the chosen location with its trace", async () => {
    reply = () => ({ status: 200, body: hit });
    const { container } = render(<DirectiveSimulatorPage />);
    simulate();
    expect(await findStatus()).toHaveTextContent("Location R-05-02-01 chosen by DIR-10, line 3, action 1.");
    const trace = screen.getByRole("table", { name: "Rule trace" });
    expect(within(trace).getByText("DIR-10")).toBeInTheDocument();
    expect(trace).toHaveTextContent("Hit");
    expect(trace).toHaveTextContent("chose R-05-02-01");
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/v1/warehouse/location-directives:simulate");
    expect(calls[0].init.method).toBe("POST");
    const body = calls[0].body as { line: unknown; directives: { id: string }[]; locations: { name: string }[] };
    expect(body.line).toEqual({
      itemId: "BOLT-M8",
      workType: "PURCHASE_PUTAWAY",
      direction: "PUT",
      warehouseId: "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11",
      qty: "1200",
      weight: "14.4",
      lps: 1,
      attributes: { item_group: "FASTENERS" },
    });
    expect(body.directives.map((d) => d.id)).toEqual(["DIR-10", "DIR-20"]);
    expect(body.locations.map((l) => l.name)).toContain("R-05-02-01");
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("reports a blocked line with WMS-0101 and an empty trace", async () => {
    render(<DirectiveSimulatorPage />);
    simulate();
    expect(await findStatus()).toHaveTextContent("Blocked (WMS-0101): no directive action found a location for this line.");
    expect(screen.getByText("No directive was evaluated")).toBeInTheDocument();
  });

  it("checks the form before calling the service", () => {
    render(<DirectiveSimulatorPage initial={{ ...exampleDraft, qty: "0", directives: "{not json" }} />);
    simulate();
    expect(screen.getByText("Enter a quantity above 0.")).toBeInTheDocument();
    expect(screen.getByText("Enter the directives as a JSON array.")).toBeInTheDocument();
    expect(screen.getByLabelText("Quantity")).toHaveAttribute("aria-invalid", "true");
    expect(calls).toHaveLength(0);
  });

  it("shows the service problem, maps its field errors and keeps the inputs", async () => {
    reply = () => ({
      status: 400,
      body: {
        code: "COM-0001",
        detail: "request failed validation",
        errors: { "line.workType": "is required", "directives[0].lines[1].fromQty": "overlaps the quantity range of lines[0]" },
      },
    });
    render(<DirectiveSimulatorPage />);
    simulate();
    expect(await screen.findByText("The simulation failed")).toBeInTheDocument();
    expect(screen.getByText("request failed validation (COM-0001)")).toBeInTheDocument();
    expect(screen.getByText("This is required.")).toBeInTheDocument();
    expect(screen.getByText("directives[0].lines[1].fromQty overlaps the quantity range of lines[0]")).toBeInTheDocument();
    expect(screen.getByLabelText("Item")).toHaveValue("BOLT-M8");
    expect(screen.getByText("No simulation run yet")).toBeInTheDocument();
  });

  it("shows a missing duty as a failure", async () => {
    reply = () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty warehouse.setup.view" } });
    render(<DirectiveSimulatorPage />);
    simulate();
    expect(await screen.findByText("missing duty warehouse.setup.view (AUTH-0003)")).toBeInTheDocument();
  });

  it("shows a network failure and clears an earlier result", async () => {
    reply = () => ({ status: 200, body: hit });
    render(<DirectiveSimulatorPage />);
    simulate();
    expect(await findTable({ name: "Rule trace" })).toBeInTheDocument();
    reply = () => Promise.reject(new TypeError("Failed to fetch"));
    simulate();
    expect(await screen.findByText("Failed to fetch (NETWORK)")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Rule trace" })).not.toBeInTheDocument();
  });

  it("runs the AC-31.14 hazmat example and shows the segregation reason", async () => {
    reply = () => ({
      status: 200,
      body: {
        blocked: false,
        locationId: "loc-h3",
        location: "H-03-01-01",
        directive: "DIR-HAZ",
        trace: [
          {
            directive: "DIR-HAZ",
            band: 1,
            action: 1,
            outcome: "hit",
            reason: "action 1 empty_location: 1 candidates, chose H-03-01-01; rejected hazmat: class 3 vs 5.1 prohibited in FCS-02 (1), not empty (1)",
          },
        ],
      },
    });
    const { container } = render(<DirectiveSimulatorPage />);
    expect(screen.getByLabelText(/Hazmat compliance/)).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Load hazmat example" }));
    expect(screen.getByLabelText("Item")).toHaveValue("SOLV-3");
    simulate();
    expect(await findStatus()).toHaveTextContent("Location H-03-01-01 chosen by DIR-HAZ, line 1, action 1.");
    expect(screen.getByRole("table", { name: "Rule trace" })).toHaveTextContent("hazmat: class 3 vs 5.1 prohibited in FCS-02");
    const body = calls[0].body as { compliance: { segregation: unknown[]; sectionLimitsKg: Record<string, string> }; locations: { fireSection?: string }[] };
    expect(body.compliance.segregation).toEqual([{ a: "3", b: "5.1", rel: "prohibited" }]);
    expect(body.compliance.sectionLimitsKg["FCS-02"]).toBe("1000");
    expect(body.locations.map((l) => l.fireSection)).toEqual(["FCS-02", "FCS-02", "FCS-01"]);
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("refuses hazmat data that is not a JSON object before calling the service", () => {
    render(<DirectiveSimulatorPage initial={{ ...hazmatDraft, compliance: "[1, 2]" }} />);
    simulate();
    expect(screen.getByText("Enter the hazmat data as a JSON object, or leave it blank.")).toBeInTheDocument();
    expect(screen.getByLabelText(/Hazmat compliance/)).toHaveAttribute("aria-invalid", "true");
    expect(calls).toHaveLength(0);
  });

  it("puts a service compliance error on the hazmat field", async () => {
    reply = () => ({
      status: 400,
      body: { code: "COM-0001", detail: "request failed validation", errors: { compliance: 'segregation 3/5.1 has unknown relation "maybe"' } },
    });
    render(<DirectiveSimulatorPage initial={hazmatDraft} />);
    simulate();
    expect(await screen.findByText('segregation 3/5.1 has unknown relation "maybe"')).toBeInTheDocument();
    expect(screen.getByLabelText(/Hazmat compliance/)).toHaveAttribute("aria-invalid", "true");
  });

  it("restores the worked example", () => {
    render(<DirectiveSimulatorPage initial={{ ...exampleDraft, itemId: "" }} />);
    fireEvent.click(screen.getByRole("button", { name: "Load worked example" }));
    expect(screen.getByLabelText("Item")).toHaveValue("BOLT-M8");
  });
});

describe("simulator helpers", () => {
  it("mirrors the service validation", () => {
    expect(validateDraft(exampleDraft)).toEqual({});
    const bad = validateDraft({ ...exampleDraft, itemId: " ", warehouseId: "WH1", weight: "-1", lps: "1.5", attributes: "no equals", locations: "{}" });
    expect(Object.keys(bad).sort()).toEqual(["attributes", "itemId", "locations", "lps", "warehouseId", "weight"]);
  });

  it("sends decimals as strings and leaves blank optional fields out", () => {
    const req = toRequest({ ...exampleDraft, weight: "", lps: "", attributes: "", warehouseId: exampleDraft.warehouseId.toUpperCase() });
    expect(req.line).toEqual({ itemId: "BOLT-M8", workType: "PURCHASE_PUTAWAY", direction: "PUT", warehouseId: exampleDraft.warehouseId, qty: "1200" });
    expect(req).not.toHaveProperty("compliance");
    expect(validateDraft(hazmatDraft)).toEqual({});
    expect(toRequest(hazmatDraft).compliance?.hazmatProfiles?.map((p) => p.storageClass)).toEqual(["3", "5.1"]);
  });

  it("places location hazmat field errors on the locations field", () => {
    expect(fieldErrors({ "locations[2].allowedStorageClasses": "must not hold a blank storage class" })).toEqual({
      locations: "locations[2].allowedStorageClasses must not hold a blank storage class",
    });
  });

  it("joins several errors on one JSON field and ignores paths it cannot place", () => {
    expect(fieldErrors({ "locations[1].id": "is used by another location", "locations[0].used": "must not be negative", other: "x" })).toEqual({
      locations: "locations[0].used must not be negative; locations[1].id is used by another location",
    });
  });
});
