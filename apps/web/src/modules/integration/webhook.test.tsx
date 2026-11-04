import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findAlert, findTable } from "../../test-utils";
import type { Subscription } from "./api/hubClient";
import integration from "./module";
import { EMPTY_WEBHOOK, parsePatterns, patternProblem, validateWebhook } from "./NewWebhookSubscriptionPage";

const SUB_ID = "0195f0a0-0000-7000-8000-0000000000ab";
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
const body = (call: { init: RequestInit }) => JSON.parse(call.init.body as string) as Record<string, unknown>;

const webhook = (over: Partial<Subscription> = {}): Subscription => ({
  id: SUB_ID,
  kind: "webhook",
  rest: null,
  name: "Order hook",
  url: "https://hooks.example.com/passage",
  patterns: ["dev.passage.sales.order.confirmed.v1"],
  filter: null,
  status: "active",
  pausedReason: null,
  version: 1,
  secretRotatedAt: null,
  previousSecretExpiresAt: null,
  createdAt: "2026-11-03T08:00:00Z",
  updatedAt: "2026-11-03T08:00:00Z",
  ...over,
});

describe("webhook pages wiring", () => {
  it("routes the create form behind the write duty", () => {
    const create = integration.routes.find((r) => r.path === "/integration/sync/new-webhook");
    expect((create?.handle as { duty?: string }).duty).toBe("hub.subscription.write");
  });
});

describe("new webhook validation", () => {
  const good = { ...EMPTY_WEBHOOK, name: "Order hook", url: "https://hooks.example.com/passage" };

  it("mirrors the hub's pattern rule", () => {
    expect(patternProblem("dev.passage.sales.**")).toBeNull();
    expect(patternProblem("dev.passage.*.order.confirmed.v1")).toBeNull();
    expect(patternProblem("**")).toMatch(/first/);
    expect(patternProblem("dev.**.v1")).toMatch(/last/);
    expect(patternProblem("Dev.Passage")).toMatch(/lowercase/);
    expect(parsePatterns("a.b\nc.d, a.b")).toEqual(["a.b", "c.d"]);
  });

  it("builds the request the hub takes and leaves the optional fields out", () => {
    expect(validateWebhook(good).input).toEqual({ kind: "webhook", name: "Order hook", url: good.url, patterns: ["dev.passage.sales.order.confirmed.v1"] });
    expect(validateWebhook({ ...good, filter: " data.currency = 'USD' ", secret: "whsec_chosen_by_me_1" }).input).toMatchObject({ filter: "data.currency = 'USD'", secret: "whsec_chosen_by_me_1" });
  });

  it("reports each bad field", () => {
    const bad = validateWebhook({ name: " ", url: "http://hooks.example.com", patterns: "**", filter: "x".repeat(1025), secret: "short" });
    expect(Object.keys(bad.errors).sort()).toEqual(["filter", "name", "patterns", "secret", "url"]);
    expect(validateWebhook({ ...good, url: "https://user:pw@hooks.example.com/x" }).errors.url).toMatch(/user name/);
    expect(validateWebhook({ ...good, patterns: "" }).errors.patterns).toMatch(/1 to 50/);
    expect(validateWebhook({ ...good, patterns: "a.B" }).errors.patterns).toMatch(/lowercase/);
  });
});

