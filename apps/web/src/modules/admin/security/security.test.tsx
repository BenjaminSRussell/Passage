import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { createElement } from "react";
import { describe, expect, it } from "vitest";
import { visibleNav } from "../../../shell/dutyGate";
import { PrincipalProvider } from "../../../shell/principal";
import { json, pageViolations, renderRoutes, server as fake, useFakeServer, type Reply } from "../../../shell/testing/fakeServer";
import admin from "../module";

useFakeServer();

const USER = "11111111-1111-4111-8111-111111111111";
const ENTITY = "22222222-2222-4222-8222-222222222222";
const A1 = "33333333-3333-4333-8333-333333333333";
const A2 = "44444444-4444-4444-8444-444444444444";

const server = {
  get calls() {
    return fake.calls;
  },
  set handler(h: (url: URL, method: string) => Reply | undefined) {
    fake.handler = (url, init) => h(url, init.method ?? "GET");
  },
};

function renderAt(path: string, duties: string[] = []) {
  return renderRoutes(
    admin.routes.map((route) => ({ path: route.path, element: createElement(PrincipalProvider, { duties, children: route.element }) })),
    path,
  );
}

async function fill(label: string, value: string) {
  fireEvent.change(await screen.findByLabelText(label), { target: { value } });
}

const report = {
  userId: USER,
  duties: ["identity.security-role.maintain"],
  ignoredRoles: ["Legacy role"],
  permissions: [
    { resource: "/v1/authz/assignments", method: "POST", allow: true, duty: "identity.security-role.maintain" },
    { resource: "/v1/fx/rates", method: "POST", allow: false, dutyMissing: "fx.rate.maintain" },
    { resource: "/v1/fx/rates", method: "GET", allow: false },
  ],
};

describe("admin security nav", () => {
  it("gates each security entry on its duty", () => {
    const duties = Object.fromEntries(admin.nav.filter((n) => n.to.startsWith("/admin/security")).map((n) => [n.to, n.duty]));
    expect(duties).toEqual({
      "/admin/security/permissions": "identity.effective-permission.view",
      "/admin/security/assignments": "identity.security-role.maintain",
      "/admin/security/legal-entities": "identity.legal-entity.maintain",
      "/admin/security/users": "identity.user.maintain",
      "/admin/security/roles": "identity.security-role.maintain",
    });
    const shown = (held: string[]) => visibleNav(admin.nav, held).map((n) => n.to).filter((to) => to.startsWith("/admin/security"));
    expect(shown([])).toEqual([]);
    expect(shown(["identity.security-role.maintain"])).toEqual(["/admin/security/assignments", "/admin/security/roles"]);
    expect(shown(["identity.user.maintain"])).toEqual(["/admin/security/users"]);
  });
});

