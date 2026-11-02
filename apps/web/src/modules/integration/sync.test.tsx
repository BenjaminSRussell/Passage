import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { Delivery, Subscription } from "./api/hubClient";
import integration from "./module";
import { EMPTY_SYNC, validateSync } from "./NewSyncSubscriptionPage";

const SUB_ID = "0195f0a0-0000-7000-8000-0000000000aa";
const MAPPING = "0195f0a0-0000-7000-8000-0000000000bb";
const WRITE = ["hub.delivery.read", "hub.subscription.write"];

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

const subscription: Subscription = {
  id: SUB_ID,
  kind: "rest-push",
  rest: { outboundMappingId: MAPPING, method: "POST", responseKeyPath: "$.data.id", onClientError: "dead_letter", auth: { type: "basic", username: "passage", hasCredentials: true } },
  name: "Legacy ERP orders",
  url: "https://erp.corp.example/api/salesorders",
  patterns: ["dev.passage.sales.order.confirmed.v1"],
  status: "active",
  pausedReason: null,
  version: 1,
  createdAt: "2026-10-26T08:00:00Z",
  updatedAt: "2026-10-26T08:00:00Z",
};
const delivery = (over: Partial<Delivery> = {}): Delivery => ({
  id: "0195f0a0-0000-7000-8000-0000000000d1",
  subscriptionId: SUB_ID,
  eventId: "evt-1",
  eventType: "dev.passage.sales.order.confirmed.v1",
  aggregateId: "order-1",
  sequence: 1,
  status: "delivered",
  attempts: 1,
  lastStatus: 201,
  nextRetryAt: null,
  replay: false,
  deadLetterCode: null,
  responseExcerpt: null,
  legacyKey: "SO-1001",
  createdAt: "2026-10-26T08:01:00Z",
  ...over,
});

describe("outbound sync module wiring", () => {
  it("routes the sync nav entries and gates them on the seeded hub duties", () => {
    const paths = integration.routes.map((r) => r.path);
    const sync = integration.nav.filter((n) => n.to.startsWith("/integration/sync"));
    expect(sync.map((n) => n.to)).toEqual(["/integration/sync", "/integration/sync/deliveries", "/integration/sync/dead-letters"]);
    for (const item of sync) {
      expect(paths).toContain(item.to);
      expect(item.duty).toBe("hub.delivery.read");
    }
    const create = integration.routes.find((r) => r.path === "/integration/sync/new");
    expect((create?.handle as { duty?: string }).duty).toBe("hub.subscription.write");
  });
});

describe("outbound sync subscriptions", () => {
  it("lists subscriptions and offers New REST push only with the write duty", async () => {
    handler = (url) => (url.pathname === "/api/v1/hub/subscriptions" ? json(200, { items: [subscription], nextCursor: null }) : undefined);
    const { container, unmount } = renderAt("/integration/sync");
    const table = await findTable({ name: "Outbound sync subscriptions" });
    expect(within(table).getByText("Legacy ERP orders")).toBeInTheDocument();
    expect(within(table).getByText("REST push")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New REST push" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    unmount();
    renderAt("/integration/sync", ["hub.delivery.read"]);
    await findTable({ name: "Outbound sync subscriptions" });
    expect(screen.queryByRole("link", { name: "New REST push" })).toBeNull();
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = () => json(200, { items: [], nextCursor: null });
    const first = renderAt("/integration/sync");
    expect(await screen.findByText("No outbound sync yet")).toBeInTheDocument();
    first.unmount();
    let fail = true;
    handler = () => (fail ? json(500, { code: "HUB-0500", detail: "unexpected error" }) : json(200, { items: [subscription], nextCursor: null }));
    renderAt("/integration/sync");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("Could not load subscriptions");
    expect(alert).toHaveTextContent("unexpected error (HUB-0500)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Legacy ERP orders")).toBeInTheDocument();
  });
});

