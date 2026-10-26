import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { loadModules } from "../registry";
import admin from "./module";
import { AdminHome, AlertInbox, AuditViewer, ImportExportPage } from "./pages";

describe("admin module", () => {
  it("uses its folder name as id and declares admin routes", () => {
    expect(admin.id).toBe("admin");
    expect(admin.routes.length).toBeGreaterThan(0);
    expect(admin.nav.every((n) => n.to.startsWith("/admin"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("admin");
  });

  it("shows user, time, old/new and trace id in the audit viewer", () => {
    render(
      <AuditViewer
        items={[
          {
            field: "name",
            userId: "user-1",
            occurredAt: "2026-10-24T12:00:00Z",
            oldValue: "Acme",
            newValue: "Acme Ltd",
            traceId: "trace-abc",
          },
        ]}
      />,
    );
    expect(screen.getByRole("table", { name: "Audit entries" })).toHaveTextContent("user-1");
    expect(screen.getByRole("table", { name: "Audit entries" })).toHaveTextContent("Acme Ltd");
    expect(screen.getByRole("table", { name: "Audit entries" })).toHaveTextContent("trace-abc");
    expect(screen.getByRole("table", { name: "Audit entries" })).toHaveTextContent("2026-10-24T12:00:00Z");
  });

  it("explains an empty audit trail", () => {
    render(<AuditViewer />);
    expect(screen.getByText("No audit entries yet")).toBeInTheDocument();
  });

  it("explains an empty alert inbox and lists alerts when present", () => {
    const { unmount } = render(<AlertInbox />);
    expect(screen.getByText("No alerts right now")).toBeInTheDocument();
    unmount();
    render(<AlertInbox items={[{ message: "Batch outbox.sweep failed" }]} />);
    expect(screen.getByText("Batch outbox.sweep failed")).toBeInTheDocument();
  });

  it("describes what each CSV field expects", () => {
    render(<ImportExportPage />);
    expect(screen.getByLabelText("CSV data")).toHaveAccessibleDescription(/first row names the columns/);
  });

  it("validates pasted CSV on the client and reports the row count", () => {
    render(<ImportExportPage />);
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("status")).toHaveTextContent("1 row looks valid");
    fireEvent.change(screen.getByLabelText("CSV data"), { target: { value: "a,b\n1,2\n3,4" } });
    expect(screen.getByRole("status")).toBeEmptyDOMElement();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("status")).toHaveTextContent("2 rows look valid");
  });

  it("flags a ragged CSV row and an empty paste", () => {
    render(<ImportExportPage />);
    fireEvent.change(screen.getByLabelText("CSV data"), { target: { value: "a,b,c\n1,2,3\n4,5" } });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Row 3 has 2 columns, expected 3");
    fireEvent.change(screen.getByLabelText("CSV data"), { target: { value: "a,b" } });
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(screen.getByRole("alert")).toHaveTextContent("Add a header row and at least one data row");
  });

  it("links each admin card to its page", () => {
    render(
      <MemoryRouter>
        <AdminHome />
      </MemoryRouter>,
    );
    expect(screen.getByRole("link", { name: "Audit" })).toHaveAttribute("href", "/admin/audit");
  });
});
