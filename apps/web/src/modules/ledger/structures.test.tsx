import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DimensionValue, Structure } from "./api/client";
import { validateValue, valueStatus } from "./DimensionValuesPage";
import { emptyStructure, parseUnder, toStructureInput, validateStructure, validToken } from "./StructureFormPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const ACTIVE_ID = "0190a000-0000-7000-8000-000000000010";
const DRAFT_ID = "0190a000-0000-7000-8000-000000000011";
const BASE = `/api/v1/ledger/ledgers/${LEDGER}`;

const ledger = { id: LEDGER, code: "GL", name: "General ledger", chartCode: "STD", currency: "USD", dimensionAttributes: ["DEPT", "CC"] };

function structure(patch: Partial<Structure> = {}): Structure {
  return {
    id: ACTIVE_ID,
    ledgerId: LEDGER,
    name: "PL",
    version: 1,
    priority: 0,
    effectiveFrom: "2026-01-01",
    status: "active",
    segments: ["DEPT", "CC"],
    accountFilter: ["6000..6999"],
    constraints: [
      { segment: "DEPT", under: { MAIN_ACCOUNT: "6010" }, allowed: ["*"] },
      { segment: "CC", allowed: ["C200", "blank"] },
    ],
    ...patch,
  };
}

const structures = [structure(), structure({ id: DRAFT_ID, name: "BS", status: "draft", accountFilter: [], segments: [], constraints: [] })];

/** A fake service: the ledger, its structures (GET by id too) and a PUT that echoes the body. */
function structureServer(list: Structure[] = structures) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === BASE && method === "GET") return json(200, ledger);
    if (url.pathname === `${BASE}/account-structures` && method === "GET") return json(200, { items: list });
    const m = url.pathname.match(/\/account-structures\/([^/]+)$/);
    if (m && method === "GET") {
      const found = list.find((s) => s.id === m[1]);
      return found ? json(200, found) : json(404, { code: "LEDGER_STRUCTURE_NOT_FOUND", detail: "account structure not found" });
    }
    if (m && method === "PUT") return json(201, { ...JSON.parse(String(init.body)), id: m[1], ledgerId: LEDGER });
    return undefined;
  };
}

const puts = () => server.calls.filter((c) => c.method === "PUT");

describe("structure form helpers", () => {
  it("parses conditions and tokens the way the service reads them", () => {
    expect(parseUnder("MAIN_ACCOUNT=6000..6999, DEPT=D100")).toEqual({ MAIN_ACCOUNT: "6000..6999", DEPT: "D100" });
    expect(parseUnder("")).toEqual({});
    expect(parseUnder("DEPT")).toBeNull();
    expect(parseUnder("DEPT=D1, DEPT=D2")).toBeNull();
    expect(validToken("6000..6999")).toBe(true);
    expect(validToken("6999..6000")).toBe(false);
    expect(validToken("blank")).toBe(true);
    expect(validToken("a b")).toBe(false);
  });

  it("checks segments against the ledger and rule conditions against earlier segments", () => {
    const draft = { ...emptyStructure, name: "PL", effectiveFrom: "2026-01-01", segments: "DEPT, PROJECT" };
    expect(validateStructure(draft, ["DEPT", "CC"]).segments).toContain("PROJECT");
    const ruled = {
      ...draft,
      segments: "DEPT, CC",
      rules: [{ key: 0, segment: "DEPT", under: "CC=C200", allowed: "*" }],
    };
    expect(validateStructure(ruled, ["DEPT", "CC"])["rules.0"]).toContain("earlier segment");
    const ok = { ...ruled, rules: [{ key: 0, segment: "CC", under: "MAIN_ACCOUNT=6010, DEPT=D100", allowed: "C200, blank" }] };
    expect(validateStructure(ok, ["DEPT", "CC"])).toEqual({});
    expect(toStructureInput(ok, "active")).toEqual({
      name: "PL",
      version: 1,
      priority: 0,
      effectiveFrom: "2026-01-01",
      status: "active",
      segments: ["DEPT", "CC"],
      accountFilter: [],
      constraints: [{ segment: "CC", under: { MAIN_ACCOUNT: "6010", DEPT: "D100" }, allowed: ["C200", "blank"] }],
    });
  });
});

