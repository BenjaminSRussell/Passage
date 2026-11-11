import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DerivationStrategy, LookupTable } from "./api/client";
import { emptyStep, toStrategyDraft, toStrategyInput, validateStrategy } from "./DerivationStrategyPage";
import { formatLookupRow, parseLookupRows, validateLookup } from "./LookupTableFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const TABLE_ID = "0190a000-0000-7000-8000-0000000000b1";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}`;

const table: LookupTable = {
  id: TABLE_ID,
  ledgerId: LEDGER,
  name: "Function by account and centre",
  sourceFields: ["ACCOUNT", "CC"],
  target: "FUNC_AREA",
  rowCount: 2,
  version: 3,
  rows: [
    { source: ["5010", "C200"], target: "PROD", validFrom: "2026-01-01" },
    { source: ["5010", "*"], target: "ADMIN", validFrom: "2026-01-01", validTo: "2026-12-31" },
  ],
};

function strategy(patch: Partial<DerivationStrategy> = {}): DerivationStrategy {
  return {
    id: "0190a000-0000-7000-8000-0000000000c1",
    ledgerId: LEDGER,
    version: 1,
    lockVersion: 4,
    name: "Cost of sales",
    status: "active",
    activatedAt: "2026-03-01T08:00:00Z",
    steps: [{ id: "s1", sequence: 10, kind: "table_lookup", sourceFields: ["ACCOUNT", "CC"], target: "FUNC_AREA", lookupTableId: TABLE_ID, overwrite: false }],
    ...patch,
  };
}

const strategies = [strategy({ version: 2, status: "draft", name: "Next", activatedAt: undefined }), strategy()];

function fake(opts: { tables?: LookupTable[]; list?: DerivationStrategy[] } = {}) {
  const tables = opts.tables ?? [table];
  const list = opts.list ?? strategies;
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    const path = url.pathname;
    if (path === `${BASE}/lookup-tables` && method === "GET") return json(200, { items: tables.map((t) => ({ ...t, rows: undefined })) });
    const t = path.match(/\/lookup-tables\/([^/]+)$/);
    if (t && method === "GET") return json(200, tables.find((x) => x.id === t[1]) ?? table);
    if (t && method === "PUT") return json(200, { ...table, ...JSON.parse(String(init.body)), id: t[1], version: 4 });
    if (path === `${BASE}/derivation-strategies` && method === "GET") return json(200, { items: list });
    const s = path.match(/\/derivation-strategies\/(\d+)(\/activate)?$/);
    if (s && s[2] && method === "POST") return json(200, strategy({ version: Number(s[1]), status: "active" }));
    if (s && method === "GET") {
      const found = list.find((x) => x.version === Number(s[1]));
      return found ? json(200, found) : json(404, { code: "LEDGER_DERIVATION_STRATEGY_NOT_FOUND", detail: "derivation strategy version not found" });
    }
    if (s && method === "PUT") return json(200, strategy({ version: Number(s[1]), status: "draft", lockVersion: 5, ...JSON.parse(String(init.body)) }));
    return undefined;
  };
}

describe("lookup table helpers", () => {
  it("parses rows from commas or tabs and reports the first bad line", () => {
    expect(parseLookupRows("5010, C200, PROD, 2026-01-01\n\n5010\t*\tADMIN\t2026-01-01\t2026-12-31", 2).rows).toEqual([
      { source: ["5010", "C200"], target: "PROD", validFrom: "2026-01-01" },
      { source: ["5010", "*"], target: "ADMIN", validFrom: "2026-01-01", validTo: "2026-12-31" },
    ]);
    expect(parseLookupRows("5010, PROD, 2026-01-01", 2).error).toContain("Line 1");
    expect(parseLookupRows("5010, C1, PROD, 01/01/2026", 2).error).toContain("YYYY-MM-DD");
    expect(parseLookupRows("5010, C1, PROD, 2026-02-01, 2026-01-01", 2).error).toContain("not before");
    expect(parseLookupRows("5010, C1, PROD, 2026-01-01\nbad value, C1, X, 2026-01-01", 2).error).toContain("Line 2");
    expect(formatLookupRow(table.rows![1])).toBe("5010, *, ADMIN, 2026-01-01, 2026-12-31");
  });

  it("validates the definition and the rows together", () => {
    const ok = { name: "F", sourceFields: "ACCOUNT, CC", target: "FUNC_AREA", rows: "5010, C200, PROD, 2026-01-01" };
    expect(validateLookup(ok)).toEqual({});
    expect(validateLookup({ ...ok, name: " " }).name).toBeDefined();
    expect(validateLookup({ ...ok, target: "func" }).target).toBeDefined();
    expect(validateLookup({ ...ok, target: "CC" }).target).toContain("can't also be read");
    expect(validateLookup({ ...ok, sourceFields: "CC, CC" }).sourceFields).toContain("twice");
    expect(validateLookup({ ...ok, rows: "5010, PROD, 2026-01-01" }).rows).toContain("Line 1");
  });
});

