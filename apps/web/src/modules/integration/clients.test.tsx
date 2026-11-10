import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import type { ApiClient } from "./api/hubClient";
import integration from "./module";
import { EMPTY_CLIENT, parseCount, parseScopes, validateClient } from "./NewClientPage";

const CLIENT_UUID = "0195f0a0-0000-7000-8000-0000000000a1";
const SANDBOX = "0195f0a0-0000-7000-8000-0000000000b1";
const READ = ["hub.delivery.read"];
const WRITE = ["hub.delivery.read", "hub.client.write"];

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = WRITE) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {integration.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

const client: ApiClient = {
  id: CLIENT_UUID,
  clientId: "acme-erp",
  name: "Acme ERP",
  scopes: ["sales.order.read", "inventory.onhand.read"],
  quotaPerMin: 600,
  burst: 100,
  sandboxTenantId: null,
  ownerContact: "it@acme.example",
  status: "active",
  revokedAt: null,
  version: 1,
  createdAt: "2026-11-03T08:00:00Z",
  updatedAt: "2026-11-03T08:00:00Z",
};
const usage = { minutes: [{ minute: "2026-11-03T08:05:00Z", allowed: 100, throttled: 600 }] };

describe("API client module wiring", () => {
  it("routes the clients nav entry and gates it on the seeded read duty, without a route duty that is not seeded", () => {
    const paths = integration.routes.map((r) => r.path);
    const item = integration.nav.find((n) => n.to === "/integration/clients");
    expect(item?.duty).toBe("hub.delivery.read");
    expect(paths).toEqual(expect.arrayContaining(["/integration/clients", "/integration/clients/new", "/integration/clients/:id"]));
    for (const path of ["/integration/clients", "/integration/clients/new", "/integration/clients/:id"]) {
      expect((integration.routes.find((r) => r.path === path)?.handle as { duty?: string }).duty).toBeUndefined();
    }
  });
});

describe("API clients list", () => {
  it("lists clients with scopes and quota, and offers Register only with the write duty", async () => {
    handler = (url) => (url.pathname === "/api/v1/hub/clients" ? json(200, { items: [client, { ...client, id: "x2", clientId: "old-erp", name: "Old ERP", status: "revoked" }], nextCursor: null }) : undefined);
    const { container, unmount } = renderAt("/integration/clients");
    const table = await findTable({ name: "API clients" });
    expect(within(table).getByText("Acme ERP")).toBeInTheDocument();
    expect(within(table).getAllByText("sales.order.read, inventory.onhand.read")).toHaveLength(2);
    expect(within(table).getAllByText("600 (burst 100)")).toHaveLength(2);
    expect(within(table).getByText("Revoked")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Register client" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();
    renderAt("/integration/clients", READ);
    await findTable({ name: "API clients" });
    expect(screen.queryByRole("link", { name: "Register client" })).toBeNull();
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = () => json(200, { items: [], nextCursor: null });
    const first = renderAt("/integration/clients");
    expect(await screen.findByText("No API clients yet")).toBeInTheDocument();
    first.unmount();
    let fail = true;
    handler = () => (fail ? json(500, { code: "HUB-0500", detail: "unexpected error" }) : json(200, { items: [client], nextCursor: null }));
    renderAt("/integration/clients");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load API clients");
    expect(alert).toHaveTextContent("unexpected error");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Acme ERP")).toBeInTheDocument();
  });
});

describe("register API client", () => {
  const good = { ...EMPTY_CLIENT, clientId: "acme-erp", name: "Acme ERP", scopes: "sales.order.read\ninventory.onhand.read", ownerContact: "it@acme.example" };

  it("validates like the hub does", () => {
    expect(validateClient(good).input).toEqual({
      clientId: "acme-erp", name: "Acme ERP", scopes: ["sales.order.read", "inventory.onhand.read"], quotaPerMin: 600, burst: 100, ownerContact: "it@acme.example",
    });
    expect(validateClient({ ...good, sandboxTenantId: SANDBOX.toUpperCase() }).input?.sandboxTenantId).toBe(SANDBOX);
    const bad = validateClient({ ...EMPTY_CLIENT, quotaPerMin: "5", burst: "0", sandboxTenantId: "nope" });
    expect(Object.keys(bad.errors).sort()).toEqual(["burst", "clientId", "name", "ownerContact", "quotaPerMin", "sandboxTenantId", "scopes"]);
    expect(validateClient({ ...good, scopes: "1read" }).errors.scopes).toContain("1read");
    expect(validateClient({ ...good, scopes: "receiving.vendor-invoice.post\nidentity.user.maintain" }).input?.scopes).toEqual(["receiving.vendor-invoice.post", "identity.user.maintain"]);
    expect(validateClient({ ...good, quotaPerMin: "100001" }).errors.quotaPerMin).toBeDefined();
    expect(validateClient({ ...good, burst: "1.5" }).errors.burst).toBeDefined();
    expect(validateClient({ ...good, clientId: "Acme" }).errors.clientId).toBeDefined();
    expect(parseScopes("a.b, a.b\n c.d\n\nidentity.user.maintain ")).toEqual(["a.b", "c.d", "identity.user.maintain"]);
    expect(parseCount(" 12 ", 10, 20)).toBe(12);
    expect(parseCount("", 10, 20)).toBeNull();
  });

  it("registers with one Idempotency-Key per attempt and opens the client", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hub/clients" && init.method === "POST") return json(201, client);
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}/usage`) return json(200, usage);
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}`) return json(200, client);
      return undefined;
    };
    const { container } = renderAt("/integration/clients/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Client id", "acme-erp");
    fill("Name", "Acme ERP");
    fill("Scopes", "sales.order.read\ninventory.onhand.read");
    fill("Owner e-mail", "it@acme.example");
    fireEvent.click(screen.getByRole("button", { name: "Register client" }));
    expect(await screen.findByRole("heading", { name: "Acme ERP" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(header(post, "idempotency-key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(post.init.body as string)).toEqual({
      clientId: "acme-erp", name: "Acme ERP", scopes: ["sales.order.read", "inventory.onhand.read"], quotaPerMin: 600, burst: 100, ownerContact: "it@acme.example",
    });
  });

  it("shows field errors without calling the hub, maps a taken id to the field and other failures to an alert", async () => {
    renderAt("/integration/clients/new");
    fireEvent.click(screen.getByRole("button", { name: "Register client" }));
    expect(await screen.findByText(/Use 3 to 64 characters/)).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Client id", "acme-erp");
    fill("Name", "Acme ERP");
    fill("Scopes", "sales.order.read");
    fill("Owner e-mail", "it@acme.example");
    handler = () => json(409, { code: "HUB-0409", detail: "an API client with this client id is already registered" });
    fireEvent.click(screen.getByRole("button", { name: "Register client" }));
    expect(await screen.findByText("A client with this id is already registered.")).toBeInTheDocument();

    handler = () => json(500, { code: "HUB-0500", detail: "unexpected error" });
    fireEvent.click(screen.getByRole("button", { name: "Register client" }));
    expect(await findAlert()).toHaveTextContent("unexpected error");
  });

  it("tells a user without the write duty why they cannot register", () => {
    renderAt("/integration/clients/new", READ);
    expect(screen.getByText("You cannot register API clients")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Register client" })).toBeNull();
  });
});

describe("API client detail", () => {
  const open = (c: ApiClient = client) => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}/usage`) return json(200, usage);
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}:revoke` && init.method === "POST") {
        c = { ...c, status: "revoked", version: c.version + 1, revokedAt: "2026-11-03T09:00:00Z" };
        return json(200, c);
      }
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}` && init.method === "PATCH") {
        c = { ...c, ...JSON.parse(init.body as string), version: c.version + 1 };
        return json(200, c);
      }
      if (url.pathname === `/api/v1/hub/clients/${CLIENT_UUID}`) return json(200, c);
      return undefined;
    };
  };

  it("shows the settings and the usage of the last hour", async () => {
    open();
    const { container } = renderAt(`/integration/clients/${CLIENT_UUID}`);
    expect(await screen.findByRole("heading", { name: "Acme ERP" })).toBeInTheDocument();
    expect(screen.getByText("600 requests per minute, burst 100")).toBeInTheDocument();
    const table = await findTable({ name: "Client usage" });
    expect(within(table).getByText("600")).toBeInTheDocument();
    expect(screen.getByText("100 admitted, 600 refused with 429.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("saves a new quota with the version last read and reloads", async () => {
    open();
    renderAt(`/integration/clients/${CLIENT_UUID}`);
    await screen.findByRole("heading", { name: "Acme ERP" });
    fill("Requests per minute", "1200");
    fill("Burst", "200");
    fireEvent.click(screen.getByRole("button", { name: "Save quota" }));
    expect(await screen.findByText("1200 requests per minute, burst 200")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH") as (typeof calls)[number];
    expect(JSON.parse(patch.init.body as string)).toEqual({ version: 1, quotaPerMin: 1200, burst: 200 });
  });

  it("refuses a bad quota locally and explains a version conflict", async () => {
    open();
    renderAt(`/integration/clients/${CLIENT_UUID}`);
    await screen.findByRole("heading", { name: "Acme ERP" });
    fill("Requests per minute", "5");
    fireEvent.click(screen.getByRole("button", { name: "Save quota" }));
    expect(await screen.findByText(/Enter a whole number from 10 to 100000/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(0);
    fill("Requests per minute", "700");
    handler = (url, init) => (init.method === "PATCH" ? json(409, { code: "HUB-0409", detail: "the subscription changed since it was read" }) : url.pathname.endsWith("/usage") ? json(200, usage) : json(200, client));
    fireEvent.click(screen.getByRole("button", { name: "Save quota" }));
    expect(await findAlert()).toHaveTextContent("The client changed since you opened it");
  });

  it("revokes after a confirmation and then shows the client revoked without edit controls", async () => {
    open();
    renderAt(`/integration/clients/${CLIENT_UUID}`);
    await screen.findByRole("heading", { name: "Acme ERP" });
    fireEvent.click(screen.getByRole("button", { name: "Revoke client" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep client" }));
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Revoke client" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke client" }));
    await waitFor(() => expect(screen.getByText("The hub's quota check refuses this client. Disable the Keycloak client too: the gateway does not ask the quota check yet.")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Save quota" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke client" })).toBeNull();
  });

  it("hides the quota form and revoke from a user who may only read", async () => {
    open();
    renderAt(`/integration/clients/${CLIENT_UUID}`, READ);
    await screen.findByRole("heading", { name: "Acme ERP" });
    expect(screen.queryByRole("button", { name: "Save quota" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Revoke client" })).toBeNull();
  });

  it("shows loading, then the error state when the client cannot be loaded", async () => {
    handler = () => json(404, { code: "HUB-0404", detail: "no such API client" });
    renderAt(`/integration/clients/${CLIENT_UUID}`);
    expect(await findAlert()).toHaveTextContent("no such API client");
  });

  it("shows the usage error and the empty usage separately", async () => {
    handler = (url) => (url.pathname.endsWith("/usage") ? json(500, { code: "HUB-0500", detail: "unexpected error" }) : json(200, client));
    const first = renderAt(`/integration/clients/${CLIENT_UUID}`);
    expect(await screen.findByText("Could not load usage")).toBeInTheDocument();
    first.unmount();
    handler = (url) => (url.pathname.endsWith("/usage") ? json(200, { minutes: [] }) : json(200, client));
    renderAt(`/integration/clients/${CLIENT_UUID}`);
    expect(await screen.findByText("No requests in the last hour")).toBeInTheDocument();
  });
});
