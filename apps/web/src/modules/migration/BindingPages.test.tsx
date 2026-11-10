import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { BindingPreview, ConnectionProfile, EntityBinding } from "./api/client";
import { buildSource, describeSource, parseBindingForm, sourceToText, splitList, toBase64 } from "./bindingDomain";
import { secretNoun } from "./domain";
import migration from "./module";

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

const WRITE = ["migration.run", "migration.connection.write"];

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

const PROFILE = "0195f0a0-0000-7000-8000-0000000000d1";
const BIND = "0195f0a0-0000-7000-8000-0000000000e1";
const SPEC = "0195f0a0-0000-7000-8000-0000000000f1";
const sqlProfile: ConnectionProfile = {
  id: PROFILE,
  name: "Acme ERP",
  kind: "sql",
  sourceSystem: "custom_acme",
  config: { driver: "sqlserver", host: "erp.acme.example", database: "erp", user: "passage" },
  secretSet: true,
  status: "active",
  version: 2,
  createdBy: "u1",
  createdAt: "2027-01-05T08:00:00Z",
  updatedAt: "2027-01-05T08:00:00Z",
};
const fileProfile: ConnectionProfile = { ...sqlProfile, id: "0195f0a0-0000-7000-8000-0000000000d2", name: "Acme drop", kind: "file", config: { dropDir: "acme", format: "csv" } };
const binding: EntityBinding = {
  id: BIND,
  connectionProfileId: PROFILE,
  entity: "customer",
  source: { schema: "dbo", table: "CustTable" },
  keyFields: ["ACCOUNTNUM"],
  cursorField: "MODIFIEDDATETIME",
  cursorType: "timestamp",
  status: "active",
  version: 1,
  createdBy: "u1",
  createdAt: "2027-01-06T08:00:00Z",
  updatedAt: "2027-01-06T08:00:00Z",
};
const spec = { id: SPEC, entity: "customer", version: 3, checksum: "x", fieldCount: 2, testCount: 1, createdAt: "2027-01-01T00:00:00Z" };

const previewOk: BindingPreview = {
  ok: true,
  durationMs: 12,
  mapping: { entity: "customer", version: 3, mappingSpecId: SPEC },
  columns: ["ACCOUNTNUM", "NAME"],
  more: true,
  mapped: 1,
  rejected: 1,
  duplicateKeys: [],
  rows: [
    { index: 1, sourceKey: "C1", source: { ACCOUNTNUM: "C1", NAME: "Acme" }, target: { customerNumber: "C1", name: "Acme" } },
    { index: 2, sourceKey: "C2", source: { ACCOUNTNUM: "C2", NAME: "" }, rejects: [{ field: "name", code: "MIG-ROW-REQUIRED", detail: "name is required" }] },
  ],
};

function routes(extra?: (url: URL, init: RequestInit) => Reply | undefined) {
  handler = (url, init) => {
    const p = url.pathname;
    const own = extra?.(url, init);
    if (own) return own;
    if (p.endsWith("/connection-profiles")) return json(200, { items: [sqlProfile, fileProfile] });
    if (p.endsWith(`/connection-profiles/${PROFILE}`)) return json(200, sqlProfile);
    if (p.endsWith("/mapping-specs")) return json(200, { items: [spec] });
    if (p.endsWith(`/entity-bindings/${BIND}`)) return json(200, binding);
    if (p.endsWith("/entity-bindings")) return json(200, { items: [binding] });
    return undefined;
  };
}

describe("bindings list", () => {
  it("lists bindings with connection, source and key, and filters by connection", async () => {
    routes();
    const { container } = renderAt("/migration/bindings");
    const table = await findTable({ name: "Entity bindings" });
    expect(within(table).getByText("customer")).toBeInTheDocument();
    expect(await within(table).findByText("Acme ERP")).toBeInTheDocument();
    expect(within(table).getByText("dbo.CustTable")).toBeInTheDocument();
    expect(within(table).getByText("ACCOUNTNUM")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Connection"), { target: { value: PROFILE } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("connectionProfileId") === PROFILE)).toBe(true));
    expect(screen.getByRole("link", { name: "New binding" })).toHaveAttribute("href", "/migration/bindings/new");
  });

  it("explains an empty list and hides New binding without the write duty", async () => {
    routes((url) => (url.pathname.endsWith("/entity-bindings") ? json(200, { items: [] }) : undefined));
    renderAt("/migration/bindings", ["migration.run"]);
    expect(await screen.findByText("No entity bindings yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New binding" })).not.toBeInTheDocument();
  });

  it("shows a failed load and loads again on retry", async () => {
    routes((url) => (url.pathname.endsWith("/entity-bindings") ? json(500, { code: "COM-0500", detail: "internal error" }) : undefined));
    renderAt("/migration/bindings");
    expect(await screen.findByText(/internal error/)).toBeInTheDocument();
    routes();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Entity bindings" })).toBeInTheDocument();
  });
});

