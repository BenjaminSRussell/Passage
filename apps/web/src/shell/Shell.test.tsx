import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, vi } from "vitest";
import type { PassageModule } from "../modules/registry";
import { checkAxeAA } from "./a11y";
import { FormRenderer } from "./forms";
import { demoNote } from "./metadata";
import { pageViolations } from "./testing/fakeServer";
import { HomePage } from "./pages/HomePage";
import { NotesPage } from "./pages/NotesPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { usePrincipalDuties } from "./principal";
import { RouteErrorBoundary } from "./RouteErrorBoundary";
import { Shell } from "./Shell";
import { recordedErrors } from "./telemetry";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const modules: PassageModule[] = [
  {
    id: "admin",
    title: "Admin",
    description: "Numbering, audit, alerts, imports and documents.",
    routes: [{ path: "/admin", element: <div>Admin home</div> }],
    nav: [{ label: "Audit", to: "/admin/audit" }],
  },
];

function renderShell(path = "/") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<Shell modules={modules} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/notes" element={<NotesPage />} />
          <Route path="/admin/audit" element={<p>Audit page</p>} />
          <Route path="*" element={<NotFoundPage />} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("shell a11y and keyboard", () => {
  it("the shell passes the in-repo WCAG 2.1 AA checks", () => {
    const { container } = renderShell();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("has a single h1, workspace nav, skip link and labelled search", async () => {
    const user = userEvent.setup();
    renderShell();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("navigation", { name: "Workspaces" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Skip to content" })).toHaveAttribute("href", "#main");
    await user.click(screen.getByRole("button", { name: "Search" }));
    expect(screen.getByRole("dialog", { name: "Search" })).toBeInTheDocument();
    expect(screen.getByLabelText("Find a page")).toHaveFocus();
  });

  it("opens search from Control+K and closes on Escape", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getByRole("dialog", { name: "Search" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Search" })).not.toBeInTheDocument();
  });

  it("keyboard-navigates search results into a workspace", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    await user.type(screen.getByLabelText("Find a page"), "audit{Enter}");
    expect(screen.getByText("Audit page")).toBeInTheDocument();
  });

  it("applies a saved view from the keyboard-accessible control", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.click(screen.getByRole("button", { name: "Apply saved view" }));
    expect(screen.getByRole("status")).toHaveTextContent("Saved view applied");
  });
});

describe("pages and navigation", () => {
  it("lists a workspace card with its top tasks on home, and keeps the note form on /notes", () => {
    const home = renderShell();
    const main = within(screen.getByRole("main"));
    expect(main.getByRole("heading", { level: 3, name: "Administration" })).toBeInTheDocument();
    expect(main.getByRole("link", { name: /Audit log/ })).toHaveAttribute("href", "/admin/audit");
    expect(main.getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    expect(screen.queryByRole("button", { name: "Save Note" })).not.toBeInTheDocument();
    home.unmount();

    const notes = renderShell("/notes");
    expect(screen.getByRole("button", { name: "Save Note" })).toBeInTheDocument();
    expect(checkAxeAA(notes.container)).toEqual([]);
  });

  it("shows the page title as the only h1, a breadcrumb and the current nav item", () => {
    renderShell("/admin/audit");
    expect(screen.getByRole("heading", { level: 1, name: "Audit" })).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Breadcrumb" })).getByRole("link", { name: "Admin" })).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Workspaces" })).getByRole("link", { name: "Audit log" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("renders a not-found page for unknown routes", () => {
    renderShell("/nope");
    expect(screen.getByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "We couldn't find that page" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to home" })).toHaveAttribute("href", "/");
  });

  it("shows the active workspace's entries under its name, plus an All features link", () => {
    renderShell();
    const nav = within(screen.getByRole("navigation", { name: "Workspaces" }));
    expect(nav.getByRole("list", { name: "Administration" })).toBeInTheDocument();
    expect(nav.getByRole("link", { name: "Audit log" })).toHaveAttribute("href", "/admin/audit");
    expect(nav.getByRole("link", { name: "All features" })).toHaveAttribute("href", "/features");
  });

  it("sets aria-pressed and data-density when compact view is toggled", async () => {
    const user = userEvent.setup();
    const { container } = renderShell();
    const toggle = screen.getByRole("button", { name: "Compact view" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    expect(container.firstElementChild).toHaveAttribute("data-density", "compact");
  });
});

describe("command palette", () => {
  it("moves the active option with the arrow keys and opens it with Enter", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    await user.keyboard("{ArrowDown}");
    const input = screen.getByRole("combobox", { name: "Find a page" });
    const second = within(screen.getByRole("dialog", { name: "Search" })).getAllByRole("option")[1];
    expect(input).toHaveAttribute("aria-activedescendant", second.id);
    expect(second).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(screen.getByRole("heading", { level: 1, name: "Notes" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Notes" })).toHaveFocus();
  });

  it("wraps the active option at both ends", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    await user.keyboard("{ArrowUp}");
    const options = within(screen.getByRole("dialog", { name: "Search" })).getAllByRole("option");
    expect(options[options.length - 1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowDown}");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
  });

  it("groups options and shows a no-results state that ignores Enter", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(within(screen.getByRole("group", { name: "Admin" })).getByRole("option", { name: /Audit log/ })).toBeInTheDocument();
    await user.type(screen.getByRole("combobox", { name: "Find a page" }), "zzz");
    expect(screen.getByText(/No pages match/)).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Find a page" })).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{Enter}");
    expect(screen.getByRole("dialog", { name: "Search" })).toBeInTheDocument();
  });

  it("does not re-announce Search opened when it is already open", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getByRole("status")).toHaveTextContent("Search opened");
    await user.click(screen.getByRole("button", { name: "Apply saved view" }));
    expect(screen.getByRole("status")).toHaveTextContent("Saved view applied");
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getByRole("status")).toHaveTextContent("Saved view applied");
  });

  it("closes when the backdrop is pressed", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    const backdrop = screen.getByRole("dialog", { name: "Search" }).parentElement as HTMLElement;
    fireEvent.mouseDown(backdrop);
    expect(screen.queryByRole("dialog", { name: "Search" })).not.toBeInTheDocument();
  });

  it("names each option with its workspace and keeps the listbox mounted with no results", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getByRole("option", { name: "Audit log, Admin" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Home, General" })).toBeInTheDocument();
    await user.type(screen.getByRole("combobox", { name: "Find a page" }), "zzz");
    const listbox = document.getElementById("palette-listbox");
    expect(listbox).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Find a page" })).toHaveAttribute("aria-controls", "palette-listbox");
    expect(listbox).toBeEmptyDOMElement();
    expect(listbox?.contains(screen.getByText(/No pages match/))).toBe(false);
  });

  it("returns focus to the menu button when the search trigger is not visible", async () => {
    const user = userEvent.setup();
    const offsetParent = vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
      return this.hasAttribute("aria-keyshortcuts") ? null : document.body;
    });
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "Open navigation" })).toHaveFocus();
    offsetParent.mockRestore();
  });

  it("returns focus to the Search button on Escape", async () => {
    const user = userEvent.setup();
    renderShell();
    const trigger = screen.getByRole("button", { name: "Search" });
    await user.click(trigger);
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
  });
});

