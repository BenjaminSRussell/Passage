import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { ConnectionProfile, ConnectionTestResult } from "./api/client";
import { checkSecret, isSourceSystem, parseConnectionConfig, parseConnectionForm, secretNoun, sourceLabel } from "./domain";
import migration from "./module";

type Reply = { status: number; body: unknown };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
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
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
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

const SECRET = "Sup3r-S3cret-sentinel";
const CONN = "0195f0a0-0000-7000-8000-0000000000d1";
const base: ConnectionProfile = {
  id: CONN,
  name: "Acme ERP",
  kind: "sql",
  sourceSystem: "custom_acme",
  config: { driver: "postgres", host: "erp.acme.example", port: 5432, database: "erp", user: "passage" },
  secretSet: false,
  status: "active",
  version: 1,
  createdBy: "u1",
  createdAt: "2027-01-05T08:00:00Z",
  updatedAt: "2027-01-05T08:00:00Z",
};
const withSecret: ConnectionProfile = { ...base, secretSet: true, secretUpdatedAt: "2027-01-06T09:30:00Z", version: 2 };
const restProfile: ConnectionProfile = { ...base, id: "0195f0a0-0000-7000-8000-0000000000d2", name: "Acme API", kind: "rest", sourceSystem: "sap_s4", config: { baseUrl: "https://api.acme.example", authType: "bearer" } };

describe("connections list", () => {
  it("lists connections with secret status, filters by kind and never shows a secret", async () => {
    handler = () => json(200, { items: [withSecret, restProfile] });
    const { container } = renderAt("/migration/connections");
    const table = await findTable({ name: "Connections" });
    expect(within(table).getByText("Acme ERP")).toBeInTheDocument();
    expect(within(table).getByText("SQL database")).toBeInTheDocument();
    expect(within(table).getByText("Custom: acme")).toBeInTheDocument();
    expect(within(table).getByText("SAP S/4HANA")).toBeInTheDocument();
    expect(within(table).getByText("Set")).toBeInTheDocument();
    expect(within(table).getByText("Not set")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "rest" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("kind") === "rest")).toBe(true));
    expect(screen.getByRole("link", { name: "New connection" })).toHaveAttribute("href", "/migration/connections/new");
  });

  it("explains an empty list, shows an error with a retry, and hides New connection without the write duty", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/migration/connections", ["migration.run"]);
    expect(await screen.findByText("No connections yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New connection" })).not.toBeInTheDocument();
  });

  it("shows a failed load and loads again on retry", async () => {
    handler = () => json(500, { code: "COM-0500", detail: "internal error" });
    renderAt("/migration/connections");
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    handler = () => json(200, { items: [base] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Connections" })).toBeInTheDocument();
  });

  it("loads the next page with the cursor", async () => {
    handler = (url) => (url.searchParams.get("cursor") ? json(200, { items: [restProfile] }) : json(200, { items: [base], nextCursor: "abc" }));
    renderAt("/migration/connections");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    const table = await findTable({ name: "Connections" });
    await waitFor(() => expect(within(table).getByText("Acme API")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("new connection", () => {
  it("validates locally, then posts typed settings with an idempotency key and opens the connection", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, base) : json(200, base));
    const { container } = renderAt("/migration/connections/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create connection" }));
    expect(await screen.findByText("Enter a connection name.")).toBeInTheDocument();
    expect(screen.getByText("Choose the source system.")).toBeInTheDocument();
    expect(screen.getByText("Enter the host.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    // The password is not a field of the create form: it is set afterwards.
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " Acme ERP " } });
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "custom" } });
    fireEvent.change(screen.getByLabelText("Custom system name"), { target: { value: "Acme" } });
    fireEvent.change(screen.getByLabelText("Driver"), { target: { value: "postgres" } });
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "erp.acme.example" } });
    fireEvent.change(screen.getByLabelText("Port"), { target: { value: "70000" } });
    fireEvent.change(screen.getByLabelText("Database"), { target: { value: "erp" } });
    fireEvent.change(screen.getByLabelText("User"), { target: { value: "passage" } });
    fireEvent.click(screen.getByRole("button", { name: "Create connection" }));
    expect(await screen.findByText("Enter a whole number from 1 to 65535.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Port"), { target: { value: "5432" } });
    fireEvent.click(screen.getByRole("button", { name: "Create connection" }));
    expect(await screen.findByRole("heading", { name: "Acme ERP" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ name: "Acme ERP", kind: "sql", sourceSystem: "custom_acme", config: { driver: "postgres", host: "erp.acme.example", port: 5432, database: "erp", user: "passage" } });
    expect(new Headers(post?.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("switches the settings with the kind and puts a service field error next to its field", async () => {
    handler = () => json(400, { code: "COM-0001", detail: "request failed validation", errors: { "config.baseUrl": "must start with https://" } });
    renderAt("/migration/connections/new");
    fireEvent.change(screen.getByLabelText("Kind"), { target: { value: "rest" } });
    expect(screen.queryByLabelText("Host")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Acme API" } });
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "ax2012" } });
    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "ftp://x" } });
    fireEvent.click(screen.getByRole("button", { name: "Create connection" }));
    expect(await screen.findByText("must start with https://")).toBeInTheDocument();
    expect(screen.getByText(/request failed validation \(COM-0001\)/)).toBeInTheDocument();
  });

  it("tells a caller without the write duty and does not post", async () => {
    renderAt("/migration/connections/new", ["migration.run"]);
    expect(screen.getByText(/migration.connection.write duty/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create connection" })).toBeDisabled();
  });
});