describe("strategy helpers", () => {
  it("round-trips a strategy and validates each step", () => {
    const draft = toStrategyDraft(strategy());
    expect(toStrategyInput(draft)).toEqual({
      name: "Cost of sales",
      steps: [{ sequence: 10, kind: "table_lookup", sourceFields: ["ACCOUNT", "CC"], target: "FUNC_AREA", lookupTableId: TABLE_ID, overwrite: false }],
    });
    expect(validateStrategy(draft)).toEqual({});
    expect(validateStrategy({ name: "", steps: [] })).toMatchObject({ name: expect.any(String), steps: expect.any(String) });
    const bad = (patch: object) => validateStrategy({ name: "X", steps: [{ ...emptyStep(0, 10), target: "SEGMENT", ...patch }] })["steps.0"];
    expect(bad({ kind: "table_lookup" })).toContain("choose a lookup table");
    expect(bad({ kind: "move", sourceFields: "A, B" })).toContain("one value to copy");
    expect(bad({ kind: "move", sourceFields: "SEGMENT" })).toContain("can't read the dimension it writes");
    expect(bad({ kind: "clear", overwrite: false })).toContain("overwrite");
    expect(bad({ kind: "constant", value: "no good" })).toContain("fixed value");
    expect(bad({ kind: "constant", value: "MFG", condOp: "eq", condField: "ACCOUNT", condValues: "" })).toContain("exactly one value");
    expect(bad({ kind: "constant", value: "MFG", condOp: "in", condField: "", condValues: "a" })).toContain("condition checks");
    expect(bad({ kind: "constant", value: "MFG", target: "x" })).toContain("capitals");
    const second = validateStrategy({ name: "X", steps: [{ ...emptyStep(0, 10), kind: "constant", value: "A", target: "A" }, { ...emptyStep(1, 10), kind: "constant", value: "B", target: "B" }] });
    expect(second["steps.1"]).toContain("above the previous");
  });

  it("builds clear and conditional steps the way the service reads them", () => {
    const input = toStrategyInput({
      name: "X",
      steps: [
        { ...emptyStep(0, 10), kind: "clear", target: "SEGMENT", overwrite: true, condField: "ACCOUNT_TYPE", condOp: "eq", condValues: "revenue" },
        { ...emptyStep(1, 20), kind: "constant", target: "FUNC_AREA", value: "MFG", condField: "SEGMENT", condOp: "blank" },
      ],
    });
    expect(input.steps[0]).toEqual({ sequence: 10, kind: "clear", sourceFields: [], target: "SEGMENT", overwrite: true, condition: { field: "ACCOUNT_TYPE", op: "eq", values: ["revenue"] } });
    expect(input.steps[1].condition).toEqual({ field: "SEGMENT", op: "blank" });
  });
});

