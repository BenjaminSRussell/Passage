import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { loadModules, type PassageModule } from "../../modules/registry";
import { Shell } from "../Shell";
import { json, pageViolations, server, useFakeServer } from "../testing/fakeServer";
import { HomePage } from "./HomePage";

useFakeServer();

const MAX_HOME_LINKS = 30;

const modules: PassageModule[] = [
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

function renderHome(duties: string[], shellModules: PassageModule[] = modules) {
  return render(
    <MemoryRouter>
      <Routes>
        <Route element={<Shell modules={shellModules} duties={duties} />}>
          <Route path="/" element={<HomePage />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const main = () => within(screen.getByRole("main"));

describe("role home", () => {
  it("shows the My work count and the top five items with a link to all of them", async () => {
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("7 items are waiting for you.")).toBeInTheDocument();
    const list = main().getByRole("list", { name: "My work" });
    expect(within(list).getAllByRole("link")).toHaveLength(5);
    expect(main().getByRole("link", { name: "See all my work" })).toHaveAttribute("href", "/workflow/inbox");
  });

  it("shows a plus when more work is waiting than was read", async () => {
    server.handler = () => json(200, { items: [item(1), item(2)], unavailable: [], truncated: ["ledger"] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("2+ items are waiting for you.")).toBeInTheDocument();
  });

  it("marks the active workspace and offers at most five shortcuts in it", () => {
    renderHome([]);
    expect(main().getByText("Current workspace")).toBeInTheDocument();
    expect(main().getAllByRole("list", { name: /Top tasks/ }).every((l) => within(l).getAllByRole("link").length <= 5)).toBe(true);
  });

  it("says You're all caught up when nothing is waiting, and has no axe violations", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    const { container } = renderHome(["inbox.read"]);
    expect(await main().findByText("You're all caught up")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a partial load and retries", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, { items: [item(1)], unavailable: [{ source: "ledger", reason: "timeout" }], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("Some items could not be loaded: journal batches")).toBeInTheDocument();
    await user.click(main().getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(2));
  });

  it("shows an error with retry when My work cannot be loaded", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderHome(["inbox.read"]);
    expect(await main().findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("leaves My work out, and asks nothing of the server, for users without the inbox duty", () => {
    renderHome([]);
    expect(main().queryByText("My work")).not.toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("keeps the number of links on home within the limit even for a user with many workspaces", async () => {
    const all = loadModules();
    const duties = [...new Set(all.flatMap((m) => m.nav.flatMap((n) => (n.duty ? [n.duty] : []))).concat("inbox.read"))];
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(duties, all);
    await main().findByText("7 items are waiting for you.");
    expect(main().getAllByRole("link").length).toBeLessThanOrEqual(MAX_HOME_LINKS);
  });

  it("shows at-a-glance tiles: waiting, approvals, overdue and the oldest wait", async () => {
    const old = { ...item(1), actions: ["approve"], createdAt: "2026-01-01T00:00:00Z", dueDate: "2026-01-02" };
    const fresh = { ...item(2), actions: ["open"], createdAt: new Date().toISOString() };
    server.handler = () => json(200, { items: [old, fresh], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    const tiles = within(await main().findByRole("list", { name: "My work at a glance" }));
    expect(tiles.getByText("Waiting for me").closest("li")).toHaveTextContent("2");
    expect(tiles.getByText("To approve").closest("li")).toHaveTextContent("1");
    expect(tiles.getByText("Overdue").closest("li")).toHaveTextContent("1");
    expect(Number(tiles.getByText("Oldest, in days").closest("li")?.textContent?.replace(/\D/g, ""))).toBeGreaterThan(100);
  });

  it("puts the count on a My work link in the sidebar, and no badge when nothing waits", async () => {
    server.handler = () => json(200, { items: [item(1), item(2), item(3)], unavailable: [], truncated: [] });
    const { unmount } = renderHome(["inbox.read"]);
    const link = await screen.findByRole("link", { name: /^My work.*3 waiting$/ });
    expect(link).toHaveAttribute("href", "/workflow/inbox");
    unmount();
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("You're all caught up");
    expect(screen.getByRole("link", { name: "My work" })).toBeInTheDocument();
  });

  it("reads the inbox once for the sidebar, the tiles and the list", async () => {
    server.handler = () => json(200, { items: [item(1)], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("1 item is waiting for you.");
    expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(1);
  });

  it("tells a new user what will appear in My work and offers no My work link without the duty", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText(/Approvals, holds and other tasks assigned to you will appear here/)).toBeInTheDocument();
    cleanup();
    renderHome([]);
    expect(screen.queryByRole("link", { name: /^My work/ })).not.toBeInTheDocument();
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
          <Route element={<Shell modules={modules} duties={[]} identity="u1" />}>
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
});