describe("navigation drawer", () => {
  it("opens, traps focus, and returns focus to the menu button on Escape", async () => {
    const user = userEvent.setup();
    renderShell();
    const open = screen.getByRole("button", { name: "Open navigation" });
    expect(open).toHaveAttribute("aria-expanded", "false");
    await user.click(open);
    expect(open).toHaveAttribute("aria-expanded", "true");
    const dialog = screen.getByRole("dialog", { name: "Navigation" });
    const close = within(dialog).getByRole("button", { name: "Close navigation" });
    expect(close).toHaveFocus();

    await user.tab({ shift: true });
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(close).not.toHaveFocus();
    await user.tab();
    expect(close).toHaveFocus();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Navigation" })).not.toBeInTheDocument();
    expect(open).toHaveFocus();
  });
});

describe("generated form keyboard and validation", () => {
  it("labels every field, announces validation, and edits a grid cell", async () => {
    const user = userEvent.setup();
    render(<FormRenderer entity={demoNote} lines={[{ title: "One" }]} />);
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Title is required");
    await user.type(screen.getByLabelText("Title"), "Kickoff");
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "One" }));
    expect(screen.getByLabelText("Edit Title")).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("button", { name: "One" })).toHaveFocus();
  });

  it("returns focus to the edit button after Enter, and keeps typed values", async () => {
    const user = userEvent.setup();
    render(<FormRenderer entity={demoNote} lines={[{ title: "One" }]} />);
    await user.click(screen.getByRole("button", { name: "One" }));
    await user.keyboard("{Enter}");
    expect(screen.getByRole("button", { name: "One" })).toHaveFocus();
  });

  it("focuses the first invalid field and marks required fields", async () => {
    const user = userEvent.setup();
    render(<FormRenderer entity={demoNote} />);
    expect(screen.getByLabelText("Title")).toHaveAttribute("aria-required", "true");
    expect(screen.getByLabelText("Body")).not.toHaveAttribute("aria-required");
    expect(screen.getAllByText("Optional")).toHaveLength(3);
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(screen.getByLabelText("Title")).toHaveFocus();
    expect(screen.getByLabelText("Title")).toHaveAccessibleDescription("Title is required");
  });

  it("adds a saved record to the list and resets the form", async () => {
    const user = userEvent.setup();
    const saved: Record<string, string>[] = [];
    render(<FormRenderer entity={demoNote} onSubmit={(values) => {
      saved.push(values);
    }} />);
    await user.type(screen.getByLabelText("Title"), "Kickoff");
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(saved).toEqual([{ title: "Kickoff" }]);
    expect(screen.getByLabelText("Title")).toHaveValue("");
    expect(within(screen.getByRole("table", { name: "Note list" })).getByRole("button", { name: "Kickoff" })).toBeInTheDocument();
  });

  it("keeps the typed values when onSubmit rejects the save", async () => {
    const user = userEvent.setup();
    render(<FormRenderer entity={demoNote} onSubmit={() => false} />);
    await user.type(screen.getByLabelText("Title"), "Kickoff");
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(screen.getByLabelText("Title")).toHaveValue("Kickoff");
    expect(screen.getByText("No rows")).toBeInTheDocument();
  });

  it("labels each line cell with its line number", async () => {
    const user = userEvent.setup();
    render(<FormRenderer entity={demoNote} />);
    await user.click(screen.getByRole("button", { name: "Add Line" }));
    await user.click(screen.getByRole("button", { name: "Add Line" }));
    expect(screen.getByLabelText("Qty, line 1")).toBeInTheDocument();
    expect(screen.getByLabelText("Qty, line 2")).toBeInTheDocument();
  });
});