describe("effective permissions page", () => {
  it("rejects a malformed user id without calling the API", async () => {
    renderAt("/admin/security/permissions");
    await fill("User id", "nope");
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    expect(await screen.findByText("Enter the user id as a UUID.")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("shows duties, ignored roles and allowed or denied routes, and filters them", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/identity/users/${USER}/effective-permissions` ? json(200, report) : undefined);
    const { container } = renderAt("/admin/security/permissions");
    await fill("User id", ` ${USER} `);
    await fill("Legal entity id", ENTITY);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    const table = await screen.findByRole("table", { name: "Route permissions" });
    expect(server.calls[0].url.search).toBe(`?legalEntityId=${ENTITY}`);
    expect(within(screen.getByRole("list", { name: "Duties in effect" })).getByText("identity.security-role.maintain")).toBeInTheDocument();
    expect(screen.getByText(/Legacy role/)).toBeInTheDocument();
    expect(within(table).getByText("Missing: fx.rate.maintain")).toBeInTheDocument();
    expect(within(table).getByText("No duty grants this route")).toBeInTheDocument();
    expect(screen.getByText("Showing 3 of 3 routes.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Decision"), { target: { value: "denied" } });
    fireEvent.change(screen.getByLabelText("Method"), { target: { value: "POST" } });
    expect(screen.getByText("Showing 1 of 3 routes.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Search routes"), { target: { value: "zzz" } });
    expect(screen.getByText("No routes match")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the server reason first, then the scope hint, with no retry on 403", async () => {
    server.handler = () => json(403, { code: "IAM-0403", detail: "legal-entity scope must be all entities" });
    const { container } = renderAt("/admin/security/permissions");
    await fill("User id", USER);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    const alert = await screen.findByText("legal-entity scope must be all entities");
    expect(alert.parentElement).toHaveTextContent(/scope of all entities/);
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("announces loading in an already mounted live region", async () => {
    server.handler = () => json(200, report);
    renderAt("/admin/security/permissions");
    const region = document.querySelector<HTMLElement>('p[role="status"]')!;
    expect(region).toBeEmptyDOMElement();
    await fill("User id", USER);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    await waitFor(() => expect(region).toHaveTextContent("Loading permissions…"));
    await screen.findByRole("table", { name: "Route permissions" });
  });

  it("reports an unknown user and offers retry on a server error", async () => {
    server.handler = () => json(404, { code: "IDN-USER-NOT-FOUND", detail: "user not found" });
    const first = renderAt("/admin/security/permissions");
    await fill("User id", USER);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    expect(await screen.findByText("User not found")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderAt("/admin/security/permissions");
    await fill("User id", USER);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("says when the user holds no duties", async () => {
    server.handler = () => json(200, { userId: USER, duties: [], ignoredRoles: [], permissions: [] });
    renderAt("/admin/security/permissions");
    await fill("User id", USER);
    fireEvent.click(screen.getByRole("button", { name: "Show permissions" }));
    expect(await screen.findByText("This user holds no duties right now.")).toBeInTheDocument();
  });
});

const future = "2999-01-01T00:00:00Z";
const assignments = [
  { assignmentId: A1, userId: USER, role: "identity.security-role.maintain", validFrom: "2026-01-01T00:00:00Z", createdAt: "2026-01-01T00:00:00Z", grantedBy: "admin", reason: "cover" },
  { assignmentId: A2, userId: USER, role: "fx.rate.view", legalEntityId: ENTITY, validFrom: "2026-01-01T00:00:00Z", validTo: "2026-02-01T00:00:00Z", createdAt: "2026-01-01T00:00:00Z" },
];

function assignmentsServer(extra?: (url: URL, method: string) => ReturnType<typeof json> | undefined) {
  server.handler = (url, method) => {
    if (url.pathname === "/api/v1/legal-entities") return json(200, { items: [{ legalEntityId: ENTITY, code: "DE01", displayName: "Germany", status: "active", createdAt: "2026-01-01T00:00:00Z" }] });
    if (url.pathname === "/api/v1/authz/assignments" && method === "GET") {
      return url.searchParams.get("after") ? json(200, { items: [{ ...assignments[1], assignmentId: "x", role: "Page two role" }] }) : json(200, { items: assignments, next: A2 });
    }
    return extra?.(url, method);
  };
}

async function find(userId: string) {
  await fill("Find by user id", userId);
  fireEvent.click(screen.getByRole("button", { name: "Find assignments" }));
}

describe("role assignments page", () => {
  it("requires a user or a role before listing", async () => {
    assignmentsServer();
    renderAt("/admin/security/assignments");
    fireEvent.click(screen.getByRole("button", { name: "Find assignments" }));
    expect(await screen.findByText("Enter a user id or a role to look up.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.url.pathname === "/api/v1/authz/assignments")).toBe(false);
  });

  it("lists by user with active filter, shows status and loads more by cursor", async () => {
    assignmentsServer();
    const { container } = renderAt("/admin/security/assignments");
    await find(USER);
    const table = await screen.findByRole("table", { name: "Role assignments" });
    expect(await within(table).findByText("identity.security-role.maintain")).toBeInTheDocument();
    const list = server.calls.find((c) => c.url.pathname === "/api/v1/authz/assignments");
    expect(list?.url.search).toBe(`?userId=${USER}&active=true`);
    expect(within(table).getByText("Tenant-wide")).toBeInTheDocument();
    expect(within(table).getAllByText("Active")).toHaveLength(1);
    expect(within(table).getByText("Ended")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await within(table).findByText("Page two role")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname === "/api/v1/authz/assignments").pop()?.url.searchParams.get("after")).toBe(A2);
    expect(pageViolations(container)).toEqual([]);
  });

  it("lists by role without an active filter", async () => {
    assignmentsServer();
    renderAt("/admin/security/assignments");
    await fill("Find by role", "fx.rate.view");
    fireEvent.click(screen.getByLabelText("Only assignments in effect now"));
    fireEvent.click(screen.getByRole("button", { name: "Find assignments" }));
    await screen.findByRole("table", { name: "Role assignments" });
    expect(server.calls.find((c) => c.url.pathname === "/api/v1/authz/assignments")?.url.search).toBe("?role=fx.rate.view");
  });

  it("ends an assignment only after confirmation and can be cancelled", async () => {
    assignmentsServer((url, method) => (url.pathname === `/api/v1/authz/assignments/${A1}/end` && method === "POST" ? json(200, { ...assignments[0], validTo: "2026-03-01T00:00:00Z" }) : undefined));
    renderAt("/admin/security/assignments");
    await find(USER);
    const endButton = await screen.findByRole("button", { name: `End identity.security-role.maintain for user ${USER}` });
    fireEvent.click(endButton);
    const dialog = screen.getByRole("alertdialog", { name: "End this assignment?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep assignment" }));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(endButton);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "End assignment" }));
    expect(await screen.findByText("identity.security-role.maintain ended")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST" && c.url.pathname.endsWith("/end"))).toBe(true);
  });

  it("offers cancel for a scheduled assignment and no action for an ended one", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/authz/assignments" ? json(200, { items: [{ ...assignments[0], validFrom: future }, assignments[1]] }) : json(200, { items: [] });
    renderAt("/admin/security/assignments");
    await find(USER);
    await screen.findByText("Scheduled");
    expect(screen.getAllByRole("button", { name: /^(End|Cancel) / })).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: `Cancel identity.security-role.maintain for user ${USER}` }));
    expect(screen.getByRole("alertdialog", { name: "Cancel this assignment?" })).toBeInTheDocument();
  });

  it("surfaces an already-ended conflict from the end call", async () => {
    assignmentsServer((url) => (url.pathname.endsWith("/end") ? json(409, { code: "IDN-ASSIGNMENT-ENDED", detail: "ended" }) : undefined));
    renderAt("/admin/security/assignments");
    await find(USER);
    fireEvent.click(await screen.findByRole("button", { name: `End identity.security-role.maintain for user ${USER}` }));
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "End assignment" }));
    expect(await screen.findByText("This assignment has already ended")).toBeInTheDocument();
  });

  it("shows an error with retry and an empty state", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/authz/assignments" ? json(500, { code: "COM-0500", detail: "down" }) : json(200, { items: [] }));
    renderAt("/admin/security/assignments");
    await find(USER);
    expect(await screen.findByText("down")).toBeInTheDocument();
    server.handler = () => json(200, { items: [] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No assignments found")).toBeInTheDocument();
  });

  it("validates the assignment form before submitting", async () => {
    assignmentsServer();
    renderAt("/admin/security/assignments");
    await fill("User to assign", "bad");
    await fill("Valid from", "2030-01-02T10:00");
    await fill("Valid to", "2030-01-01T10:00");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("Enter the role to assign.")).toBeInTheDocument();
    expect(screen.getByText("Enter the user id as a UUID.")).toBeInTheDocument();
    expect(screen.getByText("The end must be after the start.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("assigns a role with scope, dates and reason", async () => {
    assignmentsServer((url, method) =>
      url.pathname === "/api/v1/authz/assignments" && method === "POST" ? json(201, { ...assignments[0], role: "Approve invoices" }) : undefined,
    );
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", " Approve invoices ");
    await fill("Legal entity", ENTITY);
    await fill("Valid from", "2030-01-01T10:00");
    await fill("Reason", "Cover leave");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("Approve invoices assigned")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ userId: USER, role: "Approve invoices", legalEntityId: ENTITY, reason: "Cover leave", validFrom: new Date("2030-01-01T10:00").toISOString() });
    expect(post?.body).not.toHaveProperty("validTo");
    expect(screen.getByLabelText("Role to assign")).toHaveValue("");
  });

  it("explains a self-grant refusal", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(403, { code: "IAM-0416", detail: "self" }) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "identity.security-role.maintain");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("You cannot grant a role to yourself")).toBeInTheDocument();
  });

  it("explains an overlap conflict and an inactive legal entity", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(409, { code: "IDN-ASSIGNMENT-OVERLAP", detail: "overlap" }) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "identity.security-role.maintain");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("This overlaps an existing assignment")).toBeInTheDocument();
    assignmentsServer((_url, method) => (method === "POST" ? json(422, { code: "IDN-LEGAL-ENTITY-INACTIVE", detail: "inactive" }) : undefined));
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "retry" } });
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("That legal entity is inactive")).toBeInTheDocument();
  });

  it("maps server field errors onto the form fields", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(400, { code: "COM-0400", detail: "bad", errors: { validFrom: "Too far in the past." } }) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "identity.security-role.maintain");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("Too far in the past.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText("Valid from")).toHaveFocus());
  });
});

const entityPage = { items: [{ legalEntityId: ENTITY, code: "DE01", displayName: "Germany", status: "active", createdAt: "2026-01-01T00:00:00Z" }], next: A1 };

describe("legal entities page", () => {
  it("lists entities and loads more by cursor", async () => {
    server.handler = (url) =>
      url.pathname === "/api/v1/legal-entities"
        ? url.searchParams.get("after")
          ? json(200, { items: [{ ...entityPage.items[0], legalEntityId: A2, code: "FR01", displayName: "France", status: "inactive" }] })
          : json(200, entityPage)
        : undefined;
    const { container } = renderAt("/admin/security/legal-entities");
    const table = await screen.findByRole("table", { name: "Legal entities" });
    expect(await within(table).findByText("Germany")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(await within(table).findByText("France")).toBeInTheDocument();
    expect(within(table).getByText("Inactive")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("hides create without the duty and explains a failed list", async () => {
    server.handler = () => json(403, { code: "IAM-0403", detail: "no" });
    renderAt("/admin/security/legal-entities");
    expect(await screen.findByText("You do not have permission")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "New legal entity" })).toBeNull();
  });

  it("shows an empty state", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/admin/security/legal-entities");
    expect(await screen.findByText("No legal entities yet")).toBeInTheDocument();
  });

  it("validates and creates a legal entity when the duty is held", async () => {
    server.handler = (url, method) => {
      if (method === "POST") return json(201, { ...entityPage.items[0], code: "NL01", displayName: "Netherlands" });
      return url.pathname === "/api/v1/legal-entities" ? json(200, { items: [] }) : undefined;
    };
    const { container } = renderAt("/admin/security/legal-entities", ["identity.legal-entity.maintain"]);
    fireEvent.click(await screen.findByRole("button", { name: "Create legal entity" }));
    expect(await screen.findByText("Enter a name.")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
    await fill("Code", " NL01 ");
    await fill("Name", "Netherlands");
    fireEvent.click(screen.getByRole("button", { name: "Create legal entity" }));
    expect(await screen.findByText("Legal entity NL01 created")).toBeInTheDocument();
    expect(server.calls.find((c) => c.method === "POST")?.body).toEqual({ code: "NL01", displayName: "Netherlands" });
    expect(pageViolations(container)).toEqual([]);
  });

  it("counts the name in trimmed UTF-8 bytes", async () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/admin/security/legal-entities", ["identity.legal-entity.maintain"]);
    await fill("Code", "DE01");
    await fill("Name", "é".repeat(101));
    fireEvent.click(await screen.findByRole("button", { name: "Create legal entity" }));
    expect(await screen.findByText(/200 bytes or fewer/)).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("reports a duplicate code", async () => {
    server.handler = (_url, method) => (method === "POST" ? json(409, { code: "IDN-LEGAL-ENTITY-EXISTS", detail: "exists" }) : json(200, { items: [] }));
    renderAt("/admin/security/legal-entities", ["identity.legal-entity.maintain"]);
    await fill("Code", "DE01");
    await fill("Name", "Germany");
    fireEvent.click(screen.getByRole("button", { name: "Create legal entity" }));
    expect(await screen.findByText("That code is already in use")).toBeInTheDocument();
  });
});

describe("role assignments follow-ups", () => {
  const cancelled = { ...assignments[0], validFrom: future, validTo: future };

  it("treats a cancelled future assignment as ended with no action", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/authz/assignments" ? json(200, { items: [cancelled] }) : json(200, { items: [] }));
    renderAt("/admin/security/assignments");
    await find(USER);
    await screen.findByText("Ended");
    expect(screen.queryByText("Scheduled")).toBeNull();
    expect(screen.queryByRole("button", { name: /^(End|Cancel) / })).toBeNull();
  });

  it("shows the server reason before the hint for a tenant-wide scope 403", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(403, { code: "IAM-0403", detail: "a tenant-wide assignment needs access to all legal entities" }) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "identity.security-role.maintain");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    const reason = await screen.findByText("a tenant-wide assignment needs access to all legal entities");
    expect(reason.parentElement).toHaveTextContent(/identity.security-role.maintain" duty or an all-entities scope/);
  });

  it("shows the server reason for an operator-only 403", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(403, { code: "IAM-0403", detail: "identity.tenant.maintain can only be granted in the operator tenant" }) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "identity.tenant.maintain");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText("identity.tenant.maintain can only be granted in the operator tenant")).toBeInTheDocument();
  });

  it("refetches the list after assigning and after ending and moves focus to the results heading", async () => {
    assignmentsServer((url, method) => {
      if (method === "POST" && url.pathname.endsWith("/end")) return json(200, { ...assignments[0], validTo: "2026-03-01T00:00:00Z" });
      if (method === "POST") return json(201, assignments[0]);
      return undefined;
    });
    const listCalls = () => server.calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/v1/authz/assignments" && !c.url.searchParams.get("after")).length;
    renderAt("/admin/security/assignments");
    await find(USER);
    const endButton = await screen.findByRole("button", { name: `End identity.security-role.maintain for user ${USER}` });
    expect(listCalls()).toBe(1);
    await fill("User to assign", USER);
    await fill("Role to assign", "Approve invoices");
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    await screen.findByText("identity.security-role.maintain assigned");
    await waitFor(() => expect(listCalls()).toBe(2));
    fireEvent.click(endButton);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "End assignment" }));
    await screen.findByText("identity.security-role.maintain ended");
    await waitFor(() => expect(listCalls()).toBe(3));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Assignments" })).toHaveFocus());
  });

  it("disables the row action while the end request is in flight", async () => {
    let finish: (r: Reply) => void = () => undefined;
    const pending = new Promise<Reply>((resolve) => {
      finish = resolve;
    });
    assignmentsServer();
    renderAt("/admin/security/assignments");
    await find(USER);
    const endButton = await screen.findByRole("button", { name: `End identity.security-role.maintain for user ${USER}` });
    const stubbed = globalThis.fetch;
    globalThis.fetch = (async (input: string, init?: RequestInit) =>
      init?.method === "POST" ? pending.then((r) => new Response(JSON.stringify(r.body), { status: r.status })) : stubbed(input, init)) as typeof fetch;
    fireEvent.click(endButton);
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "End assignment" }));
    await waitFor(() => expect(endButton).toBeDisabled());
    finish(json(200, { ...assignments[0], validTo: "2026-03-01T00:00:00Z" }));
    await screen.findByText("identity.security-role.maintain ended");
    globalThis.fetch = stubbed;
  });

  it("trims the reason and counts bytes like the server", async () => {
    assignmentsServer((_url, method) => (method === "POST" ? json(201, assignments[0]) : undefined));
    renderAt("/admin/security/assignments");
    await fill("User to assign", USER);
    await fill("Role to assign", "Approve invoices");
    await fill("Reason", `  ${"a".repeat(500)}  `);
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    await screen.findByText("identity.security-role.maintain assigned");
    expect(server.calls.find((c) => c.method === "POST")?.body).toMatchObject({ reason: "a".repeat(500) });
    await fill("Reason", "é".repeat(251));
    fireEvent.click(screen.getByRole("button", { name: "Assign role" }));
    expect(await screen.findByText(/500 bytes or fewer/)).toBeInTheDocument();
  });

  it("is axe clean with the dialog open and in the error state", async () => {
    assignmentsServer();
    const { container } = renderAt("/admin/security/assignments");
    await find(USER);
    fireEvent.click(await screen.findByRole("button", { name: `End identity.security-role.maintain for user ${USER}` }));
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Keep assignment" }));
    server.handler = () => json(500, { code: "COM-0500", detail: "down" });
    fireEvent.change(screen.getByLabelText("Find by role"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Find assignments" }));
    expect(await screen.findByText("down")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers no retry when the assignment list is forbidden", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/authz/assignments" ? json(403, { code: "IAM-0403", detail: "denied" }) : json(200, { items: [] }));
    renderAt("/admin/security/assignments");
    await find(USER);
    expect(await screen.findByText("denied")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });
});

describe("legal entities follow-ups", () => {
  it("shows the server reason and no retry on a forbidden list", async () => {
    server.handler = () => json(403, { code: "IAM-0403", detail: "needs all entities" });
    renderAt("/admin/security/legal-entities");
    expect(await screen.findByText("needs all entities")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });

  it("assignment form stays usable when the entity suggestions are forbidden", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/legal-entities" ? json(403, { code: "IAM-0403", detail: "no" }) : undefined);
    renderAt("/admin/security/assignments");
    await waitFor(() => expect(screen.getByLabelText("Legal entity")).toHaveAccessibleDescription(/paste any other id/));
  });
});
