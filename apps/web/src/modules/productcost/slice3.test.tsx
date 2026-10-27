import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import type { ConversionReport, ConversionRun, CostResult } from "./api/client";
import { ac3910, stepRows, validateInput } from "./ConversionPage";
import productcost from "./module";
import { subtotalRows } from "./RollPage";
import { movesFor, validateVersion } from "./VersionsPage";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, body: unknown) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const body = init.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ url, body });
      const reply = handler(url, body) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {productcost.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("slice 3 helpers", () => {
  it("offers only the moves the version state machine allows", () => {
    expect(movesFor({ id: "v", type: "Standard", status: "Open", fallback: "Active" })).toEqual(["block", "activate"]);
    expect(movesFor({ id: "v", type: "Standard", status: "Blocked", fallback: "Active" })).toEqual(["unblock", "activate"]);
    expect(movesFor({ id: "s", type: "Simulation", status: "Open", fallback: "None" })).toEqual(["block"]);
    expect(movesFor({ id: "v", type: "Standard", status: "Active", fallback: "Active" })).toEqual(["close"]);
    expect(movesFor({ id: "v", type: "Standard", status: "Closed", fallback: "Active" })).toEqual([]);
  });

  it("checks a new version's code and specified fallback", () => {
    const base = { id: "STD-2027", type: "Standard" as const, fallback: "SpecifiedVersion" as const, fallbackVersionId: "", sites: "" };
    expect(validateVersion(base, []).errors["version.fallbackVersionId"]).toMatch(/Name the version/);
    expect(validateVersion({ ...base, fallbackVersionId: "STD-2027" }, []).errors["version.fallbackVersionId"]).toMatch(/itself/);
    expect(validateVersion({ ...base, fallback: "Active" }, [{ id: "STD-2027", type: "Standard", status: "Open", fallback: "Active" }]).errors["version.id"]).toMatch(
      /already/,
    );
    expect(validateVersion({ ...base, fallbackVersionId: "STD-2026", sites: "P1, P2" }, []).version).toEqual({
      id: "STD-2027",
      type: "Standard",
      status: "Open",
      fallback: "SpecifiedVersion",
      fallbackVersionId: "STD-2026",
      siteIds: ["P1", "P2"],
    });
  });

  it("builds the conversion input and refuses duplicates and bad counts", () => {
    const { input } = validateInput(ac3910);
    expect(input).toEqual({
      conversionDate: "2026-11-01",
      closeThrough: "2026-10-31",
      currency: "USD",
      blockers: ["NegativeOnHand", "InTransit", "StartedOrders"],
      positions: [{ itemId: "RM-301", siteId: "PLANT-1", onHand: "500", standardUnit: "10.00", actualValue: "4925.00", inTransit: 0, startedOrders: 0 }],
    });
    const twice = { ...ac3910, positions: [ac3910.positions[0], { ...ac3910.positions[0], inTransit: "1.5" }] };
    const { errors } = validateInput(twice);
    expect(errors["positions[1].itemId"]).toMatch(/once/);
    expect(errors["positions[1].inTransit"]).toMatch(/whole number/);
    expect(validateInput({ ...ac3910, conversionDate: "1 Nov" }).errors.conversionDate).toMatch(/YYYY-MM-DD/);
    const none = validateInput({ ...ac3910, closeThrough: "", blockers: { NegativeOnHand: false, InTransit: false, StartedOrders: false } }).input;
    expect(none?.blockers).toEqual([]);
    expect(none).not.toHaveProperty("closeThrough");
  });

  it("marks done, next and waiting checklist steps", () => {
    const run: ConversionRun = { status: "Draft", checklist: [{ step: "select", status: "Done", user: "alice", at: "2026-10-28T09:00:00Z" }] };
    expect(stepRows(run).map((r) => r.state)).toEqual(["Done", "Next", "Waiting", "Waiting", "Waiting", "Waiting"]);
    expect(stepRows(run)[0].user).toBe("alice");
  });

  it("lists costing sheet subtotals by node", () => {
    const r: CostResult = { itemId: "X", status: "Pending", level: 1, unitCost: "1", own: {}, rolled: {}, subtotals: { T: "22.5", B: "7.5" } };
    expect(subtotalRows(r)).toEqual([
      { node: "B", value: "7.5" },
      { node: "T", value: "22.5" },
    ]);
  });
});

describe("costing versions page", () => {
  it("starts empty, adds a version and blocks then activates it through the service", async () => {
    handler = (url, body) => {
      const { version } = body as { version: { status: string } };
      if (url.pathname === "/api/v1/costing/versions:transition") return { status: 200, body: { version: { ...version, status: "Blocked" } } };
      if (url.pathname === "/api/v1/costing/versions:activate") return { status: 200, body: { version: { ...version, status: "Active" } } };
      return undefined;
    };
    const { container } = renderAt("/productcost/versions");
    expect(screen.getByRole("heading", { name: "No costing versions yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Version code"), { target: { value: "STD-2027" } });
    fireEvent.click(screen.getByRole("button", { name: "Add version" }));
    const table = await screen.findByRole("table", { name: "Costing versions" });
    expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Open");
    expect(calls).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Block STD-2027" }));
    await waitFor(() => expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Blocked"));
    expect(calls[0].body).toEqual({ version: { id: "STD-2027", type: "Standard", status: "Open", fallback: "Active" }, action: "block" });

    fireEvent.click(screen.getByRole("button", { name: "Activate STD-2027" }));
    await waitFor(() => expect(within(table).getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Active"));
    expect(calls[1].url.pathname).toBe("/api/v1/costing/versions:activate");
    expect(calls[1].body).toEqual({ version: { id: "STD-2027", type: "Standard", status: "Blocked", fallback: "Active" } });
    expect(screen.getByRole("button", { name: "Close STD-2027" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the service's refusal and keeps the version unchanged", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/costing/versions:activate"
        ? { status: 403, body: { code: "AUTH-0003", detail: "missing duty CostingActivate" } }
        : undefined;
    renderAt("/productcost/versions");
    fireEvent.change(screen.getByLabelText("Version code"), { target: { value: "STD-2027" } });
    fireEvent.click(screen.getByRole("button", { name: "Add version" }));
    fireEvent.click(await screen.findByRole("button", { name: "Activate STD-2027" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty CostingActivate (AUTH-0003)");
    expect(screen.getByRole("row", { name: /STD-2027/ })).toHaveTextContent("Open");
  });

  it("refuses a specified fallback without a version before adding it", () => {
    renderAt("/productcost/versions");
    fireEvent.change(screen.getByLabelText("Version code"), { target: { value: "STD-2027" } });
    fireEvent.change(screen.getByLabelText("Fallback principle"), { target: { value: "SpecifiedVersion" } });
    fireEvent.click(screen.getByRole("button", { name: "Add version" }));
    expect(screen.getByText("Name the version to fall back to.")).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Costing versions" })).not.toBeInTheDocument();
  });
});

const ac3910Report: ConversionReport = {
  currency: "USD",
  blocked: false,
  closeComplete: true,
  lines: [{ itemId: "RM-301", siteId: "PLANT-1", onHand: "500", actualValue: "4925", standardUnit: "10", amount: "75" }],
  revaluation: "75",
  journal: [
    { account: "Inventory", debit: "75", credit: "0" },
    { account: "CostChangeVariance", debit: "0", credit: "75" },
  ],
};

describe("standard cost conversion page", () => {
  it("validates AC-39.10 and shows the 75.00 revaluation and its journal", async () => {
    handler = (url) => (url.pathname === "/api/v1/costing/conversions:validate" ? { status: 200, body: ac3910Report } : undefined);
    const { container } = renderAt("/productcost/conversion");
    expect(screen.getByRole("heading", { name: "Not validated yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Validate and preview" }));
    const journal = await screen.findByRole("table", { name: "Conversion journal" });
    expect(calls[0].body).toEqual(validateInput(ac3910).input);
    expect(screen.getByText("Ready to convert")).toBeInTheDocument();
    expect(within(journal).getByRole("row", { name: /Inventory/ })).toHaveTextContent("75.00");
    expect(within(screen.getByRole("table", { name: "Revaluation by item and site" })).getByRole("row", { name: /RM-301/ })).toHaveTextContent(
      "4925.00",
    );
    expect(pageViolations(container)).toEqual([]);
  });

  it("runs the checklist step by step and records who ran each step", async () => {
    handler = (url, body) => {
      if (url.pathname !== "/api/v1/costing/conversions:advance") return undefined;
      const { run, step } = body as { run: ConversionRun; step: string };
      const checklist = [...run.checklist, { step, status: "Done", user: "alice", at: "2026-10-28T09:00:00Z" }];
      return { status: 200, body: { run: { status: "Draft", checklist }, report: ac3910Report } };
    };
    renderAt("/productcost/conversion");
    fireEvent.click(screen.getByRole("button", { name: "Run step: Select items and sites" }));
    await screen.findByRole("button", { name: "Run step: Roll pending standards" });
    expect(calls[0].body).toEqual({ run: { status: "Draft", checklist: [] }, step: "select", input: validateInput(ac3910).input });
    const checklist = screen.getByRole("table", { name: "Conversion checklist" });
    expect(within(checklist).getByRole("row", { name: /Select items and sites/ })).toHaveTextContent("alice");
    expect(within(checklist).getByRole("row", { name: /Roll pending standards/ })).toHaveTextContent("Next");

    fireEvent.click(screen.getByRole("button", { name: "Run step: Roll pending standards" }));
    await screen.findByRole("button", { name: "Run step: Validate" });
    expect((calls[1].body as { run: ConversionRun }).run.checklist).toHaveLength(1);
  });

  it("shows a refused step with its code and keeps the checklist", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/costing/conversions:advance"
        ? { status: 422, body: { code: "CST-0736", detail: "step validate cannot run before select" } }
        : undefined;
    renderAt("/productcost/conversion");
    fireEvent.click(screen.getByRole("button", { name: "Run step: Select items and sites" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("(CST-0736)");
    expect(screen.getByRole("button", { name: "Run step: Select items and sites" })).toBeInTheDocument();
  });

  it("explains a blocked conversion", async () => {
    const blocked: ConversionReport = {
      ...ac3910Report,
      blocked: true,
      diagnostics: [{ code: "CST-0732", severity: "error", message: "negative physical on-hand blocks conversion", ref: "RM-301@PLANT-1" }],
    };
    handler = (url) => (url.pathname === "/api/v1/costing/conversions:validate" ? { status: 200, body: blocked } : undefined);
    renderAt("/productcost/conversion");
    fireEvent.click(screen.getByRole("button", { name: "Validate and preview" }));
    expect(await screen.findByText("The conversion is blocked")).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Conversion diagnostics" })).getByRole("row", { name: /CST-0732/ })).toBeInTheDocument();
  });

  it("checks the positions before calling the service", () => {
    renderAt("/productcost/conversion");
    fireEvent.change(screen.getByLabelText("Site"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Validate and preview" }));
    expect(screen.getByText("Enter the site.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("cost roll-up page valuation view", () => {
  it("sends the Group view and shows the costing sheet subtotals", async () => {
    const result: CostResult = {
      itemId: "FG-100",
      status: "Pending",
      level: 1,
      unitCost: "132",
      own: { DM: "100", DL: "20", IND: "12" },
      rolled: {},
      subtotals: { "B-DM": "100", T: "120" },
    };
    handler = (url) =>
      url.pathname === "/api/v1/costing/calculations" ? { status: 200, body: { result, split: { DM: "100", DL: "20", IND: "12" } } } : undefined;
    renderAt("/productcost/roll");
    fireEvent.change(screen.getByLabelText("Valuation view"), { target: { value: "Group" } });
    fireEvent.click(screen.getByRole("button", { name: "Roll item" }));
    const table = await screen.findByRole("table", { name: "Costing sheet subtotals of FG-100" });
    expect((calls[0].body as { options: { view: string } }).options.view).toBe("Group");
    expect(within(table).getByRole("row", { name: /^T/ })).toHaveTextContent("120.00");
  });
});
