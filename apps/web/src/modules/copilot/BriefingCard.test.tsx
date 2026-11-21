import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { AvailabilityProvider, type AiAvailability } from "../../shell/assistant/AvailabilityProvider";
import { AI_REQUEST_TIMEOUT_MS } from "../../shell/assistant/availability";
import { PrincipalProvider } from "../../shell/principal";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import { pageViolations } from "./a11yTest";
import { briefingItemRoute, briefingListSchema, type Briefing, type BriefingItem } from "./api/briefings";
import { BriefingCard } from "./BriefingCard";

useFakeServer();

const APPROVAL_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const BRIEFINGS_PATH = "/api/v1/agents/briefings";

const action: BriefingItem = {
  agentId: "cash_app",
  factId: "fact-1",
  text: "Apply the 4,200.00 USD payment from Acme",
  actionId: APPROVAL_ID,
  route: `/assistant/proposals/${APPROVAL_ID}`,
};

const briefing = (over: Partial<Briefing> = {}): Briefing => ({
  id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
  status: "READY",
  narrative: true,
  createdAt: "2026-11-19T10:00:00.000Z",
  completedAt: "2026-11-19T10:00:05.000Z",
  readAt: null,
  changes: [{ agentId: "ap_coding", factId: "fact-0", text: "3 invoices were coded" }],
  risks: [{ agentId: "anomaly", factId: "fact-2", text: "One journal looks unusual" }],
  recommendedActions: [action],
  notes: [],
  ...over,
});

const list = (...items: Briefing[]) => json(200, { items, nextCursor: null, nextScheduledAt: null });

function renderCard() {
  return render(
    <MemoryRouter>
      <BriefingCard />
    </MemoryRouter>,
  );
}

