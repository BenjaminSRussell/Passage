import { render, screen } from "@testing-library/react";
import { loadModules } from "../registry";
import workflow from "./module";
import { InboxPage, JobMonitorPage, WorkflowHome } from "./pages";

describe("workflow module", () => {
  it("uses its folder name as id and declares workflow routes", () => {
    expect(workflow.id).toBe("workflow");
    expect(workflow.routes.length).toBeGreaterThan(0);
    expect(workflow.nav.every((n) => n.to.startsWith("/workflow"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("workflow");
  });

  it("shows workspace tiles", () => {
    render(<WorkflowHome />);
    expect(screen.getByRole("heading", { name: "Workflow and batch" })).toBeTruthy();
    expect(screen.getByRole("list", { name: "Workflow tiles" })).toHaveTextContent("Pending work items 4");
  });

  it("lists work items and batch runs", () => {
    render(<InboxPage />);
    expect(screen.getByRole("table", { name: "Work items" })).toHaveTextContent("PR-1042");
    render(<JobMonitorPage />);
    expect(screen.getByRole("table", { name: "Batch runs" })).toHaveTextContent("rows-1m");
  });

  it("flags overdue work items and shows readable statuses", () => {
    render(<InboxPage />);
    const table = screen.getByRole("table", { name: "Work items" });
    expect(table).toHaveTextContent("Overdue");
    expect(table).toHaveTextContent("Escalated");
  });

  it("explains empty inbox and job monitor", () => {
    render(<InboxPage items={[]} />);
    expect(screen.getByText("You're all caught up")).toBeInTheDocument();
    render(<JobMonitorPage runs={[]} />);
    expect(screen.getByText("No batch runs yet")).toBeInTheDocument();
  });
});