describe("account structures list", () => {
  it("asks for a valid ledger id before loading anything", async () => {
    structureServer();
    renderAt("/ledger/structures");
    expect(screen.getByText("Choose a ledger to see its account structures.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Ledger"), { target: { value: LEDGER } });
    expect(await screen.findByRole("button", { name: "PL" })).toBeInTheDocument();
  });

  it("lists versions with status, accounts and segments and opens one", async () => {
    structureServer();
    const { container } = renderAt(`/ledger/structures?ledger=${LEDGER}`);
    const table = await findTable({ name: "Account structures" });
    await within(table).findByRole("button", { name: "PL" });
    const pl = within(table).getByRole("button", { name: "PL" }).closest("tr") as HTMLElement;
    expect(pl.textContent).toContain("Active");
    expect(pl.textContent).toContain("6000..6999");
    expect(pl.textContent).toContain("Main account - DEPT - CC");
    const bs = within(table).getByRole("button", { name: "BS" }).closest("tr") as HTMLElement;
    expect(bs.textContent).toContain("Draft");
    expect(bs.textContent).toContain("All");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: "BS" }));
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/structures/${DRAFT_ID}`);
    expect(await screen.findByLabelText("Name")).toHaveValue("BS");
  });

  it("shows the empty state", async () => {
    structureServer([]);
    renderAt(`/ledger/structures?ledger=${LEDGER}`);
    expect(await screen.findByText("No account structures yet")).toBeInTheDocument();
  });

  it("shows the error and retries", async () => {
    let fail = true;
    server.handler = () => (fail ? json(404, { code: "LEDGER_NOT_FOUND", detail: "ledger not found" }) : json(200, { items: structures }));
    renderAt(`/ledger/structures?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the account structures")).toBeInTheDocument();
    expect(screen.getByText("ledger not found")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "PL" })).toBeInTheDocument();
  });
});

