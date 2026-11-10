import { render, screen, waitFor, within } from "@testing-library/react";
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
});
