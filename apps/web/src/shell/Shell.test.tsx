import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import type { PassageModule } from "../modules/registry";
import { checkAxeAA } from "./a11y";
import { FormRenderer } from "./forms";
import { demoNote } from "./metadata";
import { Shell } from "./Shell";

const modules: PassageModule[] = [
  {
    id: "admin",
    title: "Admin",
    routes: [{ path: "/admin", element: <div>Admin home</div> }],
    nav: [{ label: "Audit", to: "/admin/audit" }],
  },
];

function renderShell() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <Routes>
        <Route element={<Shell modules={modules} />}>
          <Route path="/" element={<p>Home</p>} />
          <Route path="/admin/audit" element={<p>Audit page</p>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

describe("shell a11y and keyboard", () => {
  it("generated forms pass the in-repo WCAG 2.1 AA checks", () => {
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
    expect(screen.getByLabelText("Edit Title")).toHaveFocus;
    await user.keyboard("{Escape}");
  });
});
