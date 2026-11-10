import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { EdgeConfigVersion, EdgeNode } from "./api/client";
import shopfloor from "./module";
import { parseDocument } from "./EdgeNodeDetailPage";
import { configState } from "./EdgeNodesPage";
import { validateEdgeNode } from "./NewEdgeNodePage";

const EDGE = "0195f0a0-0000-7000-8000-000000000051";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];
const json = (status: number, body: unknown): Reply => ({ status, body });

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const ADMIN = ["shopfloor.view", "shopfloor.edge.configure"];
const VIEWER = ["shopfloor.view", "shopfloor.setup.maintain"];

function renderAt(path: string, duties: readonly string[] = ADMIN) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const posts = (suffix: string) => calls.filter((c) => c.method === "POST" && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });


const node: EdgeNode = {
  id: EDGE,
  siteId: null,
  name: "Plant 1 gateway",
  platform: "k3s",
  ring: "canary",
  connectorSubject: "edge-plant1",
  status: "online",
  connectorVersion: "1.4.2",
  certSerial: null,
  certExpiresAt: null,
  lastHeartbeatAt: "2026-11-02T08:00:00Z",
  bufferFillPct: "12.50",
  machines: [{ machineCode: "M1", state: "RUNNING" }],
  appliedConfigVersion: 1,
  latestConfigVersion: 2,
  active: true,
  version: 3,
  createdBy: "admin",
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
};
const SHA = "ab".repeat(32);
const v2: EdgeConfigVersion = { id: "c2", edgeId: EDGE, version: 2, document: { version: 2, writeRules: [{ node: "ns=2;s=SP", min: 0, max: 90 }] }, documentSha256: SHA, publishedBy: "admin", publishedAt: "2026-11-02T07:00:00Z", appliedAt: null };
const v1: EdgeConfigVersion = { ...v2, id: "c1", version: 1, document: { version: 1 }, appliedAt: "2026-11-02T07:30:00Z" };

