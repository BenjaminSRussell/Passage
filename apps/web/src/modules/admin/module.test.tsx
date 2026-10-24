import { render, screen } from "@testing-library/react";
import { loadModules } from "../registry";
import admin from "./module";
import { AuditViewer } from "./pages";

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
});
