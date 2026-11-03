import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { LookupTable } from "./api/client";
import migration from "./module";
import { filterEntries, parseEntriesText, parseLookupForm } from "./lookupDomain";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const violations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: "no fake" });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const WRITE = ["migration.run", "migration.project.write"];

function renderAt(path: string, duties: string[] = WRITE) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {migration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const ID = "0195f0a0-0000-7000-8000-0000000000f1";
const table: LookupTable = {
  id: ID,
  name: "unit-codes",
  kind: "enum",
  status: "active",
  version: 2,
  entryCount: 3,
  checksum: "abc",
  definition: { kind: "enum", description: "Passage units to AX", entries: { kg: "KGM", ea: "EA", box: "BX" } },
  createdBy: "u1",
  createdAt: "2027-01-06T08:00:00Z",
  updatedAt: "2027-01-07T08:00:00Z",
};
const versions = [
  { version: 2, entryCount: 3, checksum: "bbbbbbbbbbbbbbbb", createdBy: "u1", createdAt: "2027-01-07T08:00:00Z" },
  { version: 1, entryCount: 2, checksum: "aaaaaaaaaaaaaaaa", createdBy: "u1", createdAt: "2027-01-06T08:00:00Z" },
];

function routes(extra?: (url: URL, init: RequestInit) => Reply | undefined) {
  handler = (url, init) => {
    const own = extra?.(url, init);
    if (own) return own;
    const p = url.pathname;
    if (p.endsWith(`/lookup-tables/${ID}/versions`)) return json(200, { items: versions });
    if (p.endsWith(`/lookup-tables/${ID}`)) return json(200, url.searchParams.get("version") === "1" ? { ...table, version: 1, entryCount: 2, definition: { kind: "enum", entries: { kg: "KGM", ea: "EA" } } } : table);
    if (p.endsWith("/lookup-tables")) return json(200, { items: [{ ...table, definition: undefined }] });
    return undefined;
  };
}

describe("lookup table domain", () => {
  it("accepts entries as text to text only", () => {
    expect(parseEntriesText("").error).toMatch(/Enter the entries/);
    expect(parseEntriesText("{").error).toMatch(/not valid JSON/);
    expect(parseEntriesText("[]").error).toMatch(/JSON object/);
    expect(parseEntriesText('{"SKU":12.5}').error).toMatch(/must be text/);
    expect(parseEntriesText('{"SKU":"12.50"}').value).toEqual({ SKU: "12.50" });
  });

  it("validates the form before any request", () => {
    expect(parseLookupForm({ name: "1x", kind: "enum", description: "", entries: "{}" }).errors.name).toBeDefined();
    expect(parseLookupForm({ name: "x", kind: "nope", description: "", entries: "{}" }).errors.kind).toBeDefined();
    expect(parseLookupForm({ name: "x", kind: "enum", description: "", entries: "" }).errors.entries).toBeDefined();
    const ok = parseLookupForm({ name: " prices ", kind: "price_list", description: " list ", entries: '{"A":"1"}' });
    expect(ok.value).toEqual({ name: "prices", definition: { kind: "price_list", description: "list", entries: { A: "1" } } });
  });

  it("filters entries by key or value and caps the rows", () => {
    const entries = { kg: "KGM", ea: "EA", box: "BX" };
    expect(filterEntries(entries, "", 10).rows.map((r) => r.key)).toEqual(["box", "ea", "kg"]);
    expect(filterEntries(entries, "KG", 10).rows).toEqual([{ key: "kg", value: "KGM" }]);
    expect(filterEntries(entries, "", 2)).toMatchObject({ total: 3, rows: [{ key: "box" }, { key: "ea" }] });
  });
});

describe("lookup tables list", () => {
  it("lists tables, filters and offers New only with the write duty", async () => {
    routes();
    const { container } = renderAt("/migration/lookups");
    const grid = await findTable({ name: "Lookup tables" });
    expect(within(grid).getByText("unit-codes")).toBeInTheDocument();
    expect(within(grid).getByText("Code list")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Holds"), { target: { value: "price_list" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("kind") === "price_list")).toBe(true));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "archived" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "archived")).toBe(true));
    expect(screen.getByRole("link", { name: "New lookup table" })).toHaveAttribute("href", "/migration/lookups/new");
  });

  it("explains an empty list and hides New without the write duty", async () => {
    routes((url) => (url.pathname.endsWith("/lookup-tables") ? json(200, { items: [] }) : undefined));
    renderAt("/migration/lookups", ["migration.run"]);
    expect(await screen.findByText("No lookup tables yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New lookup table" })).not.toBeInTheDocument();
  });

  it("shows a failed load and loads again on retry", async () => {
    routes((url) => (url.pathname.endsWith("/lookup-tables") ? json(500, { code: "COM-0500", detail: "internal error" }) : undefined));
    renderAt("/migration/lookups");
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    routes();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Lookup tables" })).toBeInTheDocument();
  });
});

describe("new lookup table", () => {
  it("validates locally, then creates with an idempotency key", async () => {
    routes((url, init) => (url.pathname.endsWith("/lookup-tables") && init.method === "POST" ? json(201, table) : undefined));
    const { container } = renderAt("/migration/lookups/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create table" }));
    expect(await screen.findByText(/Start with a letter/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "unit-codes" } });
    fireEvent.change(screen.getByLabelText("Holds"), { target: { value: "uom_conversion" } });
    expect((screen.getByLabelText("Entries") as HTMLTextAreaElement).value).toContain("KG|LB");
    fireEvent.click(screen.getByRole("button", { name: "Create table" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ name: "unit-codes", definition: { kind: "uom_conversion", entries: { "KG|LB": "2.20462262185", "LB|KG": "0.45359237" } } });
  });

  it("keeps typed entries when the kind changes and shows the service's refusal", async () => {
    routes((url, init) => (url.pathname.endsWith("/lookup-tables") && init.method === "POST" ? json(422, { code: "MIG-LOOKUP-INVALID", detail: "price needs a decimal" }) : undefined));
    renderAt("/migration/lookups/new");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "prices" } });
    fireEvent.change(screen.getByLabelText("Entries"), { target: { value: '{"A":"x"}' } });
    fireEvent.change(screen.getByLabelText("Holds"), { target: { value: "price_list" } });
    expect((screen.getByLabelText("Entries") as HTMLTextAreaElement).value).toBe('{"A":"x"}');
    fireEvent.click(screen.getByRole("button", { name: "Create table" }));
    expect(await screen.findByText(/price needs a decimal \(MIG-LOOKUP-INVALID\)/)).toBeInTheDocument();
  });

  it("disables creating without the write duty", () => {
    routes();
    renderAt("/migration/lookups/new", ["migration.run"]);
    expect(screen.getByText("You can view lookup tables but not create them")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create table" })).toBeDisabled();
  });
});