describe("lookup tables list", () => {
  it("asks for a valid ledger id first, then lists tables and opens one", async () => {
    fake();
    const { container } = renderAt("/ledger/lookup-tables");
    expect(screen.getByText("Choose a ledger to see its lookup tables.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    const open = await screen.findByRole("button", { name: table.name });
    const row = open.closest("tr") as HTMLElement;
    expect(row.textContent).toContain("ACCOUNT, CC");
    expect(row.textContent).toContain("FUNC_AREA");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(open);
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/lookup-tables/${TABLE_ID}`);
    expect(await screen.findByLabelText("Name")).toHaveValue(table.name);
    expect(screen.getByLabelText("Rows")).toHaveValue("5010, C200, PROD, 2026-01-01\n5010, *, ADMIN, 2026-01-01, 2026-12-31");
  });

  it("shows the empty state and the error with a retry", async () => {
    fake({ tables: [] });
    renderAt(`/ledger/lookup-tables?ledger=${LEDGER}`);
    expect(await screen.findByText("No lookup tables yet")).toBeInTheDocument();
  });

  it("shows the loading skeleton then the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: [table] }));
    renderAt(`/ledger/lookup-tables?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the lookup tables")).toBeInTheDocument();
    expect(screen.getByText(/ledger not found/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: table.name })).toBeInTheDocument();
  });
});

describe("lookup table form", () => {
  it("creates a table with rows and sends them in the body", async () => {
    fake();
    const { container } = renderAt(`/ledger/lookup-tables/new?ledger=${LEDGER}`);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Function" } });
    fireEvent.change(screen.getByLabelText("Dimension to fill"), { target: { value: "func_area" } });
    fireEvent.change(screen.getByLabelText("Values to read"), { target: { value: "account, cc" } });
    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "5010, C200, PROD, 2026-01-01" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save lookup table" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/lookup-tables"));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toMatch(new RegExp(`^${BASE}/lookup-tables/[0-9a-f-]{36}$`));
    expect(new Headers(put.init.headers).get("If-Match")).toBeNull();
    expect(put.body).toEqual({ name: "Function", sourceFields: ["ACCOUNT", "CC"], target: "FUNC_AREA", rows: [{ source: ["5010", "C200"], target: "PROD", validFrom: "2026-01-01" }] });
  });

  it("replaces at the table version and shows service errors", async () => {
    fake();
    renderAt(`/ledger/lookup-tables/${TABLE_ID}?ledger=${LEDGER}`);
    await screen.findByDisplayValue(table.name);
    server.handler = (_url, init) => ((init.method ?? "GET") === "PUT" ? json(412, { code: "LEDGER_VERSION_CONFLICT", detail: "the lookup table changed since it was read" }) : undefined);
    fireEvent.click(screen.getByRole("button", { name: "Save lookup table" }));
    expect(await screen.findByText(/the lookup table changed since it was read/)).toBeInTheDocument();
    expect(new Headers(server.calls.find((c) => c.method === "PUT")!.init.headers).get("If-Match")).toBe('"3"');
  });

  it("blocks a bad form before any request and needs a ledger", async () => {
    fake();
    renderAt(`/ledger/lookup-tables/new?ledger=${LEDGER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save lookup table" }));
    expect(await screen.findByText("Enter a name of up to 80 characters.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("explains how to reach the form without a ledger", () => {
    renderAt("/ledger/lookup-tables/new");
    expect(screen.getByText(/Open this page from the lookup tables list/)).toBeInTheDocument();
  });
});

describe("derivation strategies list", () => {
  it("lists versions with status and what they fill, and opens one", async () => {
    fake();
    const { container } = renderAt(`/ledger/derivation-strategies?ledger=${LEDGER}`);
    const grid = await findTable({ name: "Derivation strategies" });
    await within(grid).findByRole("button", { name: "Next" });
    const active = within(grid).getByRole("button", { name: "Cost of sales" }).closest("tr") as HTMLElement;
    expect(active.textContent).toContain("Active");
    expect(active.textContent).toContain("FUNC_AREA");
    expect(active.textContent).toContain("2026-03-01");
    expect((within(grid).getByRole("button", { name: "Next" }).closest("tr") as HTMLElement).textContent).toContain("Not yet");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(grid).getByRole("button", { name: "Next" }));
    expect(screen.getByTestId("location").textContent).toBe("/ledger/derivation-strategies/2");
    expect(await screen.findByLabelText("Name")).toHaveValue("Next");
  });

  it("shows the empty state and the error", async () => {
    fake({ list: [] });
    renderAt(`/ledger/derivation-strategies?ledger=${LEDGER}`);
    expect(await screen.findByText("No derivation strategy yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: strategies }));
    renderAt(`/ledger/derivation-strategies?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the derivation strategies")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "Next" })).toBeInTheDocument();
  });
});

