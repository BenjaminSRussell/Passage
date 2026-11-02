import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { OutboundMapping, OutboundPreview } from "./api/client";
import migration from "./module";
import { parseDefinitionText, parseEventText, parseOutboundForm, STARTER_DEFINITION, toJsonText } from "./outboundDomain";

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

const ID = "0195f0a0-0000-7000-8000-0000000000e1";
const mapping: OutboundMapping = {
  id: ID,
  name: "Orders to Acme",
  sourceSystem: "custom_acme",
  eventType: "dev.passage.sales.order.*.v1",
  status: "active",
  version: 2,
  checksum: "abc",
  definition: STARTER_DEFINITION,
  createdBy: "u1",
  createdAt: "2027-01-06T08:00:00Z",
  updatedAt: "2027-01-07T08:00:00Z",
};
const versions = [
  { version: 2, checksum: "bbbbbbbbbbbbbbbb", createdBy: "u1", createdAt: "2027-01-07T08:00:00Z" },
  { version: 1, checksum: "aaaaaaaaaaaaaaaa", createdBy: "u1", createdAt: "2027-01-06T08:00:00Z" },
];
const rendered: OutboundPreview = {
  status: "ok",
  body: { CustAccount: "C-1001", Header: { CustomerRef: "SO-100", Total: "125.50" } },
  idempotencyKey: "mig-ob-v1-abc",
  refs: [{ entity: "salesOrder", id: "0195f0a0-0000-7000-8000-0000000000a1" }],
};
const unresolved: OutboundPreview = {
  status: "unresolved",
  code: "MIG-XREF-UNRESOLVED",
  detail: "customer",
  unresolved: [{ name: "customer", entity: "customer", id: "0195f0a0-0000-7000-8000-0000000000c1", reason: "no legacy key is recorded for this record yet" }],
};

function routes(extra?: (url: URL, init: RequestInit) => Reply | undefined) {
  handler = (url, init) => {
    const own = extra?.(url, init);
    if (own) return own;
    const p = url.pathname;
    if (p.endsWith(`/outbound-mappings/${ID}/versions`)) return json(200, { items: versions });
    if (p.endsWith(`/outbound-mappings/${ID}`)) return json(200, url.searchParams.get("version") === "1" ? { ...mapping, version: 1 } : mapping);
    if (p.endsWith("/outbound-mappings")) return json(200, { items: [{ ...mapping, definition: undefined }] });
    return undefined;
  };
}

describe("outbound mapping domain", () => {
  it("checks the definition and sample event before any request", () => {
    expect(parseDefinitionText("").error).toMatch(/Enter the definition/);
    expect(parseDefinitionText("[]").error).toMatch(/JSON object/);
    expect(parseDefinitionText("{").error).toMatch(/not valid JSON/);
    expect(parseDefinitionText('{"mapping":{}}').error).toMatch(/eventType/);
    expect(parseDefinitionText('{"eventType":"a.b"}').error).toMatch(/mapping/);
    expect(parseDefinitionText(toJsonText(STARTER_DEFINITION)).value?.eventType).toBe("dev.passage.sales.order.*.v1");
    expect(parseEventText('{"id":"e"}').error).toMatch(/id and type/);
    expect(parseOutboundForm({ name: "", sourceSystem: "", definition: "" }).errors).toMatchObject({ name: expect.any(String), sourceSystem: expect.any(String), definition: expect.any(String) });
    expect(parseOutboundForm({ name: " Orders ", sourceSystem: "custom_acme", definition: toJsonText(STARTER_DEFINITION) }).value).toMatchObject({ name: "Orders", sourceSystem: "custom_acme" });
  });
});