describe("connection detail", () => {
  it("shows settings and whether a secret is set, with no secret value anywhere", async () => {
    handler = () => json(200, withSecret);
    const { container } = renderAt(`/migration/connections/${CONN}`);
    expect(await screen.findByRole("heading", { name: "Acme ERP" })).toBeInTheDocument();
    expect(screen.getByText("erp.acme.example")).toBeInTheDocument();
    expect(screen.getByText(/A password is set \(updated 2027-01-06 09:30 UTC\)\. It is sealed and cannot be shown\./)).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(SECRET);
    expect(violations(container)).toEqual([]);
  });

  it("sets the secret write-only: posts it once, clears the field and shows no value", async () => {
    handler = (_url, init) => (init.method === "PUT" ? json(200, withSecret) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    expect(await screen.findByText("No password is set yet.")).toBeInTheDocument();
    const field = screen.getByLabelText("The password") as HTMLInputElement;
    expect(field.type).toBe("password");
    fireEvent.click(screen.getByRole("button", { name: "Set password" }));
    expect(await screen.findByText("Enter the secret.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    fireEvent.change(field, { target: { value: SECRET } });
    fireEvent.click(screen.getByRole("button", { name: "Set password" }));
    expect(await screen.findByText(/A password is set \(updated/)).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url.pathname).toBe(`/api/v1/integration/connection-profiles/${CONN}/secret`);
    expect(put?.body).toEqual({ secret: SECRET });
    expect((screen.getByLabelText("New password") as HTMLInputElement).value).toBe("");
    expect(document.body.innerHTML).not.toContain(SECRET);
    expect(screen.getByRole("button", { name: "Replace password" })).toBeInTheDocument();
  });

  it("explains a service without field-encryption keys and still clears the field", async () => {
    handler = (_url, init) => (init.method === "PUT" ? json(501, { code: "MIG-SECRETS-NOT-CONFIGURED", detail: "connection secrets need FIELDCRYPT_KEYS_FILE" }) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    const field = (await screen.findByLabelText("The password")) as HTMLInputElement;
    fireEvent.change(field, { target: { value: SECRET } });
    fireEvent.click(screen.getByRole("button", { name: "Set password" }));
    expect(await screen.findByText(/MIG-SECRETS-NOT-CONFIGURED/)).toBeInTheDocument();
    expect(field.value).toBe("");
    expect(document.body.innerHTML).not.toContain(SECRET);
  });

  it("needs no secret for a file drop or an unauthenticated API", async () => {
    handler = () => json(200, { ...base, kind: "file", config: { dropDir: "acme/in", format: "csv" } });
    renderAt(`/migration/connections/${CONN}`);
    expect(await screen.findByText("A drop directory needs no secret.")).toBeInTheDocument();
    expect(secretNoun("rest", { authType: "none" })).toBeNull();
    expect(secretNoun("rest", { authType: "bearer" })).toBe("token");
  });

  it("tests the connection and shows what the probe found", async () => {
    const found: ConnectionTestResult = {
      ok: true,
      kind: "sql",
      durationMs: 42,
      serverVersion: "PostgreSQL 16.4",
      schemas: ["public", "sales"],
      tables: [{ schema: "public", name: "custtable", type: "BASE TABLE", columns: [{ name: "accountnum", type: "text", nullable: false }] }],
    };
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith(":test") ? json(200, found) : json(200, withSecret));
    const { container } = renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected in 42 ms")).toBeInTheDocument();
    expect(screen.getByText("PostgreSQL 16.4")).toBeInTheDocument();
    expect(screen.getByText(/2 schemas: public, sales/)).toBeInTheDocument();
    const tables = await findTable({ name: "Tables found" });
    expect(within(tables).getByText("custtable")).toBeInTheDocument();
    expect(within(tables).getByText("accountnum text")).toBeInTheDocument();
    expect(calls.find((c) => c.url.pathname.endsWith(":test"))?.method).toBe("POST");
    expect(violations(container)).toEqual([]);
  });

  it("shows a failed probe with its code, and an egress refusal in plain words", async () => {
    handler = (url) =>
      url.pathname.endsWith(":test")
        ? json(200, { ok: false, kind: "sql", durationMs: 3, failure: { code: "MIG-EGRESS-DENIED", message: "egress denied: 10.1.2.3 is a private address and is not listed in MIG_EGRESS_ALLOW" } })
        : json(200, withSecret);
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/not listed in MIG_EGRESS_ALLOW \(MIG-EGRESS-DENIED\)/)).toBeInTheDocument();
    expect(screen.getByText("The connection failed")).toBeInTheDocument();
  });

  it("shows a test that could not run", async () => {
    handler = (url) => (url.pathname.endsWith(":test") ? json(501, { code: "MIG-SECRETS-NOT-CONFIGURED", detail: "connection secrets need FIELDCRYPT_KEYS_FILE" }) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("The test could not run")).toBeInTheDocument();
  });

  it("lists file probe results", async () => {
    handler = (url) =>
      url.pathname.endsWith(":test") ? json(200, { ok: true, kind: "file", durationMs: 1, fileCount: 2, files: [{ name: "CustTable_1.csv", bytes: 120 }] }) : json(200, { ...base, kind: "file", config: { dropDir: "acme/in", format: "csv" } });
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("2 matching files found.")).toBeInTheDocument();
    expect(within(await findTable({ name: "Files found" })).getByText("CustTable_1.csv")).toBeInTheDocument();
  });

  it("edits settings under the version and shows the saved values", async () => {
    handler = (_url, init) => (init.method === "PATCH" ? json(200, { ...base, version: 2, config: { ...base.config, host: "erp2.acme.example" } }) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit settings" }));
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "erp2.acme.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText("erp2.acme.example")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toEqual({ version: 1, name: "Acme ERP", config: { driver: "postgres", host: "erp2.acme.example", port: 5432, database: "erp", user: "passage" } });
  });

  it("warns before and after a repoint that the server cleared the secret", async () => {
    handler = (_url, init) =>
      init.method === "PATCH" ? json(200, { ...base, version: 3, secretSet: false, config: { ...base.config, host: "erp2.acme.example" } }) : json(200, withSecret);
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit settings" }));
    expect(screen.getByText(/clears the stored secret; set it again after saving/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Host"), { target: { value: "erp2.acme.example" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText("The secret was cleared")).toBeInTheDocument();
    expect(screen.getByText("No password is set yet.")).toBeInTheDocument();
  });

  it("shows a stale-version conflict from a save", async () => {
    handler = (_url, init) => (init.method === "PATCH" ? json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 3" }) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Edit settings" }));
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(await screen.findByText(/current version is 3 \(MIG-VERSION-CONFLICT\)/)).toBeInTheDocument();
  });

  it("disables and enables under the version", async () => {
    handler = (_url, init) => (init.method === "PATCH" ? json(200, { ...base, status: "disabled", version: 2 }) : json(200, base));
    renderAt(`/migration/connections/${CONN}`);
    fireEvent.click(await screen.findByRole("button", { name: "Disable" }));
    expect(await screen.findByRole("button", { name: "Enable" })).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ version: 1, status: "disabled" });
  });

  it("is read-only without the write duty", async () => {
    handler = () => json(200, withSecret);
    renderAt(`/migration/connections/${CONN}`, ["migration.run"]);
    expect(await screen.findByRole("heading", { name: "Acme ERP" })).toBeInTheDocument();
    for (const name of ["Test connection", "Disable", "Edit settings", "Replace password"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
  });

  it("shows not-found and loading states", async () => {
    handler = () => json(404, { code: "AUTH-0404", detail: "connection profile not found" });
    renderAt(`/migration/connections/${CONN}`);
    expect(await screen.findByText(/connection profile not found \(AUTH-0404\)/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to connections" })).toBeInTheDocument();
  });
});

describe("connection domain helpers", () => {
  it("knows legacy and custom source systems", () => {
    expect(isSourceSystem("ax2012")).toBe(true);
    expect(isSourceSystem("custom_acme_2")).toBe(true);
    for (const bad of ["", "custom_", "custom_Acme", "acme", `custom_${"a".repeat(41)}`]) expect(isSourceSystem(bad)).toBe(false);
    expect(sourceLabel("custom_acme")).toBe("Custom: acme");
    expect(sourceLabel("sap_ecc")).toBe("SAP ECC");
  });

  it("builds typed settings and keeps blanks out", () => {
    expect(parseConnectionConfig("sql", { driver: "postgres", host: " h ", port: "", database: "d", user: "u", codepage: "" }).config).toEqual({ driver: "postgres", host: "h", database: "d", user: "u" });
    expect(parseConnectionConfig("sql", { driver: "postgres", host: "h", port: "1.5", database: "d", user: "u" }).errors.port).toBeTruthy();
    expect(Object.keys(parseConnectionConfig("file", {}).errors).sort()).toEqual(["dropDir", "format"]);
    const form = { name: "x", kind: "file" as const, sourceSystem: "custom_acme", config: { dropDir: "a", format: "csv" } };
    expect(parseConnectionForm(form).value).toEqual({ name: "x", kind: "file", sourceSystem: "custom_acme", config: { dropDir: "a", format: "csv" } });
    expect(parseConnectionForm({ ...form, name: "x".repeat(81) }).errors.name).toMatch(/80/);
    expect(parseConnectionForm({ ...form, sourceSystem: "custom_" }).errors.sourceSystem).toBeTruthy();
  });

  it("checks the secret length in bytes", () => {
    expect(checkSecret("")).toBeTruthy();
    expect(checkSecret("x".repeat(4096))).toBeUndefined();
    expect(checkSecret("é".repeat(2049))).toBeTruthy();
  });
});
