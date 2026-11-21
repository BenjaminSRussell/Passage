import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearHandedOffPrompt, handOffPrompt } from "../assistant/session";
import { panelHeadingSteps } from "../../modules/copilot/a11yTest";
import { CopilotHome } from "../../modules/copilot/pages";
import { loadModules, type DepotModule } from "../../modules/registry";
import { Shell } from "../Shell";
import { SAFE_INLINE_TOOLS } from "../home/feedSources";
import { AVAILABILITY_PATH } from "../assistant/availability";
import { availableAssistant, json, pageViolations, server, useFakeServer, type Reply } from "../testing/fakeServer";
import { completedOnboardingStorage } from "../testing/onboardingFixtures";
import { AllFeaturesPage } from "./AllFeaturesPage";
import { HomePage } from "./HomePage";
import { workspaceKpis } from "./kpi";

useFakeServer();

const MAX_HOME_LINKS = 30;
const PAGE_CONTROLS = ":is(a[href], button, input, select, textarea):not([class*=\"topbar\"] *)";

const modules: DepotModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [],
    nav: [
      { label: "Journal batches", to: "/ledger/journal-batches" },
      { label: "Open items", to: "/ledger/open-items" },
    ],
  },
];

const recordModules: DepotModule[] = [{ id: "ledger", title: "General ledger", routes: [{ path: "/ledger/entries/:id", element: <div>Entry</div> }], nav: [] }];

const item = (n: number) => ({
  id: `00000000-0000-4000-8000-00000000000${n}`,
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: `PR-${n}`,
  link: `/purchasing/requisitions/${n}`,
  actions: ["open"],
  createdAt: "2026-11-01T00:00:00Z",
});

const enabled = availableAssistant;
let availability: Reply = json(200, enabled);
beforeEach(() => {
  availability = json(200, enabled);
});