describe("notes page", () => {
  it("saves a valid note into the list and confirms it", async () => {
    const user = userEvent.setup();
    renderShell("/notes");
    await user.type(screen.getByLabelText("Title"), "Kickoff");
    await user.click(screen.getByRole("button", { name: "Save Note" }));
    expect(within(screen.getByRole("table", { name: "Note list" })).getByRole("button", { name: "Kickoff" })).toBeInTheDocument();
    expect(screen.getAllByRole("status").some((node) => node.textContent === "Note saved")).toBe(true);
    expect(screen.getAllByRole("heading", { level: 2 })).toHaveLength(1);
  });
});

describe("shell feedback", () => {
  it("keeps the current density when applying the saved view, and shows a toast that hides itself", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    const { container } = renderShell();
    await user.click(screen.getByRole("button", { name: "Compact view" }));
    await user.click(screen.getByRole("button", { name: "Apply saved view" }));
    expect(container.firstElementChild).toHaveAttribute("data-density", "compact");
    expect(screen.getAllByText("Saved view applied")).toHaveLength(2);
    act(() => {
      vi.advanceTimersByTime(4000);
    });
    expect(screen.getAllByText("Saved view applied")).toHaveLength(1);
  });

  it("keeps search announcements in the live region only", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getAllByText("Search opened")).toHaveLength(1);
  });

  it("re-announces an identical message after clearing the live region", async () => {
    const frames: FrameRequestCallback[] = [];
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((callback) => frames.push(callback));
    const user = userEvent.setup();
    renderShell();
    await user.click(screen.getByRole("button", { name: "Apply saved view" }));
    expect(screen.getByRole("status")).toHaveTextContent("Saved view applied");
    await user.click(screen.getByRole("button", { name: "Apply saved view" }));
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
    act(() => frames.forEach((frame) => frame(0)));
    expect(screen.getByRole("status")).toHaveTextContent("Saved view applied");
  });

  it("shows the offline banner and announces the change", () => {
    renderShell();
    act(() => {
      window.dispatchEvent(new Event("offline"));
    });
    expect(screen.getByText(/You're offline\. Pages you've opened still work/)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("You're offline");
    act(() => {
      window.dispatchEvent(new Event("online"));
    });
    expect(screen.queryByText(/Pages you've opened still work/)).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("You're back online");
  });

  it("closes the drawer when the viewport grows past the drawer breakpoint", async () => {
    const user = userEvent.setup();
    let notify: ((event: { matches: boolean }) => void) | undefined;
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: (_type: string, listener: (event: { matches: boolean }) => void) => {
          notify = listener;
        },
        removeEventListener: () => {},
      })),
    );
    renderShell();
    await user.click(screen.getByRole("button", { name: "Open navigation" }));
    expect(screen.getByRole("dialog", { name: "Navigation" })).toBeInTheDocument();
    act(() => notify?.({ matches: true }));
    expect(screen.queryByRole("dialog", { name: "Navigation" })).not.toBeInTheDocument();
  });
});