describe("Edge fleet list", () => {
  it("lists nodes with status, ring and configuration state", async () => {
    handler = (url) => (url.pathname.endsWith("/edge-nodes") ? json(200, { items: [node, { ...node, id: "x", name: "Plant 2", status: "offline", lastHeartbeatAt: null, bufferFillPct: null, appliedConfigVersion: null, latestConfigVersion: null }] }) : undefined);
    const { container } = renderAt("/shopfloor/edge-nodes");
    const table = await findTable({ name: "Edge nodes" });
    await waitFor(() => expect(table).toHaveTextContent("Plant 1 gateway"));
    expect(table).toHaveTextContent("12.50%");
    expect(table).toHaveTextContent("Running 1, 2 published");
    expect(table).toHaveTextContent("Offline");
    expect(table).toHaveTextContent("No configuration published");
    expect(screen.getByRole("link", { name: "Plant 1 gateway" })).toHaveAttribute("href", `/shopfloor/edge-nodes/${EDGE}`);
    expect(screen.getByRole("link", { name: "Register edge node" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers registration only to holders of the edge fleet duty", async () => {
    handler = () => json(200, { items: [node] });
    renderAt("/shopfloor/edge-nodes", VIEWER);
    expect(await screen.findByText("Plant 1 gateway")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Register edge node" })).toBeNull();
  });

  it("filters by ring and status on the server", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/edge-nodes");
    expect(await screen.findByText("No edge nodes")).toBeInTheDocument();
    fill("Ring", "ring1");
    fill("Status", "offline");
    await waitFor(() => expect(calls.some((c) => c.url.searchParams.get("ring") === "ring1" && c.url.searchParams.get("status") === "offline")).toBe(true));
  });

  it("shows the error with its code and retries", async () => {
    let fail = true;
    handler = () => (fail ? json(503, { code: "COM-0503", detail: "Service unavailable" }) : json(200, { items: [node] }));
    renderAt("/shopfloor/edge-nodes");
    expect(await screen.findByText(/Service unavailable/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Plant 1 gateway")).toBeInTheDocument();
  });

  it("describes the configuration state", () => {
    expect(configState({ appliedConfigVersion: 2, latestConfigVersion: 2 })).toBe("Version 2 applied");
    expect(configState({ appliedConfigVersion: null, latestConfigVersion: 1 })).toBe("Version 1 not picked up yet");
  });
});

describe("Register edge node", () => {
  it("validates like the service", () => {
    expect(validateEdgeNode({ name: " Plant 1 ", platform: "linux", ring: "all", connectorSubject: " " }).input).toEqual({ name: "Plant 1", platform: "linux", ring: "all", connectorSubject: null });
    expect(validateEdgeNode({ name: "P", platform: "linux", ring: "all", connectorSubject: " edge-p1 " }).input?.connectorSubject).toBe("edge-p1");
    expect(Object.keys(validateEdgeNode({ name: "", platform: "x", ring: "y", connectorSubject: "service:ledger" }).errors).sort()).toEqual(["connectorSubject", "name", "platform", "ring"]);
  });

  it("registers the node with an Idempotency-Key and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/edge-nodes")) return json(201, node);
      if (url.pathname.endsWith("/config-versions")) return json(200, { items: [] });
      if (url.pathname.endsWith(`/edge-nodes/${EDGE}`)) return json(200, node);
      return undefined;
    };
    const { container } = renderAt("/shopfloor/edge-nodes/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Name", "Plant 1 gateway");
    fill("Connector identity", "edge-plant1");
    fireEvent.click(screen.getByRole("button", { name: "Register edge node" }));
    expect(await screen.findByRole("heading", { name: "Plant 1 gateway" })).toBeInTheDocument();
    const [create] = posts("/edge-nodes");
    expect(create.body).toEqual({ name: "Plant 1 gateway", platform: "k3s", ring: "all", connectorSubject: "edge-plant1" });
    expect(header(create, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("does not send an empty form, and shows a name that is taken", async () => {
    renderAt("/shopfloor/edge-nodes/new");
    fireEvent.click(screen.getByRole("button", { name: "Register edge node" }));
    expect(screen.getByText("Enter a name of up to 120 characters.")).toBeInTheDocument();
    expect(posts("/edge-nodes")).toHaveLength(0);
    handler = () => json(409, { code: "SF-0153", detail: "taken" });
    fill("Name", "Plant 1 gateway");
    fireEvent.click(screen.getByRole("button", { name: "Register edge node" }));
    expect(await screen.findByText("An edge node with this name or connector identity already exists.")).toBeInTheDocument();
  });

  it("shows no form without the edge fleet duty", () => {
    renderAt("/shopfloor/edge-nodes/new", VIEWER);
    expect(screen.getByText("You cannot register edge nodes")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Register edge node" })).toBeNull();
  });
});

describe("Edge node detail", () => {
  function serve(extra?: Handler) {
    handler = (url, init) => {
      const custom = extra?.(url, init);
      if (custom) return custom;
      if (url.pathname.endsWith("/config-versions") && !init.method) return json(200, { items: [v2, v1] });
      if (url.pathname.endsWith(`/edge-nodes/${EDGE}`) && !init.method) return json(200, node);
      return undefined;
    };
  }

  it("shows the heartbeat, machines and versions", async () => {
    serve();
    const { container } = renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    expect(await screen.findByRole("heading", { name: "Plant 1 gateway" })).toBeInTheDocument();
    expect(screen.getByText("M1: RUNNING")).toBeInTheDocument();
    expect(screen.getByText("Running 1, 2 published")).toBeInTheDocument();
    expect(screen.getByText("edge-plant1")).toBeInTheDocument();
    const table = await findTable({ name: "Configuration versions" });
    await waitFor(() => expect(table).toHaveTextContent("Not yet"));
    expect(table).toHaveTextContent(SHA.slice(0, 12));
    expect(screen.getByLabelText("Configuration version 2")).toHaveTextContent("ns=2;s=SP");
    expect(pageViolations(container)).toEqual([]);
  });

  it("is read-only without the edge fleet duty", async () => {
    serve();
    renderAt(`/shopfloor/edge-nodes/${EDGE}`, VIEWER);
    expect(await screen.findByRole("heading", { name: "Plant 1 gateway" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save node" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Publish configuration" })).toBeNull();
    expect(screen.getByText(/needs the edge fleet duty/)).toBeInTheDocument();
  });

  it("shows a load error", async () => {
    handler = () => json(404, { code: "SF-0150", detail: "edge node not found" });
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    expect(await screen.findByText(/edge node not found/)).toBeInTheDocument();
  });

  it("changes the ring and identity with the node version as If-Match, keeping the site", async () => {
    serve((_url, init) => (init.method === "PUT" ? json(200, { ...node, ring: "ring1", connectorSubject: "edge-plant9", version: 4 }) : undefined));
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    await screen.findByRole("heading", { name: "Plant 1 gateway" });
    fill("Rollout ring", "ring1");
    fill("Connector identity", " edge-plant9 ");
    fireEvent.click(screen.getByRole("button", { name: "Save node" }));
    expect(await screen.findByText("The ring, connector identity and status were saved.")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.body).toEqual({ name: "Plant 1 gateway", ring: "ring1", siteId: null, connectorSubject: "edge-plant9", active: true });
    expect(header(put, "If-Match")).toBe('"3"');
  });

  it("mints an enrollment token without a key, shows it once and refuses a switched-off node", async () => {
    serve((url, init) =>
      init.method === "POST" && url.pathname.endsWith(":enrollment-token")
        ? json(201, { edgeId: EDGE, token: "tenant.edge.secret-value", expiresAt: "2026-11-11T08:00:00Z" })
        : undefined,
    );
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    await screen.findByRole("heading", { name: "Plant 1 gateway" });
    fireEvent.click(screen.getByRole("button", { name: "Create enrollment token" }));
    expect(await screen.findByLabelText("Enrollment token")).toHaveTextContent("tenant.edge.secret-value");
    expect(screen.getByText(/it is not shown again/)).toBeInTheDocument();
    const [mint] = posts(":enrollment-token");
    expect(mint.body).toEqual({});
    expect(header(mint, "Idempotency-Key")).toBeNull();
  });

  it("does not offer a token for a switched-off node and shows the service's refusal", async () => {
    serve((url, init) => (init.method === "POST" && url.pathname.endsWith(":enrollment-token") ? json(409, { code: "SF-0152", detail: "edge node is switched off" }) : undefined));
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    await screen.findByRole("heading", { name: "Plant 1 gateway" });
    fireEvent.click(screen.getByRole("button", { name: "Create enrollment token" }));
    expect(await screen.findByText("Could not create an enrollment token")).toBeInTheDocument();
  });

  it("parses the document before sending and publishes with an Idempotency-Key", async () => {
    expect(parseDocument("[1]").error).toBeDefined();
    expect(parseDocument("{").error).toBeDefined();
    expect(parseDocument('{"a":1}').document).toEqual({ a: 1 });
    serve((_url, init) => (init.method === "POST" ? json(201, v2) : undefined));
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    await screen.findByRole("heading", { name: "Plant 1 gateway" });
    fill("Configuration document (JSON)", "{broken");
    fireEvent.click(screen.getByRole("button", { name: "Publish configuration" }));
    expect(await screen.findByText("Enter valid JSON.")).toBeInTheDocument();
    expect(posts("/config-versions")).toHaveLength(0);
    fill("Configuration document (JSON)", '{"machines":[]}');
    fireEvent.click(screen.getByRole("button", { name: "Publish configuration" }));
    expect(await screen.findByText(/Version 2 is published/)).toBeInTheDocument();
    const [pub] = posts("/config-versions");
    expect(pub.body).toEqual({ document: { machines: [] } });
    expect(header(pub, "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("shows a document the service refuses on the field", async () => {
    serve((_url, init) => (init.method === "POST" ? json(400, { code: "COM-0001", detail: "invalid", errors: { "document.tags": "tag a points at unknown machine" } }) : undefined));
    renderAt(`/shopfloor/edge-nodes/${EDGE}`);
    await screen.findByRole("heading", { name: "Plant 1 gateway" });
    fireEvent.click(screen.getByRole("button", { name: "Publish configuration" }));
    expect(await screen.findByText("tag a points at unknown machine")).toBeInTheDocument();
  });
});