describe("outbound mappings list", () => {
  it("lists mappings, filters by status and offers New only with the write duty", async () => {
    routes();
    const { container } = renderAt("/migration/outbound");
    const table = await findTable({ name: "Outbound mappings" });
    expect(within(table).getByText("Orders to Acme")).toBeInTheDocument();
    expect(within(table).getByText("dev.passage.sales.order.*.v1")).toBeInTheDocument();
    expect(within(table).getByText("Custom: acme")).toBeInTheDocument();
    expect(violations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "archived" } });
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("status") === "archived")).toBe(true));
    expect(screen.getByRole("link", { name: "New outbound mapping" })).toHaveAttribute("href", "/migration/outbound/new");
  });

  it("explains an empty list and hides New without the write duty", async () => {
    routes((url) => (url.pathname.endsWith("/outbound-mappings") ? json(200, { items: [] }) : undefined));
    renderAt("/migration/outbound", ["migration.run"]);
    expect(await screen.findByText("No outbound mappings yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New outbound mapping" })).not.toBeInTheDocument();
  });

  it("shows a failed load and loads again on retry", async () => {
    routes((url) => (url.pathname.endsWith("/outbound-mappings") ? json(500, { code: "COM-0500", detail: "internal error" }) : undefined));
    renderAt("/migration/outbound");
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    routes();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Outbound mappings" })).toBeInTheDocument();
  });
});