describe("account structure form", () => {
  it("creates and activates a structure with a constraint rule", async () => {
    structureServer();
    const { container } = renderAt(`/ledger/structures/new?ledger=${LEDGER}`);
    expect(await screen.findByText(/This ledger's dimensions: DEPT, CC/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "PL" } });
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-04-01" } });
    fireEvent.change(screen.getByLabelText("Segments"), { target: { value: "DEPT, CC" } });
    fireEvent.change(screen.getByLabelText("Main accounts"), { target: { value: "6000..6999" } });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.change(screen.getByLabelText("Rule 1 conditions"), { target: { value: "MAIN_ACCOUNT=6010" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save and activate" }));

    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/ledger/structures"));
    const [put] = puts();
    expect(put.url.pathname).toMatch(new RegExp(`^${BASE}/account-structures/[0-9a-f-]{36}$`));
    expect(put.body).toEqual({
      name: "PL",
      version: 1,
      priority: 0,
      effectiveFrom: "2026-04-01",
      status: "active",
      segments: ["DEPT", "CC"],
      accountFilter: ["6000..6999"],
      constraints: [{ segment: "DEPT", under: { MAIN_ACCOUNT: "6010" }, allowed: ["*"] }],
    });
  });

  it("checks the form before saving", async () => {
    structureServer();
    renderAt(`/ledger/structures/new?ledger=${LEDGER}`);
    await screen.findByText(/This ledger's dimensions/);
    fireEvent.change(screen.getByLabelText("Segments"), { target: { value: "PROJECT" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/Not a dimension of this ledger: PROJECT/)).toBeInTheDocument();
    expect(screen.getByText(/Use 1 to 60 letters/)).toBeInTheDocument();
    expect(screen.getByText("Enter the date the version applies from.")).toBeInTheDocument();
    expect(puts()).toHaveLength(0);
  });

  it("shows every violation the service returns", async () => {
    structureServer();
    const inner = server.handler;
    server.handler = (url, init) =>
      init.method === "PUT"
        ? json(422, {
            code: "LEDGER_STRUCTURE_INVALID",
            detail: "segment PROJECT is not a dimension attribute of this ledger",
            violations: [
              { code: "LEDGER_STRUCTURE_INVALID", field: "segments", message: "segment PROJECT is not a dimension attribute of this ledger" },
              { code: "LEDGER_STRUCTURE_INVALID", field: "constraints[0].allowed", message: "token \"x y\" is not valid" },
            ],
          })
        : inner(url, init);
    renderAt(`/ledger/structures/${DRAFT_ID}?ledger=${LEDGER}`);
    expect(await screen.findByLabelText("Name")).toHaveValue("BS");
    fireEvent.change(screen.getByLabelText("Effective from"), { target: { value: "2026-02-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    const alert = await screen.findByText("Could not save the account structure");
    const box = alert.closest("[role=alert]") as HTMLElement;
    expect(box.textContent).toContain("LEDGER_STRUCTURE_INVALID");
    expect(box.textContent).toContain("constraints[0].allowed");
    expect(screen.getByTestId("location").textContent).toBe(`/ledger/structures/${DRAFT_ID}`);
  });

  it("locks an active version and retires it unchanged", async () => {
    structureServer();
    renderAt(`/ledger/structures/${ACTIVE_ID}?ledger=${LEDGER}`);
    expect(await screen.findByLabelText("Name")).toHaveAttribute("readonly");
    expect(screen.getByText(/This version is locked/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
    expect(screen.getByLabelText("Rule 1 conditions")).toHaveValue("MAIN_ACCOUNT=6010");
    fireEvent.click(screen.getByRole("button", { name: "Retire version" }));
    await waitFor(() => expect(puts()).toHaveLength(1));
    const { status, ...definition } = puts()[0].body as Structure;
    expect(status).toBe("retired");
    const { id: _id, ledgerId: _ledger, status: _status, ...original } = structure();
    expect(definition).toEqual(original);
  });

  it("starts a new version from a locked one", async () => {
    structureServer();
    renderAt(`/ledger/structures/${ACTIVE_ID}?ledger=${LEDGER}`);
    fireEvent.click(await screen.findByRole("link", { name: "New version" }));
    expect(await screen.findByLabelText("Version")).toHaveValue("2");
    expect(screen.getByLabelText("Name")).toHaveValue("PL");
    expect(screen.getByLabelText("Name")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Effective from")).toHaveValue("");
  });

  it("shows a not-found structure as an error", async () => {
    structureServer();
    renderAt(`/ledger/structures/0190a000-0000-7000-8000-0000000000ff?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the account structure")).toBeInTheDocument();
    expect(screen.getByText("account structure not found")).toBeInTheDocument();
  });
});

const values: DimensionValue[] = [
  { attribute: "DEPT", value: "D100", name: "Sales", suspended: false },
  { attribute: "DEPT", value: "D200", name: "Old sales", suspended: true },
  { attribute: "CC", value: "C900", name: "Closed", activeTo: "2000-12-31", suspended: false },
];

function valueServer(list: DimensionValue[] = values) {
  server.handler = (url, init) => {
    const method = init.method ?? "GET";
    if (url.pathname === BASE) return json(200, ledger);
    if (url.pathname === `${BASE}/dimension-values` && method === "GET") return json(200, { items: list });
    const m = url.pathname.match(/\/dimension-values\/([^/]+)\/([^/]+)$/);
    if (m && method === "PUT") {
      const body = JSON.parse(String(init.body));
      return json(201, { attribute: m[1], value: m[2], name: body.name, suspended: body.suspended, ...(body.activeFrom ? { activeFrom: body.activeFrom } : {}) });
    }
    return undefined;
  };
}

describe("dimension values", () => {
  it("derives the status of a value on a day", () => {
    expect(valueStatus(values[0], "2026-03-01")).toBe("active");
    expect(valueStatus(values[1], "2026-03-01")).toBe("suspended");
    expect(valueStatus(values[2], "2026-03-01")).toBe("expired");
    expect(valueStatus({ ...values[0], activeFrom: "2027-01-01" }, "2026-03-01")).toBe("not yet active");
    expect(validateValue({ attribute: "PROJECT", value: "a b", name: "", activeFrom: "2026-02-01", activeTo: "2026-01-01", suspended: false }, ["DEPT"])).toEqual({
      attribute: "Pick one of the ledger's dimensions.",
      value: "Use 1 to 20 letters, digits, dots, dashes or underscores.",
      activeTo: "The end date can't be before the start date.",
    });
  });

  it("lists the register and saves a value", async () => {
    valueServer();
    const { container } = renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    const table = await findTable({ name: "Dimension values" });
    await within(table).findByText("Old sales");
    expect(within(table).getByText("Old sales").closest("tr")?.textContent).toContain("Suspended");
    expect(within(table).getByText("Closed").closest("tr")?.textContent).toContain("Expired");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Dimension"), { target: { value: "CC" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "C200" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Freight" } });
    fireEvent.change(screen.getByLabelText("Active from"), { target: { value: "2026-01-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Save value" }));
    expect(await screen.findByText("Saved CC C200.")).toBeInTheDocument();
    const [put] = puts();
    expect(put.url.pathname).toBe(`${BASE}/dimension-values/CC/C200`);
    expect(put.body).toEqual({ name: "Freight", activeFrom: "2026-01-01", activeTo: null, suspended: false });
  });

  it("opens a row into the form to change it", async () => {
    valueServer();
    renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    const table = await findTable({ name: "Dimension values" });
    const row = (await within(table).findByText("Old sales")).closest("tr") as HTMLElement;
    fireEvent.click(within(row).getByRole("button", { name: "DEPT" }));
    expect(screen.getByLabelText("Value")).toHaveValue("D200");
    expect(screen.getByLabelText("Suspended")).toBeChecked();
    fireEvent.click(screen.getByLabelText("Suspended"));
    fireEvent.click(screen.getByRole("button", { name: "Save value" }));
    await waitFor(() => expect(puts()).toHaveLength(1));
    expect(puts()[0].body).toMatchObject({ name: "Old sales", suspended: false });
  });

  it("refuses an invalid value without calling the service", async () => {
    valueServer();
    renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    await screen.findByLabelText("Dimension");
    fireEvent.click(screen.getByRole("button", { name: "Save value" }));
    expect(await screen.findByText("Pick one of the ledger's dimensions.")).toBeInTheDocument();
    expect(puts()).toHaveLength(0);
  });

  it("shows the service's refusal", async () => {
    valueServer();
    const inner = server.handler;
    server.handler = (url, init) =>
      init.method === "PUT" ? json(422, { code: "LEDGER_DIMENSION_INVALID", detail: "dimension DEPT is not defined on this ledger" }) : inner(url, init);
    renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    await screen.findByLabelText("Dimension");
    fireEvent.change(screen.getByLabelText("Dimension"), { target: { value: "DEPT" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "D300" } });
    fireEvent.click(screen.getByRole("button", { name: "Save value" }));
    expect(await screen.findByText("Could not save the dimension value")).toBeInTheDocument();
    expect(screen.getByText(/LEDGER_DIMENSION_INVALID/)).toBeInTheDocument();
  });

  it("shows the empty state and the load error", async () => {
    valueServer([]);
    const first = renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    expect(await screen.findByText("No registered values")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt(`/ledger/dimension-values?ledger=${LEDGER}`);
    expect(await screen.findByText("Could not load the dimension values")).toBeInTheDocument();
  });
});
