import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import type { PassageModule } from "../modules/registry";
import { checkAxeAA } from "./a11y";
import { FormRenderer } from "./forms";
import { demoNote } from "./metadata";
import { HomePage } from "./pages/HomePage";
import { NotesPage } from "./pages/NotesPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { Shell } from "./Shell";

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
          <Route path="/" element={<HomePage modules={modules} />} />
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
  it("lists a card and a link per nav entry on home, and keeps the note form on /notes", () => {
    const home = renderShell();
    const main = within(screen.getByRole("main"));
    expect(main.getByRole("heading", { level: 3, name: "Admin" })).toBeInTheDocument();
    expect(main.getByRole("link", { name: /Audit/ })).toHaveAttribute("href", "/admin/audit");
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
    expect(within(screen.getByRole("navigation", { name: "Workspaces" })).getByRole("link", { name: "Audit" })).toHaveAttribute(
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

  it("collapses and expands a workspace group", async () => {
    const user = userEvent.setup();
    renderShell();
    const nav = within(screen.getByRole("navigation", { name: "Workspaces" }));
    const header = nav.getByRole("button", { name: "Admin" });
    expect(header).toHaveAttribute("aria-expanded", "true");
    await user.click(header);
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(nav.queryByRole("link", { name: "Audit" })).not.toBeInTheDocument();
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
    const input = screen.getByRole("combobox");
    const second = screen.getAllByRole("option")[1];
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
    const options = screen.getAllByRole("option");
    expect(options[options.length - 1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowDown}");
    expect(options[0]).toHaveAttribute("aria-selected", "true");
  });

  it("groups options and shows a no-results state that ignores Enter", async () => {
    const user = userEvent.setup();
    renderShell();
    await user.keyboard("{Control>}k{/Control}");
    expect(within(screen.getByRole("group", { name: "Admin" })).getByRole("option", { name: /Audit/ })).toBeInTheDocument();
    await user.type(screen.getByRole("combobox"), "zzz");
    expect(screen.getByText(/No pages match/)).toBeInTheDocument();
    expect(screen.getByRole("combobox")).toHaveAttribute("aria-expanded", "false");
    await user.keyboard("{Enter}");
    expect(screen.getByRole("dialog", { name: "Search" })).toBeInTheDocument();
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
  });
});
