import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../../shell/testing/fakeServer";
import admin from "../module";

useFakeServer();

const U1 = "11111111-1111-4111-8111-111111111111";
const U2 = "55555555-5555-4555-8555-555555555555";
const ENTITY = "22222222-2222-4222-8222-222222222222";

function renderAt(path: string) {
  return renderRoutes(
    admin.routes.map((route) => ({
      path: route.path,
      element: createElement(PrincipalProvider, {
        duties: [],
        children: route.element,
      }),
    })),
    path,
  );
}

const user = (userId: string, displayName: string, status = "active") => ({
  userId,
  idpSubject: `sub-${displayName}`,
  email: `${displayName.toLowerCase()}@example.com`,
  displayName,
  status,
  createdAt: "2026-01-01T00:00:00Z",
});

const assignment = {
  assignmentId: "33333333-3333-4333-8333-333333333333",
  userId: U1,
  role: "Maintain users",
  validFrom: "2026-01-01T00:00:00Z",
  createdAt: "2026-01-01T00:00:00Z",
};

describe("users page", () => {
  it("lists users, then searches and filters with the typed query and pages by cursor", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/legal-entities") {
        return json(200, {
          items: [
            {
              legalEntityId: ENTITY,
              code: "DE01",
              displayName: "Germany",
              status: "active",
              createdAt: "2026-01-01T00:00:00Z",
            },
          ],
        });
      }
      if (url.pathname !== "/api/v1/identity/users") return undefined;
      if (url.searchParams.get("after") === U2)
        return json(200, {
          items: [user("66666666-6666-4666-8666-666666666666", "Cleo")],
        });
      if (url.searchParams.get("q")) return json(200, { items: [user(U1, "Ada")] });
      return json(200, {
        items: [user(U1, "Ada"), user(U2, "Bob", "deprovisioned")],
        next: U2,
      });
    };
    const { container } = renderAt("/admin/security/users");
    const table = await screen.findByRole("table", { name: "Users" });
    expect(await within(table).findByRole("link", { name: "Ada" })).toHaveAttribute("href", `/admin/security/users/${U1}`);
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await within(table).findByText("Cleo")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.searchParams.get("after") === U2)).toBe(true);

    await screen.findByRole("option", { name: "DE01 - Germany" });
    fireEvent.change(screen.getByLabelText("Search"), {
      target: { value: " ada " },
    });
    fireEvent.change(screen.getByLabelText("Status"), {
      target: { value: "active" },
    });
    fireEvent.change(screen.getByLabelText("Legal entity"), {
      target: { value: ENTITY },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search users" }));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Users" })).queryByText("Bob")).toBeNull());
    const last = server.calls.filter((c) => c.url.pathname === "/api/v1/identity/users").at(-1)!;
    expect(last.url.search).toBe(`?q=ada&status=active&legalEntityId=${ENTITY}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when a filter matches nothing", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/identity/users" ? json(200, { items: [] }) : json(403, { code: "IAM-0403", detail: "no" }));
    renderAt("/admin/security/users");
    expect(await screen.findByText("No users yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search"), {
      target: { value: "zzz" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search users" }));
    expect(await screen.findByText("No users match")).toBeInTheDocument();
  });

  it("shows the server reason with no retry on 403 and offers retry on 500", async () => {
    server.handler = () => json(403, { code: "IAM-0403", detail: "needs Maintain users" });
    const first = renderAt("/admin/security/users");
    expect(await screen.findByText("needs Maintain users")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    const { container } = renderAt("/admin/security/users");
    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("user detail page", () => {
  it("shows the profile, active assignments and a link to the effective permissions", async () => {
    server.handler = (url) => {
      if (url.pathname === `/api/v1/identity/users/${U1}`) return json(200, user(U1, "Ada"));
      if (url.pathname === "/api/v1/authz/assignments") return json(200, { items: [assignment] });
      return undefined;
    };
    const { container } = renderAt(`/admin/security/users/${U1}`);
    expect(await screen.findByRole("heading", { name: "Ada" })).toBeInTheDocument();
    const table = await screen.findByRole("table", {
      name: "User role assignments",
    });
    expect(within(table).getByText("Maintain users")).toBeInTheDocument();
    expect(within(table).getByText("Tenant-wide")).toBeInTheDocument();
    expect(server.calls.find((c) => c.url.pathname === "/api/v1/authz/assignments")!.url.search).toBe(`?userId=${U1}&active=true`);
    expect(screen.getByRole("link", { name: "View effective permissions" })).toHaveAttribute("href", `/admin/security/permissions?userId=${U1}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports an unknown user without loading assignments", async () => {
    server.handler = () => json(404, { code: "IDN-USER-NOT-FOUND", detail: "user not found" });
    renderAt(`/admin/security/users/${U1}`);
    expect(await screen.findByText("User not found")).toBeInTheDocument();
    expect(server.calls.every((c) => c.url.pathname !== "/api/v1/authz/assignments")).toBe(true);
  });

  it("keeps the profile when assignments are forbidden", async () => {
    server.handler = (url) =>
      url.pathname === `/api/v1/identity/users/${U1}`
        ? json(200, user(U1, "Ada"))
        : json(403, {
            code: "IAM-0403",
            detail: "needs Maintain security roles",
          });
    renderAt(`/admin/security/users/${U1}`);
    expect(await screen.findByText("needs Maintain security roles")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Ada" })).toBeInTheDocument();
  });
});