describe("shell principal", () => {
  function DutiesProbe() {
    return <p>{`duties:${usePrincipalDuties().join("|")}`}</p>;
  }

  it("falls back to the dev duties when no duties prop is given", () => {
    vi.stubGlobal("__PASSAGE_DEV_DUTIES__", "org.read, org.hierarchy.write");
    render(
      <MemoryRouter>
        <Routes>
          <Route element={<Shell modules={modules} />}>
            <Route path="/" element={<DutiesProbe />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByText("duties:org.read|org.hierarchy.write")).toBeInTheDocument();
  });
});

describe("route error boundary", () => {
  it("shows retry and home actions, records the error, and recovers on retry", async () => {
    const user = userEvent.setup();
    vi.spyOn(console, "error").mockImplementation(() => {});
    let broken = true;
    function Page() {
      if (broken) {
        throw new Error("page exploded");
      }
      return <p>Recovered page</p>;
    }
    render(
      <MemoryRouter>
        <RouteErrorBoundary>
          <Page />
        </RouteErrorBoundary>
      </MemoryRouter>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("This page couldn't load");
    expect(screen.getByRole("link", { name: "Go to home" })).toHaveAttribute("href", "/");
    expect(recordedErrors().some((entry) => entry.message === "page exploded")).toBe(true);
    broken = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("Recovered page")).toBeInTheDocument();
  });
});

describe("duty gating", () => {
  const gated: PassageModule[] = [
    {
      id: "ledger",
      title: "Ledger",
      description: "Journals and balances.",
      routes: [],
      nav: [
        { label: "Journal batches", to: "/ledger/journal-batches", duty: "ledger.journal.read" },
        { label: "Open items", to: "/ledger/open-items" },
      ],
    },
    {
      id: "treasury",
      title: "Treasury",
      description: "Cash and loans.",
      routes: [],
      nav: [{ label: "Checks", to: "/treasury/checks", duty: "treasury.view" }],
    },
  ];

  function renderGated(path: string, duties: string[]) {
    return render(
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route element={<Shell modules={gated} duties={duties} />}>
            <Route path="/" element={<HomePage />} />
            <Route path="/ledger/journal-batches" element={<p>Journals page</p>} />
            <Route path="/ledger/journal-batches/:id" element={<p>Journal detail page</p>} />
            <Route path="/ledger/open-items" element={<p>Open items page</p>} />
            <Route path="*" element={<NotFoundPage />} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
  }

  it("hides gated entries and empty workspaces from the sidebar and home cards, keeping ungated entries", () => {
    renderGated("/", []);
    const nav = screen.getByRole("navigation", { name: "Workspaces" });
    expect(within(nav).queryByRole("link", { name: "Post a journal" })).not.toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Review open items" })).toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "Print and track checks" })).not.toBeInTheDocument();
    const cards = screen.getByRole("list", { name: "Workspaces" });
    expect(within(cards).queryByRole("link", { name: "Post a journal" })).not.toBeInTheDocument();
    expect(within(cards).getAllByRole("link", { name: "Review open items" }).length).toBeGreaterThan(0);
  });

  it("shows gated entries to a principal holding the duty", () => {
    renderGated("/", ["ledger.journal.read", "treasury.view"]);
    const nav = screen.getByRole("navigation", { name: "Workspaces" });
    expect(within(nav).getByRole("link", { name: "Post a journal" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Print and track checks" })).toBeInTheDocument();
  });

  it("leaves hidden entries out of search", async () => {
    const user = userEvent.setup();
    renderGated("/", []);
    await user.click(screen.getByRole("button", { name: "Search" }));
    const dialog = screen.getByRole("dialog", { name: "Search" });
    expect(within(dialog).queryByText("Post a journal")).not.toBeInTheDocument();
    expect(within(dialog).getByText("Review open items")).toBeInTheDocument();
  });

  it("renders the not-available page, not the route, for a direct URL to a gated page or its detail route", () => {
    const first = renderGated("/ledger/journal-batches", []);
    expect(screen.getByRole("heading", { name: "This page isn't available to you" })).toBeInTheDocument();
    expect(screen.queryByText("Journals page")).not.toBeInTheDocument();
    expect(document.title).toBe("Page not available · Passage");
    expect(pageViolations(first.container)).toEqual([]);
    first.unmount();
    renderGated("/ledger/journal-batches/42", []);
    expect(screen.getByRole("heading", { name: "This page isn't available to you" })).toBeInTheDocument();
    expect(screen.queryByText("Journal detail page")).not.toBeInTheDocument();
  });

  it("serves the gated route to a principal holding the duty and ungated routes to everyone", () => {
    const first = renderGated("/ledger/journal-batches/42", ["ledger.journal.read"]);
    expect(screen.getByText("Journal detail page")).toBeInTheDocument();
    first.unmount();
    renderGated("/ledger/open-items", []);
    expect(screen.getByText("Open items page")).toBeInTheDocument();
  });
});