describe("BriefingCard", () => {
  it("shows the generated time, the AI badge and three sections with source-agent labels", async () => {
    server.handler = () => list(briefing());
    renderCard();
    expect(await screen.findByRole("heading", { level: 2, name: "Your briefing" })).toBeInTheDocument();
    expect(screen.getByText("AI-generated: check before you use it")).toBeInTheDocument();
    expect(document.querySelector("time")).toHaveAttribute("datetime", "2026-11-19T10:00:05.000Z");
    for (const name of ["Changes (1)", "Risks (1)", "Recommended actions (1)"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(3);
    expect(screen.getByText("Cash application agent:")).toBeInTheDocument();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0]?.url.pathname + server.calls[0]?.url.search).toBe(`${BRIEFINGS_PATH}?limit=20`);
  });

  it("opens Recommended actions at once and toggles aria-expanded on the others", async () => {
    server.handler = () => list(briefing());
    renderCard();
    const actions = await screen.findByRole("button", { name: "Recommended actions (1)" });
    const risks = screen.getByRole("button", { name: "Risks (1)" });
    expect(actions).toHaveAttribute("aria-expanded", "true");
    expect(risks).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("One journal looks unusual")).not.toBeVisible();
    await userEvent.setup().click(risks);
    expect(risks).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("One journal looks unusual")).toBeVisible();
  });

  it("links a recommended action to the proposal route and offers no control that decides or posts", async () => {
    server.handler = () => list(briefing());
    renderCard();
    const link = await screen.findByRole("link", { name: action.text });
    expect(link).toHaveAttribute("href", `/assistant/proposals/${APPROVAL_ID}`);
    expect(screen.queryByRole("button", { name: /approve|decline|post|pay/i })).toBeNull();
    expect(server.calls.every((call) => call.method === "GET")).toBe(true);
  });

  it("shows counts, links and a note without the AI badge when the briefing is deterministic", async () => {
    server.handler = () => list(briefing({ narrative: false }));
    renderCard();
    expect(await screen.findByText("Summaries are unavailable, so this briefing lists counts and links only.")).toBeInTheDocument();
    expect(screen.queryByText("AI-generated: check before you use it")).toBeNull();
    expect(screen.getByRole("button", { name: "Changes (1)" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: action.text })).toBeInTheDocument();
  });

  it("shows Nothing here for an empty section", async () => {
    server.handler = () => list(briefing({ recommendedActions: [] }));
    renderCard();
    expect(await screen.findByRole("button", { name: "Recommended actions (0)" })).toBeInTheDocument();
    expect(screen.getByText("Nothing here.")).toBeInTheDocument();
  });

  it("uses the newest READY briefing and skips RUNNING and FAILED ones", async () => {
    server.handler = () =>
      list(briefing({ status: "RUNNING", changes: [] }), briefing({ status: "FAILED", changes: [] }), briefing({ changes: [{ agentId: "ap_coding", factId: "f", text: "Ready one" }] }));
    renderCard();
    await screen.findByRole("button", { name: "Changes (1)" });
    await userEvent.setup().click(screen.getByRole("button", { name: "Changes (1)" }));
    expect(screen.getByText("Ready one")).toBeVisible();
  });

  it.each([
    ["an empty list", () => list()],
    ["only RUNNING and FAILED briefings", () => list(briefing({ status: "RUNNING" }), briefing({ status: "FAILED" }))],
  ])("offers Generate now, disabled with an explanation while AI is unavailable, for %s", async (_label, reply) => {
    server.handler = reply;
    renderCard();
    expect(await screen.findByText("No briefing yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate now" })).toBeDisabled();
    expect(screen.getByText(/needs the assistant/)).toBeInTheDocument();
    expect(server.calls.every((call) => call.method === "GET")).toBe(true);
  });

  it("shows an inline error with Retry on a server failure and recovers on the second request", async () => {
    let attempts = 0;
    server.handler = () => (++attempts === 1 ? json(500, { code: "AGT-0500" }) : list(briefing()));
    renderCard();
    expect(await screen.findByRole("alert")).toHaveTextContent("Your briefing could not be loaded");
    expect(screen.queryByRole("dialog")).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "Recommended actions (1)" })).toBeInTheDocument();
    expect(server.calls).toHaveLength(2);
  });

  it.each([
    ["a 503", json(503, { code: "AGT-0202", detail: "The assistant is switched off." })],
    ["a 503 without a problem body", json(503, {})],
    ["the kill-switch code", json(409, { code: "AGT-0201" })],
    ["the budget-stop code", json(429, { code: "AGT-0202" })],
  ])("degrades quietly, with no alert, when the AI layer answers with %s", async (_label, reply) => {
    server.handler = () => reply;
    renderCard();
    expect(await screen.findByText(/briefing is unavailable while the assistant is/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });

  it("degrades quietly when the network request fails", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    renderCard();
    expect(await screen.findByText(/briefing is unavailable while the assistant is/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("degrades quietly when the AI layer times out", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      vi.stubGlobal(
        "fetch",
        vi.fn((_url: string, init: RequestInit) => new Promise((_resolve, reject) => init.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))))),
      );
      renderCard();
      await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS);
      expect(await screen.findByText(/briefing is unavailable while the assistant is/)).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the danger alert with Try again for a validation failure", async () => {
    server.handler = () => json(422, { code: "AGT-0422" });
    renderCard();
    expect(await screen.findByRole("alert")).toHaveTextContent("Your briefing could not be loaded");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("treats a body that fails the strict schema as an error and renders nothing from it", async () => {
    server.handler = () => json(200, { items: [{ ...briefing(), extra: true }], nextCursor: null, nextScheduledAt: null });
    renderCard();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("3 invoices were coded")).toBeNull();
  });

  it("has no axe violations in the narrative, deterministic and error states", async () => {
    server.handler = () => list(briefing());
    const narrative = renderCard();
    await screen.findByRole("button", { name: "Recommended actions (1)" });
    expect(pageViolations(narrative.container)).toEqual([]);
    narrative.unmount();

    server.handler = () => list(briefing({ narrative: false }));
    const deterministic = renderCard();
    await screen.findByText(/Summaries are unavailable/);
    expect(pageViolations(deterministic.container)).toEqual([]);
    deterministic.unmount();

    server.handler = () => json(500, { code: "AGT-0500" });
    const failed = renderCard();
    await screen.findByRole("alert");
    expect(pageViolations(failed.container)).toEqual([]);
    expect(within(failed.container).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

const ENABLED: AiAvailability = { state: { status: "enabled", budget: "OK" }, reportRefusal: () => undefined, reportFailure: () => undefined };
const DISABLED: AiAvailability = { ...ENABLED, state: { status: "unavailable", reason: "off" } };
const INVOKE = ["agents.invoke", "agents.run.read"];
const GENERATE = "/api/v1/agents/briefings:generate";
const STALE = "2020-01-01T10:00:00.000Z";
let sequence = 0;

function renderAi({ duties = INVOKE, availability = ENABLED, identity = `user-${++sequence}` } = {}) {
  const ui = (
    <MemoryRouter>
      <PrincipalProvider duties={duties} identity={identity}>
        <AvailabilityProvider value={availability}>
          <BriefingCard />
        </AvailabilityProvider>
      </PrincipalProvider>
    </MemoryRouter>
  );
  return { ...render(ui), ui, identity };
}

const posts = (suffix: string) => server.calls.filter((call) => call.method === "POST" && call.url.pathname.endsWith(suffix));

describe("BriefingCard daily generation", () => {
  it("generates once on the first visit of the day, announces it politely and renders the briefing when ready", async () => {
    let reads = 0;
    server.handler = (url) => {
      if (url.pathname === GENERATE) return json(202, { id: "b-1", status: "RUNNING" });
      return ++reads === 1 ? list(briefing({ createdAt: STALE, completedAt: STALE })) : list(briefing({ changes: [{ agentId: "ap_coding", factId: "f", text: "Fresh one" }] }), briefing({ createdAt: STALE, completedAt: STALE }));
    };
    renderAi();
    expect(await screen.findByRole("status")).toHaveTextContent("Preparing your briefing");
    await waitFor(() => expect(screen.getByRole("status")).toBeEmptyDOMElement(), { timeout: 6000 });
    expect(posts(":generate")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Past briefings (1)" })).toBeInTheDocument();
  }, 10000);

  it("does not generate again on a second mount for the same user and day (second tab)", async () => {
    server.handler = (url) => (url.pathname === GENERATE ? json(409, { code: "AGT-0231" }) : list());
    const first = renderAi();
    await waitFor(() => expect(posts(":generate")).toHaveLength(1));
    first.unmount();
    renderAi({ identity: first.identity });
    await screen.findByText("No briefing yet");
    expect(posts(":generate")).toHaveLength(1);
  });

  it.each([
    ["409 in flight", json(409, { code: "AGT-0231" })],
    ["429 rate limited", json(429, { code: "AGT-0230" }, { "Retry-After": "120" })],
    ["a server error", json(500, { code: "AGT-0500" })],
  ])("makes no further automatic attempt after %s and shows no dialog", async (_label, reply) => {
    server.handler = (url) => (url.pathname === GENERATE ? reply : list(briefing({ createdAt: STALE, completedAt: STALE })));
    renderAi();
    await screen.findByRole("button", { name: "Recommended actions (1)" });
    await waitFor(() => expect(posts(":generate")).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("status")).toBeEmptyDOMElement());
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(posts(":generate")).toHaveLength(1);
  });

  it("skips the automatic call when a briefing already exists for the current local day", async () => {
    server.handler = () => list(briefing({ createdAt: new Date().toISOString(), completedAt: null }));
    renderAi();
    await screen.findByRole("button", { name: "Recommended actions (1)" });
    expect(posts(":generate")).toHaveLength(0);
  });

  it.each([
    ["AI is unavailable", { availability: DISABLED }],
    ["the user lacks agents.invoke", { duties: ["agents.run.read"] }],
  ])("makes no automatic call when %s, and not later that day either", async (_label, options) => {
    server.handler = () => list();
    const view = renderAi(options);
    await screen.findByText("No briefing yet");
    view.unmount();
    renderAi({ identity: view.identity });
    await screen.findByText("No briefing yet");
    expect(posts(":generate")).toHaveLength(0);
  });
});

describe("BriefingCard on-demand generation, read state and past briefings", () => {
  it("shows Try again in N minutes from Retry-After when Generate now is rate limited", async () => {
    server.handler = (url) => (url.pathname === GENERATE ? json(429, { code: "AGT-0230" }, { "Retry-After": "120" }) : list());
    renderAi({ identity: "claimed-today" });
    await screen.findByText("No briefing yet");
    await waitFor(() => expect(posts(":generate")).toHaveLength(1));
    expect(await screen.findByText("Try again in 2 minutes.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate now" })).toBeDisabled();
  });

  it("starts a generation from Generate now on the empty state", async () => {
    server.handler = (url) => (url.pathname === GENERATE ? json(500, { code: "AGT-0500" }) : list());
    renderAi({ availability: ENABLED, duties: ["agents.invoke"] });
    await waitFor(() => expect(posts(":generate")).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Generate now" })).toBeEnabled());
    await userEvent.setup().click(screen.getByRole("button", { name: "Generate now" }));
    expect(posts(":generate")).toHaveLength(2);
    expect(await screen.findByText("The briefing could not be started. Try again in a moment.")).toBeInTheDocument();
  });

  it("marks the briefing read on the server and collapses it to a one-line summary", async () => {
    const shown = briefing({ createdAt: new Date().toISOString() });
    server.handler = (url) => (url.pathname.endsWith(":mark-read") ? json(200, { ...shown, readAt: "2026-11-20T09:00:00.000Z" }) : list(shown));
    renderCard();
    await userEvent.setup().click(await screen.findByRole("button", { name: "Mark as read" }));
    expect(await screen.findByText("Read: 1 changes, 1 risks, 1 recommended actions.")).toBeInTheDocument();
    expect(posts(`/${shown.id}:mark-read`)).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Recommended actions (1)" })).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "Show briefing" }));
    expect(screen.getByRole("button", { name: "Recommended actions (1)" })).toBeInTheDocument();
  });

  it("keeps the briefing open and says so when mark-read fails", async () => {
    server.handler = (url) => (url.pathname.endsWith(":mark-read") ? json(500, { code: "AGT-0500" }) : list(briefing({ createdAt: new Date().toISOString() })));
    renderCard();
    await userEvent.setup().click(await screen.findByRole("button", { name: "Mark as read" }));
    expect(await screen.findByText("The briefing could not be marked as read. Try again in a moment.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Recommended actions (1)" })).toBeInTheDocument();
  });

  it("renders a briefing whose readAt is set collapsed and lists past briefings", async () => {
    const today = new Date().toISOString();
    server.handler = () => list(briefing({ id: "a", readAt: today, createdAt: today }), briefing({ id: "b", createdAt: STALE, completedAt: STALE }));
    const { container } = renderCard();
    expect(await screen.findByText("Read: 1 changes, 1 risks, 1 recommended actions.")).toBeInTheDocument();
    const past = screen.getByRole("button", { name: "Past briefings (1)" });
    expect(past).toHaveAttribute("aria-expanded", "false");
    await userEvent.setup().click(past);
    expect(past).toHaveAttribute("aria-expanded", "true");
    expect(within(screen.getByRole("list", { name: "Past briefings (1)" })).getAllByRole("listitem")).toHaveLength(1);
    expect(pageViolations(container)).toEqual([]);
  });

  it("has no axe violations on the empty state", async () => {
    server.handler = () => list();
    const { container } = renderCard();
    await screen.findByText("No briefing yet");
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("briefingItemRoute", () => {
  const item = (over: Partial<BriefingItem>): BriefingItem => ({ agentId: "ap_coding", factId: "f", text: "t", ...over });

  it("returns the proposal route for a valid route or a valid action id", () => {
    expect(briefingItemRoute(item({ route: `/assistant/proposals/${APPROVAL_ID}` }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
    expect(briefingItemRoute(item({ actionId: APPROVAL_ID }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
  });

  it.each([
    ["no route or action id", {}],
    ["an empty route", { route: "" }],
    ["a protocol-relative route", { route: "//evil.example/assistant/proposals/x" }],
    ["a script route", { route: "javascript:alert(1)" }],
    ["a route with a non-uuid id", { route: "/assistant/proposals/not-a-uuid" }],
    ["a route with a path-traversal id", { route: `/assistant/proposals/${APPROVAL_ID}/../../admin` }],
    ["a non-uuid action id", { actionId: "../admin" }],
  ])("returns no link for %s", (_label, over) => {
    expect(briefingItemRoute(item(over))).toBeUndefined();
  });

  it("falls back to a valid action id when the route is invalid", () => {
    expect(briefingItemRoute(item({ route: "//evil.example", actionId: APPROVAL_ID }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
  });
});

describe("briefingListSchema", () => {
  const body = { items: [briefing()], nextCursor: null, nextScheduledAt: null };

  it("accepts the documented shape", () => {
    expect(briefingListSchema.safeParse(body).success).toBe(true);
  });

  it.each([
    ["an unknown agent id", { ...body, items: [briefing({ changes: [{ ...action, agentId: "rogue" as BriefingItem["agentId"] }] })] }],
    ["a string narrative", { ...body, items: [{ ...briefing(), narrative: "true" }] }],
    ["a non-ISO createdAt", { ...body, items: [briefing({ createdAt: "yesterday" })] }],
    ["a missing items array", { nextCursor: null, nextScheduledAt: null }],
    ["a scheduled time", { ...body, nextScheduledAt: "2026-11-20T08:00:00.000Z" }],
  ])("rejects %s", (_label, bad) => {
    expect(briefingListSchema.safeParse(bad).success).toBe(false);
  });
});