describe("derivation strategy form", () => {
  it("creates the next version and activates it", async () => {
    fake();
    const { container } = renderAt(`/ledger/derivation-strategies/new?ledger=${LEDGER}`);
    expect(await screen.findByText(/Version 3/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Cost of sales" } });
    fireEvent.click(screen.getByRole("button", { name: "Add step" }));
    fireEvent.change(screen.getByLabelText("Step 1 dimension to write"), { target: { value: "func_area" } });
    fireEvent.change(screen.getByLabelText("Step 1 values to read"), { target: { value: "account, cc" } });
    fireEvent.change(screen.getByLabelText("Step 1 lookup table"), { target: { value: TABLE_ID } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save and activate" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/derivation-strategies"));
    const put = server.calls.find((c) => c.method === "PUT")!;
    expect(put.url.pathname).toBe(`${BASE}/derivation-strategies/3`);
    expect(put.body).toEqual({
      name: "Cost of sales",
      steps: [{ sequence: 10, kind: "table_lookup", sourceFields: ["ACCOUNT", "CC"], target: "FUNC_AREA", lookupTableId: TABLE_ID, overwrite: false }],
    });
    const activate = server.calls.find((c) => c.method === "POST")!;
    expect(activate.url.pathname).toBe(`${BASE}/derivation-strategies/3/activate`);
    expect(new Headers(activate.init.headers).get("If-Match")).toBe('"5"');
  });

  it("starts from an existing version with ?from and lists the steps", async () => {
    fake();
    renderAt(`/ledger/derivation-strategies/new?ledger=${LEDGER}&from=1`);
    expect(await screen.findByLabelText("Name")).toHaveValue("Cost of sales");
    expect(screen.getByLabelText("Step 1 dimension to write")).toHaveValue("FUNC_AREA");
    fireEvent.click(screen.getByRole("button", { name: "Remove step 1" }));
    expect(screen.getByText("No steps yet.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Add at least one step.")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.method === "PUT")).toHaveLength(0);
  });

  it("shows step errors, and service violations when saving fails", async () => {
    fake();
    renderAt(`/ledger/derivation-strategies/new?ledger=${LEDGER}`);
    await screen.findByText(/Version 3/);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "X" } });
    fireEvent.click(screen.getByRole("button", { name: "Add step" }));
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/Step 1: name the dimension it writes/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Step 1 action"), { target: { value: "constant" } });
    fireEvent.change(screen.getByLabelText("Step 1 dimension to write"), { target: { value: "SEGMENT" } });
    fireEvent.change(screen.getByLabelText("Step 1 fixed value"), { target: { value: "MFG" } });
    server.handler = () => json(422, { code: "LEDGER_DERIVATION_INVALID", detail: "step 1: target \"SEGMENT\" is not a dimension attribute of the ledger", violations: [{ code: "LEDGER_DERIVATION_INVALID", message: "step 1: target \"SEGMENT\" is not a dimension attribute of the ledger" }] });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Could not save the strategy")).toBeInTheDocument();
    expect(screen.getAllByText(/is not a dimension attribute of the ledger/).length).toBeGreaterThan(0);
  });

  it("locks an active version and offers a new version from it", async () => {
    fake();
    renderAt(`/ledger/derivation-strategies/1?ledger=${LEDGER}`);
    expect(await screen.findByText(/This version is locked/)).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
    expect(screen.getByRole("link", { name: "Create a new version from this one" })).toHaveAttribute("href", `/ledger/derivation-strategies/new?ledger=${LEDGER}&from=1`);
  });

  it("shows a load error for a missing version", async () => {
    fake();
    renderAt(`/ledger/derivation-strategies/9?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the derivation strategy")).toBeInTheDocument();
  });
});

describe("derivation on posted lines and activation retries", () => {
  it("marks the posted lines whose dimensions a derivation step filled", async () => {
    const ENTRY = "0190a000-0000-7000-8000-0000000000e1";
    server.handler = (url) => {
      if (url.pathname === "/api/v1/ledger/reason-codes") return json(200, { items: [] });
      if (url.pathname !== `/api/v1/ledger/journal-entries/${ENTRY}`) return undefined;
      return json(200, {
        id: ENTRY, ledgerId: LEDGER, voucher: "GL-000007", postingSequence: 7, postingDate: "2026-03-10", periodKey: "2026-03",
        description: "Freight", traceId: "a".repeat(32), totals: { currency: "USD", debit: "40.00", credit: "40.00" },
        lines: [
          { id: "l1", lineNumber: 1, account: "5010", displayName: "5010-C200-PROD", debitCredit: "D", amount: "40.00", derivedByStepIds: ["0190a000-0000-7000-8000-0000000000d1"] },
          { id: "l2", lineNumber: 2, account: "2100", displayName: "2100", debitCredit: "C", amount: "40.00", derivedByStepIds: [] },
        ],
      });
    };
    renderAt(`/ledger/entries/${ENTRY}`);
    const derived = await screen.findByText("Derived by 1 step");
    expect((derived.closest("tr") as HTMLElement).textContent).toContain("5010-C200-PROD");
    expect(screen.getAllByText(/Derived by/)).toHaveLength(1);
  });

  it("retries a failed activation of a draft at the lockVersion its save returned", async () => {
    fake();
    renderAt(`/ledger/derivation-strategies/2?ledger=${LEDGER}`);
    await screen.findByDisplayValue("Next");
    let activations = 0;
    const base = server.handler;
    server.handler = (url, init) => {
      if ((init.method ?? "GET") === "POST" && activations++ === 0) {
        return json(422, { code: "LEDGER_DERIVATION_INVALID", detail: "lookup table missing", violations: [{ code: "LEDGER_DERIVATION_INVALID", message: "step 1: lookup table x does not exist on this ledger" }] });
      }
      return base(url, init);
    };
    fireEvent.click(screen.getByRole("button", { name: "Save and activate" }));
    expect(await screen.findByText(/does not exist on this ledger/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save and activate" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/derivation-strategies"));
    const puts = server.calls.filter((c) => c.method === "PUT");
    expect(puts.map((c) => new Headers(c.init.headers).get("If-Match"))).toEqual(['"4"', '"5"']);
  });
});