describe("effective permissions deep link", () => {
  it("prefills and runs the report for a user id in the query string", async () => {
    server.handler = (url) =>
      url.pathname === `/api/v1/identity/users/${U1}/effective-permissions`
        ? json(200, {
            userId: U1,
            duties: ["Maintain users"],
            ignoredRoles: [],
            permissions: [],
          })
        : undefined;
    renderAt(`/admin/security/permissions?userId=${U1}`);
    expect(await screen.findByRole("list", { name: "Duties in effect" })).toBeInTheDocument();
    expect(screen.getByLabelText("User id")).toHaveValue(U1);
  });

  it("ignores a malformed user id in the query string", () => {
    renderAt("/admin/security/permissions?userId=nope");
    expect(screen.getByLabelText("User id")).toHaveValue("");
    expect(server.calls).toHaveLength(0);
  });
});

const roles = [
  { name: "Maintain users", duties: ["Maintain users"] },
  { name: "Maintain security roles", duties: ["Maintain security roles"] },
];
const duties = [
  { name: "Maintain users", serviceOnly: false, permissionCount: 3 },
  { name: "Internal sync", serviceOnly: true, permissionCount: 1 },
];

describe("role catalogue page", () => {
  it("lists roles and duties and filters by name and duty kind", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/authz/roles")
        return json(200, {
          items: url.searchParams.get("q") ? [roles[0]] : roles,
        });
      if (url.pathname === "/api/v1/authz/duties")
        return json(200, {
          items: url.searchParams.get("serviceOnly") === "true" ? [duties[1]] : duties,
        });
      return undefined;
    };
    const { container } = renderAt("/admin/security/roles");
    const roleLink = await within(await screen.findByRole("table", { name: "Roles" })).findByRole("link", { name: "Maintain security roles" });
    expect(roleLink).toHaveAttribute("href", "/admin/security/roles/Maintain%20security%20roles");
    expect(await within(await screen.findByRole("table", { name: "Duties" })).findByText("Service only")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Duty kind"), {
      target: { value: "service" },
    });
    fireEvent.change(screen.getByLabelText("Search"), {
      target: { value: "users" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Roles" })).queryByText("Maintain security roles")).toBeNull());
    await waitFor(() => expect(within(screen.getByRole("table", { name: "Duties" })).queryByText("Maintain users")).toBeNull());
    const last = server.calls.filter((c) => c.url.pathname === "/api/v1/authz/duties").at(-1)!;
    expect(last.url.search).toBe("?q=users&serviceOnly=true");
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends serviceOnly=false for person duties", async () => {
    server.handler = (url) => json(200, { items: url.pathname.endsWith("/roles") ? roles : duties });
    renderAt("/admin/security/roles");
    await screen.findByRole("table", { name: "Duties" });
    fireEvent.change(screen.getByLabelText("Duty kind"), {
      target: { value: "person" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    await waitFor(() => expect(server.calls.some((c) => c.url.search === "?serviceOnly=false")).toBe(true));
  });

  it("shows empty states and the server reason with no retry on 403", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/admin/security/roles");
    expect(await screen.findByText("No roles yet")).toBeInTheDocument();
    expect(await screen.findByText("No duties match")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "IAM-0403", detail: "needs Maintain security roles" });
    const { container } = renderAt("/admin/security/roles");
    expect((await screen.findAllByText("needs Maintain security roles")).length).toBe(2);
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("role detail page", () => {
  it("shows each duty with the routes it grants", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/authz/roles/Maintain%20users"
        ? json(200, {
            name: "Maintain users",
            duties: [
              {
                name: "Maintain users",
                serviceOnly: false,
                permissions: [
                  {
                    resource: "/v1/identity/users{,/**}",
                    methods: ["GET", "POST"],
                  },
                ],
              },
            ],
          })
        : undefined;
    const { container } = renderAt("/admin/security/roles/Maintain%20users");
    const table = await screen.findByRole("table", {
      name: "Routes granted by Maintain users",
    });
    expect(within(table).getByText("GET, POST")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("reports an unknown role and says when a role holds no duties", async () => {
    server.handler = () => json(404, { code: "IDN-ROLE-NOT-FOUND", detail: "role not found" });
    const first = renderAt("/admin/security/roles/Nope");
    expect(await screen.findByText("Role not found")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(200, { name: "Empty", duties: [] });
    renderAt("/admin/security/roles/Empty");
    expect(await screen.findByText("This role holds no duties.")).toBeInTheDocument();
  });
});
