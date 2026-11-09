import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import type { PassageModule } from "../../modules/registry";
import { AllFeaturesPage } from "../pages/AllFeaturesPage";
import { HomePage } from "../pages/HomePage";
import { Shell } from "../Shell";
import { pageViolations } from "../testing/fakeServer";

const modules: PassageModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [],
    nav: [
      { label: "Journal batches", to: "/ledger/journal-batches", duty: "ledger.journal.read" },
      { label: "Open items", to: "/ledger/open-items" },
      { label: "Ledger settings", to: "/ledger/ledger-settings", duty: "ledger.journal.read" },
    ],
  },
  {
    id: "receivables",
    title: "Accounts receivable",
    routes: [],
    nav: [
      { label: "Record payment", to: "/receivables/payments/new" },
      { label: "Open items", to: "/receivables/open-items" },
    ],
  },
  {
    id: "admin",
    title: "Admin",
    routes: [],
    nav: [
      { label: "Audit", to: "/admin/audit" },
      { label: "Sequences", to: "/admin/sequences" },
    ],
  },
];

function renderShell(path = "/", duties: string[] = []) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<Shell modules={modules} duties={duties} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/features" element={<AllFeaturesPage />} />
          <Route path="*" element={<p>Some page</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const sidebar = () => within(screen.getByRole("navigation", { name: "Workspaces" }));

describe("workspace switcher", () => {
  it("lists every workspace the user has something in, defaulting to the first", () => {
    renderShell();
    const options = within(screen.getByLabelText("Workspace")).getAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual([
      "Accounts payable",
      "Accounts receivable",
      "Accounting and close",
      "Administration",
    ]);
    expect(screen.getByLabelText("Workspace")).toHaveValue("ap-clerk");
    expect(sidebar().getByRole("list", { name: "Accounts payable" })).toBeInTheDocument();
  });

  it("switches the sidebar to the chosen workspace and remembers it after a reload", async () => {
    const user = userEvent.setup();
    const first = renderShell();
    await user.selectOptions(screen.getByLabelText("Workspace"), "ar-clerk");
    expect(sidebar().getByRole("link", { name: "Record a customer payment" })).toHaveAttribute("href", "/receivables/payments/new");
    expect(sidebar().queryByRole("link", { name: "Review open items" })).not.toBeInTheDocument();
    first.unmount();
    renderShell();
    expect(screen.getByLabelText("Workspace")).toHaveValue("ar-clerk");
  });

  it("holds several workspaces and lets the user drop one, but never the last", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.click(screen.getByText("Choose my workspaces"));
    const group = screen.getByRole("group", { name: "Workspaces shown in the switcher" });
    await user.click(within(group).getByRole("checkbox", { name: "Accounts payable" }));
    expect(screen.getByLabelText("Workspace")).toHaveValue("ar-clerk");
    expect(within(screen.getByLabelText("Workspace")).queryByRole("option", { name: "Accounts payable" })).not.toBeInTheDocument();
    for (const name of ["Accounts receivable", "Accounting and close"]) {
      await user.click(within(group).getByRole("checkbox", { name }));
    }
    expect(screen.getByLabelText("Workspace")).toHaveValue("tenant-admin");
    expect(within(group).getByRole("checkbox", { name: "Administration" })).toBeDisabled();
  });

  it("falls back to all workspaces when saved choices are unreadable", () => {
    window.localStorage.setItem("passage.workspaces.v1", "{not json");
    renderShell();
    expect(screen.getByLabelText("Workspace")).toHaveValue("ap-clerk");
  });

  it("passes the in-repo accessibility checks", () => {
    const { container } = renderShell();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("duty gating inside workspaces", () => {
  it("hides a curated entry the user lacks the duty for, and shows it once granted", () => {
    const first = renderShell("/", []);
    expect(sidebar().queryByRole("link", { name: "Post a journal" })).not.toBeInTheDocument();
    expect(sidebar().getByRole("link", { name: "Review open items" })).toBeInTheDocument();
    first.unmount();
    renderShell("/", ["ledger.journal.read"]);
    expect(sidebar().getByRole("link", { name: "Post a journal" })).toBeInTheDocument();
  });

  it("keeps gated entries out of All features too", async () => {
    renderShell("/features", []);
    expect(screen.queryByRole("link", { name: /Ledger settings/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Post a journal/ })).not.toBeInTheDocument();
  });
});

describe("All features", () => {
  it("groups entries by business area and puts technical screens under Advanced", async () => {
    const user = userEvent.setup();
    renderShell("/features", ["ledger.journal.read"]);
    const finance = within(screen.getByRole("region", { name: "Finance" }));
    expect(finance.getByRole("link", { name: /Post a journal/ })).toHaveAttribute("href", "/ledger/journal-batches");
    const advanced = finance.getByText("Advanced (1)").closest("details") as HTMLElement;
    expect(advanced).not.toHaveAttribute("open");
    await user.click(finance.getByText("Advanced (1)"));
    expect(within(advanced).getByRole("link", { name: /Ledger settings/ })).toBeInTheDocument();
    expect(within(screen.getByRole("region", { name: "Administration" })).getByRole("link", { name: /Audit log/ })).toBeInTheDocument();
  });

  it("filters by task label or original screen name, opening Advanced matches, and reports no matches", async () => {
    const user = userEvent.setup();
    renderShell("/features", ["ledger.journal.read"]);
    await user.type(screen.getByLabelText("Filter features"), "settings");
    const main = within(screen.getByRole("main"));
    expect(main.getByRole("link", { name: /Ledger settings/ })).toBeVisible();
    expect(main.queryByRole("link", { name: /Audit log/ })).not.toBeInTheDocument();
    expect(screen.getByText("1 feature")).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Filter features"));
    await user.type(screen.getByLabelText("Filter features"), "Journal batches");
    expect(within(screen.getByRole("main")).getByRole("link", { name: /Post a journal/ })).toBeInTheDocument();
    await user.clear(screen.getByLabelText("Filter features"));
    await user.type(screen.getByLabelText("Filter features"), "zzz");
    expect(screen.getByText(/No features match/)).toBeInTheDocument();
  });

  it("is reachable from the sidebar and home", () => {
    renderShell("/", []);
    expect(sidebar().getByRole("link", { name: "All features" })).toHaveAttribute("href", "/features");
    expect(within(screen.getByRole("main")).getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
  });
});

describe("home", () => {
  it("shows the top tasks of each held workspace, active first", async () => {
    const user = userEvent.setup();
    renderShell("/", []);
    await user.selectOptions(screen.getByLabelText("Workspace"), "tenant-admin");
    const cards = within(screen.getByRole("list", { name: "Workspaces" }));
    const headings = cards.getAllByRole("heading", { level: 3 }).map((h) => h.textContent);
    expect(headings[0]).toBe("Administration");
    expect(cards.getByRole("list", { name: "Administration: Top tasks" })).toBeInTheDocument();
  });

  it("explains an empty state when nothing is available", () => {
    render(
      <MemoryRouter>
        <Routes>
          <Route element={<Shell modules={[]} duties={[]} />}>
            <Route path="/" element={<HomePage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText("No workspaces yet")).toBeInTheDocument();
    expect(screen.queryByLabelText("Workspace")).not.toBeInTheDocument();
  });
});