describe("new webhook page", () => {
  it("creates the webhook with one Idempotency-Key, then shows the signing secret once", async () => {
    handler = (url, init) => (url.pathname === "/api/v1/hub/subscriptions" && init.method === "POST" ? json(201, { ...webhook({ filter: "data.currency = 'USD'" }), secret: "whsec_shown_exactly_once" }) : undefined);
    const { container } = renderAt("/integration/sync/new-webhook");
    expect(pageViolations(container)).toEqual([]);
    fill("Name", "Order hook");
    fill("Endpoint", "https://hooks.example.com/passage");
    fill("Filter", "data.currency = 'USD'");
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await screen.findByRole("heading", { name: "Webhook created" })).toBeInTheDocument();
    expect(screen.getByLabelText("Signing secret")).toHaveTextContent("whsec_shown_exactly_once");
    const post = calls.find((c) => c.method === "POST") as (typeof calls)[number];
    expect(new Headers(post.init.headers).get("idempotency-key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(body(post)).toEqual({ kind: "webhook", name: "Order hook", url: "https://hooks.example.com/passage", patterns: ["dev.passage.sales.order.confirmed.v1"], filter: "data.currency = 'USD'" });
    expect(screen.getByRole("link", { name: "Open the subscription" })).toHaveAttribute("href", `/integration/sync/${SUB_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says the secret is not shown again when a retried submit finds the webhook already created", async () => {
    handler = (url, init) => (url.pathname === "/api/v1/hub/subscriptions" && init.method === "POST" ? json(200, webhook()) : undefined);
    renderAt("/integration/sync/new-webhook");
    fill("Name", "Order hook");
    fill("Endpoint", "https://hooks.example.com/passage");
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await screen.findByRole("heading", { name: "Webhook created" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Signing secret")).toBeNull();
    expect(screen.getByText(/Rotate the secret on the subscription page/)).toBeInTheDocument();
  });

  it("shows field errors without calling the hub, and maps the hub's refusals onto the fields", async () => {
    renderAt("/integration/sync/new-webhook");
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await screen.findByText("Enter a name, up to 200 characters.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);

    fill("Name", "Order hook");
    fill("Endpoint", "https://hooks.example.com/passage");
    fill("Filter", "data.currency =");
    handler = () => json(400, { code: "HUB-0400", detail: "filter: unexpected end of expression" });
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await screen.findByText("unexpected end of expression")).toBeInTheDocument();

    fill("Filter", "");
    handler = () => json(422, { code: "HUB-0422", detail: "unsafe webhook url: host resolves to a non-public address (10.1.1.1)" });
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await screen.findByText(/non-public address/)).toBeInTheDocument();

    handler = () => json(501, { code: "HUB-0501", detail: "webhook signing secrets cannot be stored until field encryption keys are configured" });
    fireEvent.click(screen.getByRole("button", { name: "Create webhook" }));
    expect(await findAlert()).toHaveTextContent("(HUB-0501)");
  });

  it("refuses a caller without the write duty", () => {
    renderAt("/integration/sync/new-webhook", ["hub.delivery.read"]);
    expect(screen.getByText("You cannot create webhooks")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create webhook" })).toBeNull();
  });
});

describe("outbound sync list", () => {
  it("offers New webhook beside New REST push, with the filter column", async () => {
    handler = (url) => (url.pathname === "/api/v1/hub/subscriptions" ? json(200, { items: [webhook({ filter: "data.qty > 5" })], nextCursor: null }) : undefined);
    renderAt("/integration/sync");
    const table = await findTable({ name: "Outbound sync subscriptions" });
    expect(within(table).getByText("data.qty > 5")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New webhook" })).toHaveAttribute("href", "/integration/sync/new-webhook");
    expect(screen.getByRole("link", { name: "New REST push" })).toBeInTheDocument();
  });
});

describe("webhook subscription detail", () => {
  const load = (sub: Subscription, extra: Handler = () => undefined) => {
    handler = (url, init) => {
      const custom = extra(url, init);
      if (custom) return custom;
      return url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}` && (init.method ?? "GET") === "GET" ? json(200, sub) : undefined;
    };
  };

  it("shows the filter and the signing secret state, and no secret", async () => {
    load(webhook({ filter: "data.qty > 5", secretRotatedAt: "2026-11-02T10:00:00Z", previousSecretExpiresAt: "2999-01-01T00:00:00Z" }));
    const { container } = renderAt(`/integration/sync/${SUB_ID}`);
    expect(await screen.findByRole("heading", { name: "Order hook" })).toBeInTheDocument();
    expect(screen.getAllByText("data.qty > 5").length).toBeGreaterThan(0);
    expect(screen.getByText(/Still signs until/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Signing secret")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("rotates the secret after a confirmation, sending the version and the grace period, and shows the new secret once", async () => {
    const rotated = webhook({ version: 2, secretRotatedAt: "2026-11-03T09:00:00Z", previousSecretExpiresAt: "2999-01-01T00:00:00Z" });
    let done = false;
    load(webhook(), (url, init) => {
      if (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}:rotate` && init.method === "POST") {
        done = true;
        return json(200, { ...rotated, secret: "whsec_rotated_shown_once" });
      }
      return done && url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}` ? json(200, rotated) : undefined;
    });
    renderAt(`/integration/sync/${SUB_ID}`);
    await screen.findByRole("heading", { name: "Order hook" });
    fill("Keep the old secret for (hours)", "48");
    fireEvent.click(screen.getByRole("button", { name: "Rotate secret" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("The old secret keeps signing for 48 hours.");
    expect(calls.some((c) => c.url.pathname.endsWith(":rotate"))).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Rotate secret" }));
    await waitFor(() => expect(screen.getByLabelText("Signing secret")).toHaveTextContent("whsec_rotated_shown_once"));
    const rotate = calls.find((c) => c.url.pathname.endsWith(":rotate")) as (typeof calls)[number];
    expect(body(rotate)).toEqual({ version: 1, graceSeconds: 172_800 });
    // The page reloads the subscription (now version 2); the new secret stays on screen until the user leaves.
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url.pathname.endsWith(SUB_ID))).toHaveLength(2));
    expect(await screen.findByText(/Still signs until/)).toBeInTheDocument();
    expect(screen.getByLabelText("Signing secret")).toHaveTextContent("whsec_rotated_shown_once");
  });

  it("refuses a grace period out of range before asking, and shows the hub's refusal", async () => {
    load(webhook());
    renderAt(`/integration/sync/${SUB_ID}`);
    await screen.findByRole("heading", { name: "Order hook" });
    fill("Keep the old secret for (hours)", "500");
    fireEvent.click(screen.getByRole("button", { name: "Rotate secret" }));
    expect(await screen.findByText(/whole hours from 0 to 168/)).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).toBeNull();

    load(webhook(), (url, init) => (url.pathname.endsWith(":rotate") && init.method === "POST" ? json(409, { code: "HUB-0409", detail: "the subscription changed since it was read" }) : undefined));
    fill("Keep the old secret for (hours)", "0");
    fireEvent.click(screen.getByRole("button", { name: "Rotate secret" }));
    expect((await screen.findByRole("alertdialog")).textContent).toContain("stops signing at once");
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Rotate secret" }));
    expect(await findAlert()).toHaveTextContent("the subscription changed since it was read (HUB-0409)");
  });

  it("does not offer rotation on a rest-push subscription", async () => {
    load(webhook({ kind: "rest-push", rest: { outboundMappingId: SUB_ID, method: "POST", responseKeyPath: "", onClientError: "dead_letter", auth: { type: "bearer", hasCredentials: true } } }));
    renderAt(`/integration/sync/${SUB_ID}`);
    await screen.findByRole("heading", { name: "Order hook" });
    expect(screen.queryByRole("button", { name: "Rotate secret" })).toBeNull();
    expect(screen.getByRole("button", { name: "Save filter" })).toBeInTheDocument();
  });

  it("saves, shows the hub's parse error for, and removes a filter", async () => {
    let current = webhook();
    handler = (url, init) => {
      if (url.pathname !== `/api/v1/hub/subscriptions/${SUB_ID}`) return undefined;
      if (init.method === "PATCH") {
        const patch = JSON.parse(init.body as string) as { filter: string | null; version: number };
        if (patch.filter === "bad (") return json(400, { code: "HUB-0400", detail: "filter: unexpected '(' at position 5" });
        current = { ...current, filter: patch.filter, version: patch.version + 1 };
        return json(200, current);
      }
      return json(200, current);
    };
    renderAt(`/integration/sync/${SUB_ID}`);
    await screen.findByRole("heading", { name: "Order hook" });
    const save = screen.getByRole("button", { name: "Save filter" });
    expect(save).toBeDisabled();
    fill("Filter", "bad (");
    fireEvent.click(screen.getByRole("button", { name: "Save filter" }));
    expect(await screen.findByText("unexpected '(' at position 5")).toBeInTheDocument();
    fill("Filter", "data.qty > 5");
    fireEvent.click(screen.getByRole("button", { name: "Save filter" }));
    expect(await screen.findByRole("button", { name: "Remove filter" })).toBeInTheDocument();
    expect(JSON.parse(calls.filter((c) => c.method === "PATCH").at(-1)?.init.body as string)).toEqual({ version: 1, filter: "data.qty > 5" });
    fireEvent.click(screen.getByRole("button", { name: "Remove filter" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove filter" })).toBeNull());
    expect(JSON.parse(calls.filter((c) => c.method === "PATCH").at(-1)?.init.body as string)).toEqual({ version: 2, filter: null });
  });

  it("disables the actions without the write duty", async () => {
    load(webhook());
    renderAt(`/integration/sync/${SUB_ID}`, ["hub.delivery.read"]);
    await screen.findByRole("heading", { name: "Order hook" });
    expect(screen.getByRole("button", { name: "Rotate secret" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save filter" })).toBeDisabled();
  });

  it("shows the error state with a retry", async () => {
    let fail = true;
    handler = (url) => (url.pathname === `/api/v1/hub/subscriptions/${SUB_ID}` ? (fail ? json(500, { code: "HUB-0500", detail: "unexpected error" }) : json(200, webhook())) : undefined);
    renderAt(`/integration/sync/${SUB_ID}`);
    const alert = await findAlert();
    expect(alert).toHaveTextContent("unexpected error (HUB-0500)");
    fail = false;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "Order hook" })).toBeInTheDocument();
  });
});