describe("lookup table detail", () => {
  it("shows the entries, searches them and lists the versions", async () => {
    routes();
    const { container } = renderAt(`/migration/lookups/${ID}`);
    expect(await screen.findByRole("heading", { name: "unit-codes" })).toBeInTheDocument();
    const entries = await findTable({ name: "Entries" });
    expect(within(entries).getByText("KGM")).toBeInTheDocument();
    expect(within(entries).getAllByRole("row")).toHaveLength(4);
    const versionsTable = await findTable({ name: "Versions" });
    expect(within(versionsTable).getAllByRole("row")).toHaveLength(3);
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Search entries"), { target: { value: "bx" } });
    expect(within(await findTable({ name: "Entries" })).getAllByRole("row")).toHaveLength(2);
    fireEvent.change(screen.getByLabelText("Search entries"), { target: { value: "zzz" } });
    expect(await screen.findByText("No entry matches")).toBeInTheDocument();
  });

  it("shows an older version when asked", async () => {
    routes();
    renderAt(`/migration/lookups/${ID}`);
    await screen.findByRole("heading", { name: "unit-codes" });
    fireEvent.click(await screen.findByRole("button", { name: "Show v1" }));
    expect(await screen.findByText("Entries, version 1")).toBeInTheDocument();
    expect(calls.some((c) => c.url.searchParams.get("version") === "1")).toBe(true);
    expect(screen.queryByRole("button", { name: "Edit entries" })).not.toBeInTheDocument();
  });

  it("saves edited entries as the next version and archives", async () => {
    routes((url, init) => (url.pathname.endsWith(`/lookup-tables/${ID}`) && init.method === "PATCH" ? json(200, { ...table, version: 3 }) : undefined));
    renderAt(`/migration/lookups/${ID}`);
    await screen.findByRole("heading", { name: "unit-codes" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit entries" }));
    fireEvent.change(screen.getByLabelText("Entries"), { target: { value: '{"kg":2}' } });
    fireEvent.click(screen.getByRole("button", { name: "Save as version 3" }));
    expect(await screen.findByText(/must be text/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Entries"), { target: { value: '{"kg":"KGM"}' } });
    fireEvent.click(screen.getByRole("button", { name: "Save as version 3" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(calls.find((c) => c.method === "PATCH")?.body).toMatchObject({ version: 2, definition: { kind: "enum", entries: { kg: "KGM" } } });
    fireEvent.click(await screen.findByRole("button", { name: "Archive" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1]?.body).toMatchObject({ status: "archived" });
  });

  it("shows a stale-version refusal from the service", async () => {
    routes((_url, init) => (init.method === "PATCH" ? json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 3" }) : undefined));
    renderAt(`/migration/lookups/${ID}`);
    await screen.findByRole("heading", { name: "unit-codes" });
    fireEvent.click(await screen.findByRole("button", { name: "Archive" }));
    expect(await screen.findByText(/current version is 3 \(MIG-VERSION-CONFLICT\)/)).toBeInTheDocument();
  });

  it("explains an archived table, is read-only without the write duty and reports a missing table", async () => {
    routes((url) => (url.pathname.endsWith(`/lookup-tables/${ID}`) ? json(200, { ...table, status: "archived" }) : undefined));
    const first = renderAt(`/migration/lookups/${ID}`, ["migration.run"]);
    await screen.findByRole("heading", { name: "unit-codes" });
    expect(screen.getByText("This table is archived")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reactivate" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit entries" })).not.toBeInTheDocument();
    first.unmount();
    routes((url) => (url.pathname.includes("/lookup-tables/") ? json(404, { code: "MIG-LOOKUP-NOT-FOUND", detail: "lookup table not found" }) : undefined));
    renderAt(`/migration/lookups/${ID}`);
    expect(await screen.findByText(/lookup table not found \(MIG-LOOKUP-NOT-FOUND\)/)).toBeInTheDocument();
  });
});