describe("new REST push", () => {
  const good = { ...EMPTY_SYNC, name: "ERP", outboundMappingId: MAPPING, url: "https://erp.corp.example/api/customers/{CustAccount}/orders", username: "svc", password: "pw-123456" };

  it("validates like the hub does", () => {
    expect(validateSync(good).input).toEqual({
      kind: "rest-push", name: "ERP", url: good.url, patterns: ["dev.passage.sales.order.confirmed.v1"], outboundMappingId: MAPPING,
      method: "POST", onClientError: "dead_letter", auth: { type: "basic", username: "svc", password: "pw-123456" },
    });
    expect(validateSync({ ...good, responseKeyPath: "$.data.id", patterns: "a.b\nc.d, a.b" }).input).toMatchObject({ responseKeyPath: "$.data.id", patterns: ["a.b", "c.d"] });
    const bad = validateSync({ ...EMPTY_SYNC, patterns: " ", url: "https://{host}.example/x", responseKeyPath: "$..x" });
    expect(Object.keys(bad.errors).sort()).toEqual(["name", "outboundMappingId", "password", "patterns", "responseKeyPath", "url", "username"]);
    expect(validateSync({ ...good, url: "ftp://erp" }).errors.url).toBeDefined();
    expect(validateSync({ ...good, authType: "api-key", headerName: "", value: "" }).errors).toMatchObject({ headerName: expect.any(String), value: expect.any(String) });
    expect(validateSync({ ...good, authType: "oauth2-client-credentials" }).errors).toMatchObject({ tokenUrl: expect.any(String), clientId: expect.any(String), clientSecret: expect.any(String) });
    expect(validateSync({ ...good, authType: "bearer", token: "t" }).input?.auth).toEqual({ type: "bearer", token: "t" });
  });

  it("creates the subscription with one Idempotency-Key per attempt, sends the credentials once and opens it", async () => {
    handler = (url, init) => {
      if (url.pathname === "/api/v1/hub/subscriptions" && init.method === "POST") return json(201, subscription);
      if (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}`) return json(200, subscription);
      return undefined;
    };
    const { container } = renderAt("/integration/sync/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Name", "Legacy ERP orders");
    fill("Outbound mapping id", MAPPING);
    fill("Endpoint", "https://erp.corp.example/api/salesorders");
    fill("User name", "passage");
    fill("Password", "pw-123456");
    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
    fireEvent.click(screen.getByRole("button", { name: "Create subscription" }));
    expect(await screen.findByRole("heading", { name: "Legacy ERP orders" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(header(post, "idempotency-key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(JSON.parse(post.init.body as string)).toMatchObject({ kind: "rest-push", outboundMappingId: MAPPING, auth: { type: "basic", username: "passage", password: "pw-123456" } });
    // The detail page shows that credentials exist, never what they are.
    expect(screen.getByText("Credentials stored")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("pw-123456");
  });

  it("shows field errors without calling the hub, maps HUB-0422 to the endpoint and other failures to an alert", async () => {
    renderAt("/integration/sync/new");
    fireEvent.click(screen.getByRole("button", { name: "Create subscription" }));
    expect(await screen.findByText("Enter a name, up to 200 characters.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    handler = () => json(422, { code: "HUB-0422", detail: "unsafe webhook url: host resolves to a non-public address (10.1.1.1)" });
    fill("Name", "ERP");
    fill("Outbound mapping id", MAPPING);
    fill("Endpoint", "https://erp.corp.example/api");
    fill("User name", "u");
    fill("Password", "p");
    fireEvent.click(screen.getByRole("button", { name: "Create subscription" }));
    expect(await screen.findByText(/non-public address/)).toBeInTheDocument();

    handler = () => json(501, { code: "HUB-0502", detail: "rest-push is not configured on this hub" });
    fireEvent.click(screen.getByRole("button", { name: "Create subscription" }));
    expect(await findAlert()).toHaveTextContent("rest-push is not configured on this hub (HUB-0502)");
  });

  it("switches the credential fields with the authentication type", () => {
    renderAt("/integration/sync/new");
    fireEvent.change(screen.getByLabelText("Authentication"), { target: { value: "oauth2-client-credentials" } });
    expect(screen.getByLabelText("Token endpoint")).toBeInTheDocument();
    expect(screen.getByLabelText("Client secret")).toHaveAttribute("type", "password");
    expect(screen.queryByLabelText("Password")).toBeNull();
  });

  it("tells a user without the write duty why they cannot create", () => {
    renderAt("/integration/sync/new", ["hub.delivery.read"]);
    expect(screen.getByText("You cannot create outbound sync")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create subscription" })).toBeNull();
  });
});

describe("subscription detail and test send", () => {
  beforeEach(() => {
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}:test`) {
        const send = JSON.parse(init.body as string).send;
        return json(200, send ? { stage: "sent", method: "POST", url: "https://erp.corp.example/api/salesorders", status: 201, legacyKey: "SO-1001" } : { stage: "built", method: "POST", url: "https://erp.corp.example/api/salesorders", body: { CustAccount: "C-1" } });
      }
      if (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}`) return json(200, subscription);
      return undefined;
    };
  });

  it("shows the settings and renders a sample event, then sends it when asked", async () => {
    const { container } = renderAt(`/integration/sync/${SUB_ID}`);
    expect(await screen.findByRole("heading", { name: "Legacy ERP orders" })).toBeInTheDocument();
    expect(screen.getByText("$.data.id")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Render" }));
    expect(await findStatus()).toHaveTextContent("Rendered. Nothing was sent.");
    expect(JSON.parse(calls.at(-1)?.init.body as string)).toMatchObject({ send: false, event: { type: "dev.passage.sales.order.confirmed.v1" } });

    fireEvent.click(screen.getByLabelText("Also send it to the ERP"));
    fireEvent.click(screen.getByRole("button", { name: "Render and send" }));
    await waitFor(() => expect(screen.getByLabelText("Test send result")).toHaveTextContent("SO-1001"));
    expect(JSON.parse(calls.at(-1)?.init.body as string).send).toBe(true);
  });

  it("refuses a sample that is not JSON without calling the hub", async () => {
    renderAt(`/integration/sync/${SUB_ID}`);
    await screen.findByRole("heading", { name: "Legacy ERP orders" });
    fill("Sample event", "{nope");
    fireEvent.click(screen.getByRole("button", { name: "Render" }));
    expect(await screen.findByText("The sample event is not valid JSON.")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname.endsWith(":test"))).toHaveLength(0);
  });

  it("offers Resume on a paused subscription to a user who may write, and not to one who may only read", async () => {
    const paused = { ...subscription, status: "paused" as const, pausedReason: "HUB-0114" };
    let resumed = false;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}:resume` && init.method === "POST") {
        resumed = true;
        return json(200, subscription);
      }
      return url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}` ? json(200, resumed ? subscription : paused) : undefined;
    };
    const { unmount } = renderAt(`/integration/sync/${SUB_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Resume" })).toBeNull());
    expect(resumed).toBe(true);
    unmount();
    resumed = false;
    renderAt(`/integration/sync/${SUB_ID}`, ["hub.delivery.read"]);
    await screen.findByText(/Paused: the receiver answered 410 Gone/);
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("shows the problem when the subscription is not found", async () => {
    handler = () => json(404, { code: "HUB-0404", detail: "no such subscription" });
    renderAt(`/integration/sync/${SUB_ID}`);
    expect(await findAlert()).toHaveTextContent("no such subscription (HUB-0404)");
  });
});

describe("delivery monitor", () => {
  it("lists deliveries with status and the ERP key, filters by status and loads more", async () => {
    handler = (url) => {
      if (url.pathname !== "/api/v1/hub/deliveries") return undefined;
      if (url.searchParams.get("cursor") === "c2") return json(200, { items: [delivery({ id: "0195f0a0-0000-7000-8000-0000000000d3", aggregateId: "order-3", legacyKey: "SO-3" })], nextCursor: null });
      return json(200, { items: [delivery(), delivery({ id: "0195f0a0-0000-7000-8000-0000000000d2", aggregateId: "order-2", status: "pending", attempts: 2, lastStatus: 503, legacyKey: null, nextRetryAt: 1_800_000_010 })], nextCursor: "c2" });
    };
    const { container } = renderAt("/integration/sync/deliveries");
    const table = await findTable({ name: "Sync deliveries" });
    await waitFor(() => expect(within(table).getByText("SO-1001")).toBeInTheDocument());
    expect(within(table).getByText("Delivered")).toBeInTheDocument();
    expect(within(table).getByText("503")).toBeInTheDocument();
    expect(within(table).getByText("2027-01-15 08:00 UTC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(screen.getByText("SO-3")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "dead_letter" } });
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("dead_letter"));
  });

  it("scopes to one subscription from the query string and ignores a malformed id", async () => {
    handler = () => json(200, { items: [], nextCursor: null });
    const { unmount } = renderAt(`/integration/sync/deliveries?subscriptionId=${SUB_ID}`);
    expect(await screen.findByText("No deliveries yet")).toBeInTheDocument();
    expect(calls[0]?.url.searchParams.get("subscriptionId")).toBe(SUB_ID);
    unmount();
    calls = [];
    renderAt("/integration/sync/deliveries?subscriptionId=nope");
    await screen.findByText("No deliveries yet");
    expect(calls[0]?.url.searchParams.has("subscriptionId")).toBe(false);
  });

  it("shows the error state with a retry", async () => {
    let fail = true;
    handler = () => (fail ? json(403, { code: "HUB-0403", detail: "you do not hold duty hub.delivery.read" }) : json(200, { items: [delivery()], nextCursor: null }));
    renderAt("/integration/sync/deliveries");
    const alert = await findAlert();
    expect(alert).toHaveTextContent("you do not hold duty hub.delivery.read (HUB-0403)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("SO-1001")).toBeInTheDocument();
  });
});

describe("dead letters", () => {
  const dead = delivery({ status: "dead_letter", lastStatus: 422, legacyKey: null, deadLetterCode: "HUB-0117", responseExcerpt: "render 422 MIG-XREF-UNRESOLVED: customer has no legacy key" });

  it("shows the reason and the ERP's answer and replays one delivery", async () => {
    let replayed = false;
    handler = (url, init) => {
      if (url.pathname === `/api/v1/hub/deliveries/${dead.id}:replay` && init.method === "POST") {
        replayed = true;
        return json(202, { status: "pending" });
      }
      return url.pathname === "/api/v1/hub/dead-letters" ? json(200, { items: replayed ? [] : [dead], nextCursor: null }) : undefined;
    };
    const { container } = renderAt("/integration/sync/dead-letters");
    const table = await findTable({ name: "Sync dead letters" });
    await waitFor(() => expect(within(table).getByText(/MIG-XREF-UNRESOLVED/)).toBeInTheDocument());
    expect(within(table).getByText("Refused (4xx or unresolved reference) (HUB-0117)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: `Replay ${dead.eventType} ${dead.aggregateId}` }));
    expect(await findStatus()).toHaveTextContent("will be sent on the next poll");
    expect(await screen.findByText("No dead letters")).toBeInTheDocument();
  });

  it("disables Replay without the write duty and shows a replay failure", async () => {
    handler = (url) => (url.pathname === "/api/v1/hub/dead-letters" ? json(200, { items: [dead], nextCursor: null }) : undefined);
    const { unmount } = renderAt("/integration/sync/dead-letters", ["hub.delivery.read"]);
    expect(await screen.findByRole("button", { name: /^Replay / })).toBeDisabled();
    unmount();
    handler = (url, init) =>
      init.method === "POST" ? json(409, { code: "HUB-0409", detail: "only a dead-lettered delivery can be replayed" }) : url.pathname === "/api/v1/hub/dead-letters" ? json(200, { items: [dead], nextCursor: null }) : undefined;
    renderAt("/integration/sync/dead-letters");
    fireEvent.click(await screen.findByRole("button", { name: /^Replay / }));
    expect(await findAlert()).toHaveTextContent("only a dead-lettered delivery can be replayed (HUB-0409)");
  });

  it("offers no Replay for a pushed record whose legacy key conflicts (HUB-0118)", async () => {
    const pushed = delivery({ id: "0195f0a0-0000-7000-8000-0000000000d2", eventId: "evt-2", aggregateId: "order-2", status: "dead_letter", lastStatus: 409, legacyKey: "SO-2", deadLetterCode: "HUB-0118", responseExcerpt: "pushed (receiver 201); not resent. xref:record 409 MIG-XREF-EVENT-CONFLICT: event already recorded" });
    let posted = false;
    handler = (url, init) => {
      if (init.method === "POST") posted = true;
      return url.pathname === "/api/v1/hub/dead-letters" ? json(200, { items: [pushed, dead], nextCursor: null }) : undefined;
    };
    const { container } = renderAt("/integration/sync/dead-letters");
    const table = await findTable({ name: "Sync dead letters" });
    await waitFor(() => expect(within(table).getByText("Pushed, but the event already has another legacy key (HUB-0118)")).toBeInTheDocument());
    expect(within(table).getByText("Reconcile in the ERP (legacy key SO-2)")).toBeInTheDocument();
    // Only the HUB-0117 row can be replayed.
    expect(within(table).getAllByRole("button", { name: /^Replay / })).toHaveLength(1);
    expect(pageViolations(container)).toEqual([]);
    expect(posted).toBe(false);
  });

  it("shows the empty state", async () => {
    handler = () => json(200, { items: [], nextCursor: null });
    renderAt("/integration/sync/dead-letters");
    expect(await screen.findByText("No dead letters")).toBeInTheDocument();
  });
});