function renderHome(duties: string[], shellModules: DepotModule[] = modules, entry: { pathname: string } = { pathname: "/" }, features?: Record<string, boolean>) {
  const inner = server.handler;
  server.handler = (url, init) => (url.pathname === AVAILABILITY_PATH ? availability : inner(url, init));
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route element={<Shell modules={shellModules} duties={duties} features={features} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/copilot" element={<CopilotHome />} />
          <Route path="/features" element={<AllFeaturesPage />} />
          <Route path="/ledger/entries/:id" element={<div>Entry</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const main = () => within(screen.getByRole("main"));

describe("role home", () => {
  it("shows the count and every item, each with a Review link named by its identifier", async () => {
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("7 items need your attention.")).toBeInTheDocument();
    const list = main().getByRole("list", { name: "Needs your attention" });
    expect(within(list).getAllByRole("link")).toHaveLength(7);
    expect(within(list).getByRole("link", { name: "Review Requisition approval PR-3" })).toHaveAttribute("href", "/purchasing/requisitions/3");
    expect(main().queryByRole("link", { name: "See all my work" })).not.toBeInTheDocument();
  });

  it("shows the first eight of eleven items and a See all link to the inbox", async () => {
    server.handler = () => json(200, { items: Array.from({ length: 11 }, (_, n) => item(n + 1)), unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("11 items need your attention.");
    expect(within(main().getByRole("list", { name: "Needs your attention" })).getAllByRole("link")).toHaveLength(8);
    expect(main().getByRole("link", { name: "See all my work" })).toHaveAttribute("href", "/workflow/inbox");
  });

  it("shows a plus when more work is waiting than was read", async () => {
    server.handler = () => json(200, { items: [item(1), item(2)], unavailable: [], truncated: ["ledger"] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("2+ items need your attention.")).toBeInTheDocument();
    expect(main().getByRole("link", { name: "See all my work" })).toBeInTheDocument();
  });

  it("marks the active workspace and offers at most five shortcuts in it", () => {
    renderHome([]);
    expect(main().getByText("Current workspace")).toBeInTheDocument();
    expect(main().getAllByRole("list", { name: /Top tasks/ }).every((l) => within(l).getAllByRole("link").length <= 5)).toBe(true);
  });

  it("says nothing needs attention when nothing is waiting, and has no axe violations", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    const { container } = renderHome(["inbox.read"]);
    expect(await main().findByText("Nothing needs your attention")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a partial load, names the source, retries once per click and has no axe violations", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, { items: [item(1)], unavailable: [{ source: "ledger", reason: "timeout" }], truncated: [] });
    const { container } = renderHome(["inbox.read"]);
    expect(await main().findByText("Some items could not be loaded: journal batches")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    await user.click(main().getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(2));
  });

  it("shows an error with retry when My work cannot be loaded", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderHome(["inbox.read"]);
    expect(await main().findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("leaves the feed out, and asks nothing of the server, for users with neither inbox nor planning duty", () => {
    renderHome([]);
    expect(main().queryByText("Needs your attention")).not.toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("keeps the number of links on home within the limit even for a user with many workspaces", async () => {
    const all = loadModules();
    const kpiDuties = Object.values(workspaceKpis).flatMap((tiles) => tiles.map((t) => t.duty));
    const duties = [...new Set(all.flatMap((m) => m.nav.flatMap((n) => (n.duty ? [n.duty] : []))).concat("inbox.read", ...kpiDuties))];
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(duties, all);
    await main().findByText("7 items need your attention.");
    await waitFor(() => expect(main().getAllByRole("list", { name: / at a glance$/ }).length).toBeGreaterThan(0));
    expect(main().getAllByRole("link").length).toBeLessThanOrEqual(MAX_HOME_LINKS);
  });

  it("puts the count on a My work link in the sidebar, and no badge when nothing waits", async () => {
    server.handler = () => json(200, { items: [item(1), item(2), item(3)], unavailable: [], truncated: [] });
    const { unmount } = renderHome(["inbox.read"]);
    const link = await screen.findByRole("link", { name: /^My work.*3 waiting$/ });
    expect(link).toHaveAttribute("href", "/workflow/inbox");
    unmount();
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("Nothing needs your attention");
    expect(screen.getByRole("link", { name: "My work" })).toBeInTheDocument();
  });

  it("reads the inbox once for the sidebar and the feed", async () => {
    server.handler = () => json(200, { items: [item(1)], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("1 item needs your attention.");
    expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(1);
  });

  it("tells a new user what will appear and offers no My work link without the duty", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText(/Approvals, holds, overdue tasks and planning messages assigned to you will appear here/)).toBeInTheDocument();
    cleanup();
    renderHome([]);
    expect(screen.queryByRole("link", { name: /^My work/ })).not.toBeInTheDocument();
  });

  describe("attention feed sources", () => {
    const message = (n: number, over: Record<string, unknown> = {}) => ({
      id: `msg-${n}`,
      status: "open",
      version: 1,
      action: "postpone",
      itemId: `item-${n}`,
      supplyRef: `PO-${n}`,
      text: "Move the order out",
      delayDays: 0,
      qty: "5",
      ...over,
    });
    const inboxBody = (...items: Record<string, unknown>[]) => json(200, { items, unavailable: [], truncated: [] });
    const PLANNING = "/api/v1/planning/messages";
    const APPROVALS = "/api/v1/agents/approvals";
    const NO_APPROVALS = json(200, { items: [] });
    const serveBoth = (planning: Reply, inbox: Reply = inboxBody(item(1)), approvals: Reply = NO_APPROVALS) => {
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? inbox : url.pathname === PLANNING ? planning : url.pathname === APPROVALS ? approvals : undefined);
    };
    const approval = (n: number, over: Record<string, unknown> = {}) => ({
      runId: `00000000-0000-4000-8000-0000000000a${n}`,
      actionId: `00000000-0000-4000-8000-0000000000b${n}`,
      agentId: "cash_application",
      tool: "depot.ar.settlement.propose",
      state: "PENDING",
      amount: { amount: "1200.50", currency: "USD" },
      requestedAt: "2026-11-19T10:00:00.000Z",
      expiresAt: "2099-11-22T10:00:00.000Z",
      ...over,
    });
    const partyItem = { ...item(1), id: "p1", source: "party", kind: "Customer change", number: "CC-1", actions: ["approve", "reject"] };
    const decisionCalls = () => server.calls.filter((c) => c.method === "POST");
    const planningCalls = () => server.calls.filter((c) => c.url.pathname === PLANNING);

    it("merges inbox tasks and planning messages, overdue first, and requests open messages by priority", async () => {
      serveBoth(json(200, { items: [message(1), message(2, { delayDays: 4 })] }), inboxBody({ ...item(1), dueDate: "2026-11-02" }, item(2)));
      renderHome(["inbox.read", "planning.read"]);
      await main().findByText("4 items need your attention.");
      const names = within(main().getByRole("list", { name: "Needs your attention" })).getAllByRole("link").map((l) => l.getAttribute("aria-label"));
      expect(names).toEqual([
        "Review Requisition approval PR-1",
        "Review Planning delay PO-2",
        "Review Requisition approval PR-2",
        "Review Planning action PO-1",
      ]);
      expect(main().getByRole("link", { name: "Review Planning delay PO-2" })).toHaveAttribute("href", "/planning/messages/msg-2?plan=MAIN");
      expect(planningCalls()[0].url.search).toBe("?planCode=MAIN&sort=priority&status=open&limit=50");
    });

    it("does not request planning messages, and shows no placeholder, without planning.read", async () => {
      serveBoth(json(200, { items: [message(1)] }));
      renderHome(["inbox.read"]);
      await main().findByText("1 item needs your attention.");
      expect(planningCalls()).toHaveLength(0);
      expect(within(main().getByRole("list", { name: "Needs your attention" })).queryByText(/Planning/)).not.toBeInTheDocument();
    });

    it("does not request the inbox without inbox.read and still shows planning messages", async () => {
      serveBoth(json(200, { items: [message(1)] }));
      renderHome(["planning.read"]);
      await main().findByText("1 item needs your attention.");
      expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(0);
    });

    it.each([
      ["a 500", json(500, { code: "PLAN-500" })],
      ["a 403", json(403, { code: "PLAN-403" })],
      ["a body of the wrong type", json(200, { items: "none" })],
      ["a message with a string delay", json(200, { items: [message(1, { delayDays: "3" })] })],
    ])("names planning messages as unavailable on %s while inbox rows still render", async (_name, reply: Reply) => {
      serveBoth(reply);
      const { container } = renderHome(["inbox.read", "planning.read"]);
      expect(await main().findByText("Some items could not be loaded: planning messages")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "Review Requisition approval PR-1" })).toBeInTheDocument();
      expect(main().queryByText(/Planning (action|delay)/)).not.toBeInTheDocument();
      expect(pageViolations(container)).toEqual([]);
    });

    it("names workflow tasks as unavailable when the inbox fails and planning rows still render", async () => {
      serveBoth(json(200, { items: [message(1)] }), json(500, { code: "COM-0500" }));
      renderHome(["inbox.read", "planning.read"]);
      expect(await main().findByText("Some items could not be loaded: workflow tasks")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "Review Planning action PO-1" })).toBeInTheDocument();
    });

    it("retries planning once per click and never on its own", async () => {
      const user = userEvent.setup();
      serveBoth(json(500, {}));
      renderHome(["inbox.read", "planning.read"]);
      await main().findByText("Some items could not be loaded: planning messages");
      expect(planningCalls()).toHaveLength(1);
      await user.click(main().getByRole("button", { name: "Try again" }));
      await waitFor(() => expect(planningCalls()).toHaveLength(2));
      await main().findByText("Some items could not be loaded: planning messages");
      expect(planningCalls()).toHaveLength(2);
    });

    it("offers only Review for exceptions and for inbox items of a source with no inline decision", async () => {
      serveBoth(json(200, { items: [message(1)] }), inboxBody({ ...item(1), actions: ["approve", "reject", "open"] }));
      const { container } = renderHome(["inbox.read", "planning.read", "procurement.requisition.approve"]);
      await main().findByText("2 items need your attention.");
      const list = within(main().getByRole("list", { name: "Needs your attention" }));
      expect(list.queryByRole("button")).not.toBeInTheDocument();
      expect(list.getAllByRole("link").every((l) => l.textContent === "Review")).toBe(true);
      expect(pageViolations(container)).toEqual([]);
    });

    it("offers inline Approve and Reject on a party task only to a holder of its decision duty", async () => {
      serveBoth(json(200, { items: [] }), inboxBody(partyItem));
      renderHome(["inbox.read"]);
      await main().findByText("1 item needs your attention.");
      expect(main().queryByRole("button", { name: /Approve Customer change CC-1/ })).not.toBeInTheDocument();
      cleanup();
      renderHome(["inbox.read", "party.approve"]);
      expect(await main().findByRole("button", { name: "Approve Customer change CC-1" })).toBeInTheDocument();
      expect(main().getByRole("button", { name: "Reject Customer change CC-1" })).toBeInTheDocument();
      expect(main().getByRole("link", { name: "Review Customer change CC-1" })).toBeInTheDocument();
    });

    it("calls the decision endpoint once, removes the row and announces the result", async () => {
      const user = userEvent.setup();
      serveBoth(json(200, { items: [] }), inboxBody(partyItem));
      const inner = server.handler;
      server.handler = (url, init) => (url.pathname === "/api/v1/party/pending-changes/p1:approve" ? json(200, {}) : inner(url, init));
      const { container } = renderHome(["inbox.read", "party.approve"]);
      await user.click(await main().findByRole("button", { name: "Approve Customer change CC-1" }));
      expect(await main().findByText("Customer change CC-1 approved.")).toBeInTheDocument();
      expect(main().queryByRole("button", { name: /Customer change CC-1/ })).not.toBeInTheDocument();
      expect(decisionCalls()).toHaveLength(1);
      expect(pageViolations(container)).toEqual([]);
    });

    it.each([
      ["a 409 conflict", 409, "PTY-0409"],
      ["a 403", 403, "COM-0403"],
    ])("restores the row and shows the problem on %s, with no retry", async (_name, status, code) => {
      const user = userEvent.setup();
      serveBoth(json(200, { items: [] }), inboxBody(partyItem));
      const inner = server.handler;
      server.handler = (url, init) => (url.pathname === "/api/v1/party/pending-changes/p1:approve" ? json(status, { code, detail: "refused" }) : inner(url, init));
      renderHome(["inbox.read", "party.approve"]);
      await user.click(await main().findByRole("button", { name: "Approve Customer change CC-1" }));
      expect(await main().findByText(/Customer change CC-1 was not decided/)).toBeInTheDocument();
      expect(main().getByRole("button", { name: "Approve Customer change CC-1" })).toBeEnabled();
      expect(decisionCalls()).toHaveLength(1);
    });

    describe("agent approvals", () => {
      afterEach(() => SAFE_INLINE_TOOLS.clear());

      it("lists approvals with the AI badge, Why this?, Data used and a Review link to the proposal, and no inline decision for a settlement", async () => {
        serveBoth(json(200, { items: [] }), inboxBody(), json(200, { items: [approval(1)] }));
        const { container } = renderHome(["inbox.read", "agents.run.read"]);
        await main().findByText("1 item needs your attention.");
        const row = within(main().getByRole("group", { name: "AI suggestion" }));
        expect(row.getByText(/AI-generated/)).toBeInTheDocument();
        expect(row.getByRole("button", { name: "Why this?" })).toBeInTheDocument();
        expect(row.getByRole("button", { name: "Data used" })).toBeInTheDocument();
        expect(main().getByRole("link", { name: /^Review Agent approval/ })).toHaveAttribute("href", "/assistant/proposals/00000000-0000-4000-8000-0000000000b1");
        expect(main().queryByRole("button", { name: /^(Approve|Reject)/ })).not.toBeInTheDocument();
        expect(server.calls.filter((c) => c.url.pathname === APPROVALS)[0].url.search).toBe("?limit=50");
        expect(pageViolations(container)).toEqual([]);
      });

      it("renders each Data used panel heading one level under the feed card title", async () => {
        serveBoth(json(200, { items: [] }), inboxBody(), json(200, { items: [approval(1)] }));
        renderHome(["inbox.read", "agents.run.read"]);
        await main().findByText("1 item needs your attention.");
        await userEvent.setup().click(main().getByRole("button", { name: "Data used" }));
        const feed = screen.getByRole("heading", { name: "Needs your attention", level: 2 }).parentElement?.parentElement;
        if (!feed) throw new Error("feed card is missing");
        expect(panelHeadingSteps(feed)).toEqual([1, 1]);
      });

      it("does not request approvals without agents.run.read", async () => {
        serveBoth(json(200, { items: [] }), inboxBody(item(1)));
        renderHome(["inbox.read"]);
        await main().findByText("1 item needs your attention.");
        expect(server.calls.filter((c) => c.url.pathname === APPROVALS)).toHaveLength(0);
      });

      it("names agent approvals as unavailable while the other sources render", async () => {
        serveBoth(json(200, { items: [] }), inboxBody(item(1)), json(500, {}));
        renderHome(["inbox.read", "agents.run.read"]);
        expect(await main().findByText("Some items could not be loaded: agent approvals")).toBeInTheDocument();
        expect(main().getByRole("link", { name: "Review Requisition approval PR-1" })).toBeInTheDocument();
      });

      it("decides an allowlisted tool inline through the run decision endpoint", async () => {
        const user = userEvent.setup();
        SAFE_INLINE_TOOLS.add("depot.ar.settlement.propose");
        serveBoth(json(200, { items: [] }), inboxBody(), json(200, { items: [approval(1)] }));
        const inner = server.handler;
        server.handler = (url, init) => (url.pathname.endsWith("/decision") ? json(200, {}) : inner(url, init));
        renderHome(["inbox.read", "agents.run.read"]);
        await user.click(await main().findByRole("button", { name: /^Reject Agent approval/ }));
        await waitFor(() => expect(decisionCalls()).toHaveLength(1));
        expect(decisionCalls()[0].url.pathname).toBe("/api/v1/agents/runs/00000000-0000-4000-8000-0000000000a1/actions/00000000-0000-4000-8000-0000000000b1/decision");
        expect(decisionCalls()[0].body).toEqual({ decision: "REJECTED" });
      });

      it("disables the control and gives the reason on AGT-0209", async () => {
        const user = userEvent.setup();
        SAFE_INLINE_TOOLS.add("depot.ar.settlement.propose");
        serveBoth(json(200, { items: [] }), inboxBody(), json(200, { items: [approval(1)] }));
        const inner = server.handler;
        server.handler = (url, init) => (url.pathname.endsWith("/decision") ? json(403, { code: "AGT-0209", detail: "self approval" }) : inner(url, init));
        renderHome(["inbox.read", "agents.run.read"]);
        await user.click(await main().findByRole("button", { name: /^Approve Agent approval/ }));
        expect(await main().findByText(/someone else must decide/)).toBeInTheDocument();
        expect(main().getByRole("button", { name: /^Approve Agent approval/ })).toBeDisabled();
        expect(decisionCalls()).toHaveLength(1);
      });
    });

    it("says nothing needs attention when both enabled sources are empty", async () => {
      serveBoth(json(200, { items: [] }), inboxBody());
      renderHome(["inbox.read", "planning.read"]);
      expect(await main().findByText("Nothing needs your attention")).toBeInTheDocument();
      expect(main().queryByRole("list", { name: "Needs your attention" })).not.toBeInTheDocument();
    });
  });

  it("offers a switch button, not more links, for the other workspaces", async () => {
    const user = userEvent.setup();
    renderHome([], [
      ...modules,
      { id: "purchasing", title: "Purchasing", routes: [], nav: [{ label: "Purchase orders", to: "/purchasing/purchase-orders" }] },
    ]);
    const switchers = main().getAllByRole("button", { name: /^Switch to / });
    expect(switchers.length).toBeGreaterThan(0);
    const before = main().getAllByRole("heading", { level: 3 })[0].textContent;
    await user.click(switchers[0]);
    expect(main().getAllByRole("heading", { level: 3 })[0].textContent).not.toBe(before);
  });

  it("remembers the pages opened last and lists them on home", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/ledger/journal-batches"]}>
        <Routes>
          <Route element={<Shell modules={modules} duties={[]} identity="u1" onboardingStorage={completedOnboardingStorage("u1")} />}>
            <Route path="/" element={<HomePage />} />
            <Route path="/ledger/journal-batches" element={<p>Journals page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await user.click(within(screen.getByRole("navigation", { name: "Workspaces" })).getByRole("link", { name: "Home" }));
    const recent = within(main().getByRole("list", { name: "Recent" }));
    expect(recent.getAllByRole("link")[0]).toHaveAttribute("href", "/ledger/journal-batches");
  });

  it("invites an administrator to finish setup, and stays quiet when setup is finished", async () => {
    const setup = (finished: number) => json(200, { steps: [], total: 10, finished, complete: finished === 10 });
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? setup(4) : undefined);
    const first = renderHome(["identity.setup.maintain"]);
    expect(await main().findByText("4 of 10 setup steps are finished.")).toBeInTheDocument();
    expect(main().getByRole("link", { name: "Continue setup" })).toHaveAttribute("href", "/setup");
    first.unmount();
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? setup(10) : undefined);
    renderHome(["identity.setup.maintain"]);
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/tenant/setup")).toBe(true));
    expect(main().queryByText(/setup steps are finished/)).not.toBeInTheDocument();
  });

  describe("assistant-led home", () => {
    const COPILOT_DUTY = "mcp.copilot.use";
    const askBox = () => main().findByRole("textbox", { name: "Ask or do anything..." });

    it("puts the prompt first in the main landmark and the workspace cards in a Browse workspaces region", async () => {
      renderHome([COPILOT_DUTY]);
      const box = await askBox();
      expect(screen.getByRole("main").querySelector(PAGE_CONTROLS)).toBe(box);
      const region = within(main().getByRole("region", { name: "Browse workspaces" }));
      expect(region.getByText("Current workspace")).toBeInTheDocument();
      expect(region.getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(box.compareDocumentPosition(main().getByRole("region", { name: "Browse workspaces" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("shows an error and makes no request when the prompt is empty", async () => {
      const user = userEvent.setup();
      renderHome([COPILOT_DUTY]);
      await askBox();
      await user.click(main().getByRole("button", { name: "Send" }));
      expect(main().getByRole("alert")).toHaveTextContent("Type a question or a request first.");
      expect(server.calls.filter((c) => c.url.pathname !== AVAILABILITY_PATH)).toHaveLength(0);
    });

    const CONVERSATIONS = "/api/v1/agents/copilot/conversations";
    const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
    const MESSAGES = `${CONVERSATIONS}/${CONVERSATION_ID}/messages`;
    const chat = (answer = json(200, { answer: "Cash is steady.", citations: [], toolCalls: [], refusal: null, generated: true })) => (url: URL) =>
      url.pathname === "/api/v1/copilot/chat" ? routeReply : url.pathname === CONVERSATIONS ? json(201, { conversationId: CONVERSATION_ID }) : url.pathname === MESSAGES ? answer : undefined;
    let routeReply: Reply = json(200, { kind: "ask", confidence: 1, generated: true });
    beforeEach(() => {
      routeReply = json(200, { kind: "ask", confidence: 1, generated: true });
    });
    const chats = () => server.calls.filter((c) => c.url.pathname === "/api/v1/copilot/chat" || c.url.pathname === MESSAGES);

    it("classifies the typed text first, then opens the copilot in ask mode and sends it once", async () => {
      const user = userEvent.setup();
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "how is cash{Enter}");
      expect(await main().findByRole("article", { name: "Copilot answer" })).toHaveTextContent("Cash is steady.");
      expect(chats().map((c) => c.body)).toEqual([
        { mode: "route", message: "how is cash" },
        { mode: "ask", message: "how is cash" },
      ]);
    });

    it.each(["navigate", "act"])("opens the copilot ask path when the %s handler is not registered yet", async (kind) => {
      const user = userEvent.setup();
      routeReply = json(200, { kind, confidence: 0.9, generated: true });
      server.handler = chat();
      renderHome([COPILOT_DUTY, "agents.invoke"]);
      await user.type(await askBox(), "show me overdue invoices{Enter}");
      expect(await main().findByRole("article", { name: "Copilot answer" })).toBeInTheDocument();
      expect(chats().map((c) => (c.body as { mode: string }).mode)).toEqual(["route", "ask"]);
    });

    it("falls back to the features page, never to the copilot, when the classification fails", async () => {
      const user = userEvent.setup();
      routeReply = json(503, { code: "AGT-0500" });
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "apply this payment to invoice INV-1042{Enter}");
      expect(await screen.findByRole("searchbox", { name: "Filter features" })).toHaveValue("apply this payment to invoice INV-1042");
      expect(chats().map((c) => (c.body as { mode: string }).mode)).toEqual(["route"]);
    });

    it("sends no copilot request and goes to the features page when the assistant turns unavailable after render", async () => {
      const user = userEvent.setup();
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      const box = await askBox();
      availability = json(200, { ...enabled, killSwitch: { ...enabled.killSwitch, halted: true } });
      fireEvent.focus(window);
      await user.type(box, "show me overdue invoices{Enter}");
      await waitFor(() => expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument());
      expect(chats()).toHaveLength(0);
    });

    it("answers inline on the home, in one new conversation, without opening the copilot page", async () => {
      const user = userEvent.setup();
      server.handler = chat(json(200, { answer: "Cash is steady.", citations: [], toolCalls: [{ tool: "metric.search", status: "OK" }], refusal: null, generated: true }));
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "how is cash{Enter}");
      const answer = await main().findByRole("article", { name: "Copilot answer" });
      expect(within(answer).getByText("AI-generated: check before you use it")).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(server.calls.filter((c) => c.url.pathname === CONVERSATIONS)).toHaveLength(1);
      expect(chats().at(-1)?.url.pathname).toBe(MESSAGES);
      expect(screen.getAllByRole("status").some((r) => r.textContent === "The copilot has finished answering.")).toBe(true);
    });

    it.each([
      ["an empty string", ""],
      ["only whitespace", "   "],
      ["nothing handed off", undefined],
    ])("sends nothing when the copilot opens with %s as its prompt", async (_name, handedOff) => {
      clearHandedOffPrompt();
      if (handedOff !== undefined) handOffPrompt(handedOff);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      expect(chats()).toHaveLength(0);
    });

    it("answers a question asked from the command bar while the copilot page is already open", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      expect(chats()).toHaveLength(0);
      await user.keyboard("{Control>}k{/Control}");
      await user.type(await screen.findByLabelText("Find a page"), "zzqq");
      await screen.findByRole("group", { name: "Assistant" });
      await user.keyboard("{Enter}");
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "zzqq" }));
    });

    it("sends each new command-bar prompt once while the copilot page stays open", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      for (const text of ["zzqq", "yyww"]) {
        await user.keyboard("{Control>}k{/Control}");
        await user.type(await screen.findByLabelText("Find a page"), text);
        await screen.findByRole("group", { name: "Assistant" });
        await user.keyboard("{Enter}");
        await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: text }));
      }
      await user.type(screen.getByLabelText("Ask Copilot"), "more");
      const sent = chats().map((c) => c.body as { mode: string; message: string });
      expect(sent.filter((b) => b.mode === "ask" && (b.message === "zzqq" || b.message === "yyww"))).toHaveLength(2);
    });

    it("updates the record context when Ctrl+/ is pressed on a record page and again on the copilot page", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], recordModules, { pathname: "/ledger/entries/JE-77" });
      await user.keyboard("{Control>}/{/Control}");
      await user.type(await screen.findByLabelText("Ask Copilot"), "first");
      await user.click(screen.getByRole("button", { name: "Send" }));
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "first", recordRef: "record:entries/JE-77" }));
      await user.keyboard("{Control>}/{/Control}");
      await user.type(await screen.findByLabelText("Ask Copilot"), "second");
      await user.click(screen.getByRole("button", { name: "Send" }));
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "second" }));
    });

    it("sends nothing when the handed-off prompt is older than ten seconds", async () => {
      handOffPrompt("how is cash");
      const now = Date.now();
      vi.spyOn(Date, "now").mockReturnValue(now + 11_000);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      vi.restoreAllMocks();
      expect(chats()).toHaveLength(0);
    });

    it("passes an oversize prompt through so the copilot shows the refusal instead of dropping it", async () => {
      const prompt = "x".repeat(4500);
      server.handler = chat(json(200, { answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0001", message: "That question is too long." }, generated: true }));
      handOffPrompt(prompt);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      expect(await main().findByText("That question is too long.")).toBeInTheDocument();
      expect(chats()[0].body).toEqual({ mode: "ask", message: prompt });
    });

    it("shows the classic cards as the primary content, with no prompt and no copilot request, without the duty", () => {
      renderHome([]);
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().queryByRole("region", { name: "Browse workspaces" })).not.toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(server.calls.filter((c) => c.url.pathname.includes("/copilot"))).toHaveLength(0);
    });

    it("shows the classic home, with no prompt to drop, when the copilot is switched off for the installation", () => {
      renderHome([COPILOT_DUTY], modules, { pathname: "/" }, { "install:node": false });
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("offers a workspace page as a link, one click away, inside the secondary region", async () => {
      renderHome([COPILOT_DUTY]);
      const region = within(await main().findByRole("region", { name: "Browse workspaces" }));
      const tasks = region.getAllByRole("list", { name: /Top tasks/ })[0];
      expect(within(tasks).getAllByRole("link")[0]).toHaveAttribute("href", expect.stringMatching(/^\//));
    });
  });

  describe("briefing card", () => {
    const BRIEFINGS = "/api/v1/agents/briefings";
    const READ_DUTY = "agents.run.read";
    const readyBriefing = {
      id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
      status: "READY",
      narrative: true,
      createdAt: "2026-11-19T10:00:00.000Z",
      completedAt: "2026-11-19T10:00:05.000Z",
      readAt: null,
      changes: [],
      risks: [],
      recommendedActions: [{ agentId: "collections", factId: "f1", text: "Send the reminder", actionId: "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11" }],
      notes: [],
    };
    const serveBriefings = () => {
      server.handler = (url) => (url.pathname === BRIEFINGS ? json(200, { items: [readyBriefing], nextCursor: null, nextScheduledAt: null }) : undefined);
    };

    it("sits below the prompt and above the workspace cards on the assistant-led home", async () => {
      serveBriefings();
      renderHome(["mcp.copilot.use", READ_DUTY]);
      const box = await main().findByRole("textbox", { name: "Ask or do anything..." });
      const card = await main().findByRole("heading", { level: 2, name: "Your briefing" });
      expect(box.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      expect(card.compareDocumentPosition(main().getByRole("region", { name: "Browse workspaces" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("shows on the classic home too, and a failing briefing leaves the rest of the home working", async () => {
      server.handler = (url) => (url.pathname === BRIEFINGS ? json(500, { code: "AGT-0500" }) : undefined);
      renderHome([READ_DUTY]);
      expect(await main().findByText("Your briefing could not be loaded")).toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("is absent, and asks nothing of the server, without the agents.run.read duty", async () => {
      serveBriefings();
      renderHome(["mcp.copilot.use"]);
      await main().findByRole("textbox", { name: "Ask or do anything..." });
      expect(main().queryByRole("heading", { name: "Your briefing" })).not.toBeInTheDocument();
      expect(server.calls.filter((c) => c.url.pathname === BRIEFINGS)).toHaveLength(0);
    });
  });

  describe("when the assistant is not available", () => {
    const COPILOT_DUTY = "mcp.copilot.use";
    const promptBox = () => screen.queryByRole("textbox", { name: "Ask or do anything..." });
    const paused = "The assistant is paused";
    const limit = "The assistant has reached its usage limit";
    const unreachable = "The assistant can't be reached right now";
    const withKill = (kill: object) => ({ ...enabled, killSwitch: { ...enabled.killSwitch, ...kill } });

    it.each([
      ["the kill switch is on", json(200, withKill({ halted: true })), paused],
      ["the budget is used up", json(200, { ...enabled, budgetStatus: "HARD" }), limit],
      ["the company has the assistant off", json(200, { ...enabled, copilotEnabled: false }), "The assistant is turned off"],
      ["the deployment waits for sign-off", json(200, { ...enabled, copilotEnabled: false, signoffPending: true }), "The assistant is not available in this deployment until sign-off"],
      ["the read fails with a server error", json(503, {}), unreachable],
      ["the body cannot be read", json(200, { killSwitch: { halted: "false" } }), unreachable],
    ])("shows the classic home, a notice and no prompt when %s", async (_name, reply: Reply, title) => {
      availability = reply;
      renderHome([COPILOT_DUTY]);
      expect(await main().findByText(title)).toBeInTheDocument();
      expect(promptBox()).not.toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("keeps the feed working and has no axe violations while the assistant is unreachable", async () => {
      availability = json(503, {});
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? json(200, { items: [item(1), item(2)], unavailable: [], truncated: [] }) : undefined);
      const { container } = renderHome([COPILOT_DUTY, "inbox.read"]);
      expect(await main().findByText("2 items need your attention.")).toBeInTheDocument();
      expect(await main().findByText(unreachable)).toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(pageViolations(container)).toEqual([]);
    });

    it("returns the assistant-led home on the next check when the budget is raised, with no notice left", async () => {
      availability = json(200, { ...enabled, budgetStatus: "HARD" });
      renderHome([COPILOT_DUTY]);
      expect(await main().findByText(limit)).toBeInTheDocument();
      availability = json(200, enabled);
      fireEvent.focus(window);
      expect(await main().findByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(main().queryByText(limit)).not.toBeInTheDocument();
    });

    it("shows a soft budget warning to an administrator and nothing to anyone else", async () => {
      availability = json(200, { ...enabled, budgetStatus: "SOFT" });
      const admin = renderHome([COPILOT_DUTY, "agents.admin"]);
      expect(await main().findByText("The assistant is close to its usage limit")).toBeInTheDocument();
      expect(promptBox()).toBeInTheDocument();
      admin.unmount();
      renderHome([COPILOT_DUTY]);
      expect(await main().findByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(main().queryByText("The assistant is close to its usage limit")).not.toBeInTheDocument();
    });
  });
});
