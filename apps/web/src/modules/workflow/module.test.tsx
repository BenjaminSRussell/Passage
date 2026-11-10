import { render, screen } from "@testing-library/react";
import { loadModules } from "../registry";
import workflow from "./module";
import { MemoryRouter } from "react-router-dom";
import { JobMonitorPage, WorkflowHome } from "./pages";

describe("workflow module", () => {
  it("uses its folder name as id and declares workflow routes", () => {
    expect(workflow.id).toBe("workflow");
    expect(workflow.routes.length).toBeGreaterThan(0);
    expect(workflow.nav.every((n) => n.to.startsWith("/workflow"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("workflow");
  });

  it("shows workspace tiles and links to My work", () => {
    render(
      <MemoryRouter>
        <WorkflowHome />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "Workflow and batch" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "Workflow tiles" })).toHaveTextContent("Running jobs 1");
    expect(screen.getByRole("link", { name: "Open My work" })).toHaveAttribute("href", "/workflow/inbox");
  });

  it("lists batch runs", () => {
    render(<JobMonitorPage />);
    expect(screen.getByRole("table", { name: "Batch runs" })).toHaveTextContent("rows-1m");
  });

  it("explains an empty job monitor", () => {
    render(<JobMonitorPage runs={[]} />);
    expect(screen.getByText("No batch runs yet")).toBeInTheDocument();
  });
});