describe("new outbound mapping", () => {
  it("validates locally, previews the draft without saving, then creates with an idempotency key", async () => {
    routes((url, init) => {
      if (url.pathname.endsWith("/outbound-mappings:preview")) return json(200, rendered);
      if (url.pathname.endsWith("/outbound-mappings") && init.method === "POST") return json(201, mapping);
      return undefined;
    });
    const { container } = renderAt("/migration/outbound/new");
    expect(violations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create mapping" }));
    expect(await screen.findByText("Give the mapping a name.")).toBeInTheDocument();
    expect(screen.getByText("Choose the legacy system this mapping writes to.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Orders to Acme" } });
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "custom" } });
    fireEvent.change(screen.getByLabelText("Custom system name"), { target: { value: "acme" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview body" }));
    expect(await screen.findByText("Rendered")).toBeInTheDocument();
    expect(screen.getByLabelText("Rendered request body")).toHaveTextContent("C-1001");
    expect(screen.getByText("mig-ob-v1-abc")).toBeInTheDocument();
    const draft = calls.find((c) => c.url.pathname.endsWith("/outbound-mappings:preview"));
    expect(draft?.body).toMatchObject({ sourceSystem: "custom_acme", definition: { eventType: "dev.passage.sales.order.*.v1" }, event: { id: "evt-sample-1" } });
    expect(calls.filter((c) => c.method === "POST" && !c.url.pathname.includes(":preview"))).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Create mapping" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/outbound-mappings"))).toBe(true));
    expect(calls.find((c) => c.method === "POST" && c.url.pathname.endsWith("/outbound-mappings"))?.body).toMatchObject({ name: "Orders to Acme", sourceSystem: "custom_acme" });
  });

  it("shows the service's refusal and a malformed sample event", async () => {
    routes((url, init) => (url.pathname.endsWith("/outbound-mappings") && init.method === "POST" ? json(409, { code: "MIG-OUTBOUND-NAME-EXISTS", detail: "an outbound mapping with this name already exists" }) : undefined));
    renderAt("/migration/outbound/new");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Orders" } });
    fireEvent.change(screen.getByLabelText("Source system"), { target: { value: "ax2012" } });
    fireEvent.click(screen.getByRole("button", { name: "Create mapping" }));
    expect(await screen.findByText(/already exists \(MIG-OUTBOUND-NAME-EXISTS\)/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Sample event"), { target: { value: "{nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview body" }));
    expect(await screen.findByText(/sample event is not valid JSON/)).toBeInTheDocument();
  });
});

describe("outbound mapping detail", () => {
  it("shows the definition and versions, and previews a sample with its unresolved references", async () => {
    routes((url) => (url.pathname.endsWith(`${ID}:preview`) ? json(200, unresolved) : undefined));
    const { container } = renderAt(`/migration/outbound/${ID}`);
    expect(await screen.findByRole("heading", { name: "Orders to Acme" })).toBeInTheDocument();
    expect(screen.getByLabelText("Definition JSON")).toHaveTextContent("xref.customer");
    const table = await findTable({ name: "Versions" });
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(violations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Preview body" }));
    const refs = await findTable({ name: "Unresolved references" });
    expect(within(refs).getAllByText("customer")).toHaveLength(2);
    expect(within(refs).getByText("no legacy key is recorded for this record yet")).toBeInTheDocument();
    expect(screen.getByText("Unresolved references", { selector: "span" })).toBeInTheDocument();
    const call = calls.find((c) => c.url.pathname.endsWith(`${ID}:preview`));
    expect(call?.body).toMatchObject({ event: { type: "dev.passage.sales.order.confirmed.v1" } });
    expect(call?.body).not.toHaveProperty("version");
  });

  it("previews an older version when it is shown", async () => {
    routes((url) => (url.pathname.endsWith(`${ID}:preview`) ? json(200, rendered) : undefined));
    renderAt(`/migration/outbound/${ID}`);
    await screen.findByRole("heading", { name: "Orders to Acme" });
    fireEvent.click(await screen.findByRole("button", { name: "Show v1" }));
    expect(await screen.findByText("Definition, version 1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Preview body" }));
    await screen.findByText("Rendered");
    expect(calls.find((c) => c.url.pathname.endsWith(`${ID}:preview`))?.body).toMatchObject({ version: 1 });
  });

  it("saves an edited definition as the next version and archives", async () => {
    routes((url, init) => {
      if (url.pathname.endsWith(`/outbound-mappings/${ID}`) && init.method === "PATCH") return json(200, { ...mapping, version: 3 });
      return undefined;
    });
    renderAt(`/migration/outbound/${ID}`);
    await screen.findByRole("heading", { name: "Orders to Acme" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit definition" }));
    fireEvent.change(screen.getByLabelText("Definition"), { target: { value: "{" } });
    fireEvent.click(screen.getByRole("button", { name: "Save as version 3" }));
    expect(await screen.findByText(/not valid JSON/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Definition"), { target: { value: toJsonText(STARTER_DEFINITION) } });
    fireEvent.click(screen.getByRole("button", { name: "Save as version 3" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(calls.find((c) => c.method === "PATCH")?.body).toMatchObject({ version: 2, definition: { eventType: "dev.passage.sales.order.*.v1" } });
    fireEvent.click(await screen.findByRole("button", { name: "Archive" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PATCH")[1]?.body).toMatchObject({ status: "archived" });
  });

  it("shows a stale-version refusal from the service", async () => {
    routes((_url, init) => (init.method === "PATCH" ? json(409, { code: "MIG-VERSION-CONFLICT", detail: "current version is 3" }) : undefined));
    renderAt(`/migration/outbound/${ID}`);
    await screen.findByRole("heading", { name: "Orders to Acme" });
    fireEvent.click(await screen.findByRole("button", { name: "Archive" }));
    expect(await screen.findByText(/current version is 3 \(MIG-VERSION-CONFLICT\)/)).toBeInTheDocument();
  });

  it("is read-only without the write duty and reports a missing mapping", async () => {
    routes();
    const first = renderAt(`/migration/outbound/${ID}`, ["migration.run"]);
    await screen.findByRole("heading", { name: "Orders to Acme" });
    expect(screen.queryByRole("button", { name: "Archive" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit definition" })).not.toBeInTheDocument();
    first.unmount();
    routes((url) => (url.pathname.includes("/outbound-mappings/") ? json(404, { code: "MIG-OUTBOUND-NOT-FOUND", detail: "outbound mapping not found" }) : undefined));
    renderAt(`/migration/outbound/${ID}`);
    expect(await screen.findByText(/outbound mapping not found \(MIG-OUTBOUND-NOT-FOUND\)/)).toBeInTheDocument();
  });
});