describe("new binding", () => {
  it("validates locally, previews a draft without saving, then creates with an idempotency key", async () => {
    routes((url, init) => {
      if (url.pathname.endsWith("/entity-bindings:preview")) return json(200, previewOk);
      if (url.pathname.endsWith("/entity-bindings") && init.method === "POST") return json(201, binding);
      return undefined;
    });
    const { container } = renderAt("/migration/bindings/new");
    await screen.findByRole("option", { name: /Acme ERP/ });
    expect(violations(container)).toEqual([]);
    expect(screen.getByRole("button", { name: "Preview rows" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Create binding" }));
    expect(await screen.findByText("Choose the connection.")).toBeInTheDocument();
    expect(screen.getByText("Give one to five key fields, comma separated.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Connection"), { target: { value: PROFILE } });
    fireEvent.change(screen.getByLabelText("Passage entity"), { target: { value: "customer" } });
    fireEvent.change(screen.getByLabelText(/^Table or view/), { target: { value: "CustTable" } });
    fireEvent.change(screen.getByLabelText(/^Schema/), { target: { value: "dbo" } });
    fireEvent.change(screen.getByLabelText("Key fields"), { target: { value: "ACCOUNTNUM" } });
    fireEvent.change(screen.getByLabelText(/^Change cursor field/), { target: { value: "MODIFIEDDATETIME" } });
    fireEvent.click(screen.getByRole("button", { name: "Create binding" }));
    expect(await screen.findByText("Choose the cursor type.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/^Cursor type/), { target: { value: "timestamp" } });

    fireEvent.change(screen.getByLabelText("Rows"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview rows" }));
    const rows = await findTable({ name: "Sampled rows" });
    expect(within(rows).getByText("Mapped")).toBeInTheDocument();
    expect(within(rows).getByText("Rejected")).toBeInTheDocument();
    expect(within(rows).getByText(/name is required \(MIG-ROW-REQUIRED\)/)).toBeInTheDocument();
    expect(screen.getByText("Mapping for customer, version 3.")).toBeInTheDocument();
    const draft = calls.find((c) => c.url.pathname.endsWith("/entity-bindings:preview"));
    expect(draft?.body).toEqual({
      connectionProfileId: PROFILE,
      entity: "customer",
      source: { schema: "dbo", table: "CustTable" },
      keyFields: ["ACCOUNTNUM"],
      cursorField: "MODIFIEDDATETIME",
      cursorType: "timestamp",
      limit: 5,
    });
    expect(calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith("/entity-bindings"))).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Create binding" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/entity-bindings"))).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/entity-bindings"));
    expect(post?.body).toMatchObject({ connectionProfileId: PROFILE, entity: "customer", keyFields: ["ACCOUNTNUM"] });
  });

  it("shows service field errors on the form and a source failure from the preview", async () => {
    routes((url, init) => {
      if (url.pathname.endsWith("/entity-bindings:preview")) return json(200, { ok: false, durationMs: 3, failure: { code: "MIG-CONNECT-FAILED", message: "connection refused" } });
      if (url.pathname.endsWith("/entity-bindings") && init.method === "POST") return json(400, { code: "COM-0001", detail: "request failed validation", errors: { "source.query": "may not use DELETE: only a read-only SELECT is allowed" } });
      return undefined;
    });
    renderAt("/migration/bindings/new");
    await screen.findByRole("option", { name: /Acme ERP/ });
    fireEvent.change(screen.getByLabelText("Connection"), { target: { value: PROFILE } });
    fireEvent.change(screen.getByLabelText("Passage entity"), { target: { value: "customer" } });
    fireEvent.change(screen.getByLabelText(/^Read-only query/), { target: { value: "select 1" } });
    fireEvent.change(screen.getByLabelText("Key fields"), { target: { value: "id" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview rows" }));
    expect(await screen.findByText(/connection refused \(MIG-CONNECT-FAILED\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create binding" }));
    expect(await screen.findByText(/only a read-only SELECT is allowed/)).toBeInTheDocument();
    expect(screen.getByText(/Could not create the binding/)).toBeInTheDocument();
  });

  it("points to creating a connection first when there is none", async () => {
    routes((url) => (url.pathname.endsWith("/connection-profiles") ? json(200, { items: [] }) : undefined));
    renderAt("/migration/bindings/new");
    expect(await screen.findByText("No active connection yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create a connection" })).toHaveAttribute("href", "/migration/connections/new");
  });

  it("offers a file chooser for a file connection and sends the file as an upload", async () => {
    routes((url) => (url.pathname.endsWith("/entity-bindings:preview") ? json(200, { ...previewOk, mapping: undefined, mapped: 0, rejected: 0 }) : undefined));
    renderAt("/migration/bindings/new");
    await screen.findByRole("option", { name: /Acme drop/ });
    fireEvent.change(screen.getByLabelText("Connection"), { target: { value: fileProfile.id } });
    fireEvent.change(screen.getByLabelText("Passage entity"), { target: { value: "customer" } });
    fireEvent.change(screen.getByLabelText("File name"), { target: { value: "cust.csv" } });
    fireEvent.change(screen.getByLabelText("Key fields"), { target: { value: "id" } });
    const file = new File(["id,name\n1,Acme\n"], "cust.csv", { type: "text/csv" });
    Object.defineProperty(file, "arrayBuffer", { value: async () => new TextEncoder().encode("id,name\n1,Acme\n").buffer });
    fireEvent.change(screen.getByLabelText(/^Try a file/), { target: { files: [file] } });
    await waitFor(() => expect(screen.getByLabelText(/^Try a file/)).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Preview rows" }));
    await findTable({ name: "Sampled rows" });
    const sent = calls.find((c) => c.url.pathname.endsWith("/entity-bindings:preview"))?.body as { upload?: { name: string; contentBase64: string } };
    expect(sent.upload).toEqual({ name: "cust.csv", contentBase64: btoa("id,name\n1,Acme\n") });
    expect(screen.getByText(/No mapping ran/)).toBeInTheDocument();
  });
});

describe("binding detail", () => {
  it("shows the source, previews through the stored binding and reports rule errors", async () => {
    routes((url, init) => (url.pathname.endsWith(`/entity-bindings/${BIND}:preview`) && init.method === "POST" ? json(200, previewOk) : undefined));
    const { container } = renderAt(`/migration/bindings/${BIND}`);
    expect(await screen.findByRole("heading", { name: "customer from Acme ERP" })).toBeInTheDocument();
    expect(screen.getByText("dbo.CustTable")).toBeInTheDocument();
    expect(screen.getByText("MODIFIEDDATETIME (Timestamp)")).toBeInTheDocument();
    await screen.findByRole("option", { name: "customer v3" });
    fireEvent.change(screen.getByLabelText("Mapping spec"), { target: { value: SPEC } });
    fireEvent.click(screen.getByRole("button", { name: "Preview rows" }));
    const rows = await findTable({ name: "Sampled rows" });
    expect(within(rows).getByText("name is required (MIG-ROW-REQUIRED)", { exact: false })).toBeInTheDocument();
    const sent = calls.find((c) => c.url.pathname.endsWith(":preview"))?.body;
    expect(sent).toEqual({ limit: 10, mappingSpecId: SPEC });
    expect(await findTable({ name: "Source rows" })).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
  });

  it("edits the source under the version and shows a conflict", async () => {
    routes((url, init) => {
      if (url.pathname.endsWith(`/entity-bindings/${BIND}`) && init.method === "PATCH") return json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 2" });
      return undefined;
    });
    renderAt(`/migration/bindings/${BIND}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit source" }));
    fireEvent.change(screen.getByLabelText(/^Table or view/), { target: { value: "VendTable" } });
    fireEvent.click(screen.getByRole("button", { name: "Save source" }));
    expect(await screen.findByText(/current version is 2/)).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toMatchObject({ version: 1, source: { schema: "dbo", table: "VendTable" }, keyFields: ["ACCOUNTNUM"], cursorField: "MODIFIEDDATETIME", cursorType: "timestamp" });
  });

  it("disables a binding and hides the actions without the write duty", async () => {
    routes((url, init) => (url.pathname.endsWith(`/entity-bindings/${BIND}`) && init.method === "PATCH" ? json(200, { ...binding, status: "disabled", version: 2 }) : undefined));
    renderAt(`/migration/bindings/${BIND}`);
    fireEvent.click(await screen.findByRole("button", { name: "Disable" }));
    expect(await screen.findByRole("button", { name: "Enable" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview rows" })).not.toBeInTheDocument();
  });

  it("links to the binding's delta sync, where it gets its own schedule", async () => {
    routes(() => undefined);
    renderAt(`/migration/bindings/${BIND}`);
    expect(await screen.findByRole("link", { name: "Delta sync and schedule" })).toHaveAttribute("href", `/migration/sync/${BIND}`);
  });

  it("is read-only without the write duty and says so on a missing binding", async () => {
    routes();
    renderAt(`/migration/bindings/${BIND}`, ["migration.run"]);
    expect(await screen.findByRole("heading", { name: "customer from Acme ERP" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit source" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Preview rows" })).not.toBeInTheDocument();
    routes((url) => (url.pathname.includes("/entity-bindings/") ? json(404, { code: "AUTH-0404", detail: "entity binding not found" }) : undefined));
    renderAt("/migration/bindings/0195f0a0-0000-7000-8000-0000000000ff");
    expect(await screen.findByText(/entity binding not found/)).toBeInTheDocument();
  });
});

describe("binding domain", () => {
  it("builds sql, rest and file sources and refuses the obvious mistakes", () => {
    expect(buildSource("sql", { table: "T", columns: "a, b" })).toEqual({ source: { table: "T", columns: ["a", "b"] }, errors: {} });
    expect(buildSource("sql", {}).errors.table).toBeTruthy();
    expect(buildSource("sql", { table: "T", query: "select 1" }).errors.query).toBeTruthy();
    const rest = buildSource("rest", { path: "/c", pagingStyle: "cursor", cursorPath: "meta.next", pageSize: "100", params: "a=1\nb = two" });
    expect(rest.source).toEqual({ path: "/c", params: { a: "1", b: "two" }, paging: { style: "cursor", cursorPath: "meta.next", pageSize: 100 } });
    expect(buildSource("rest", { path: "c", pagingStyle: "cursor", pageSize: "5000", params: "novalue" }).errors).toMatchObject({ path: expect.any(String), cursorPath: expect.any(String), pageSize: expect.any(String), params: expect.any(String) });
    expect(buildSource("file", { file: "a.csv" }).errors).toEqual({});
    expect(buildSource("file", { file: "a.csv", filePattern: "*.csv" }).errors.file).toBeTruthy();
    expect(buildSource("file", {}).errors.file).toBeTruthy();
  });

  it("round-trips a source through its form text", () => {
    const source = { path: "/c", recordsPath: "data", params: { a: "1" }, paging: { style: "page", pageSize: 50, pageParam: "p" } };
    expect(buildSource("rest", sourceToText("rest", source)).source).toEqual(source);
    expect(sourceToText("sql", { schema: "dbo", table: "T", columns: ["a", "b"] })).toEqual({ schema: "dbo", table: "T", columns: "a, b" });
    expect(describeSource("sql", { source: { query: "select 1" } })).toBe("Query");
    expect(describeSource("rest", { source: { path: "/c" } })).toBe("/c");
    expect(describeSource("file", { source: { filePattern: "c_*.csv" } })).toBe("c_*.csv");
  });

  it("validates the whole form", () => {
    const form = { connectionProfileId: PROFILE, entity: "Customer", keyFields: "a, a", cursorField: "", cursorType: "int" as const, mappingSpecId: "", source: { table: "T" } };
    expect(Object.keys(parseBindingForm(form, "sql").errors).sort()).toEqual(["cursorField", "entity", "keyFields"]);
    const ok = parseBindingForm({ ...form, entity: "customer", keyFields: "a, b", cursorField: "u", mappingSpecId: SPEC }, "sql");
    expect(ok.value).toEqual({ connectionProfileId: PROFILE, entity: "customer", source: { table: "T" }, keyFields: ["a", "b"], cursorField: "u", cursorType: "int", mappingSpecId: SPEC });
    expect(parseBindingForm({ ...form, connectionProfileId: "" }, null).errors.connectionProfileId).toBeTruthy();
    expect(splitList(" a , ,b ")).toEqual(["a", "b"]);
    expect(toBase64(new TextEncoder().encode("héllo"))).toBe(btoa("hÃ©llo"));
  });

  it("names the OAuth2 client secret", () => {
    expect(secretNoun("rest", { authType: "oauth2_client" })).toBe("client secret");
  });
});
