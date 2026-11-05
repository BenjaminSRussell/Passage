import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import hr from "./module";

const REP = "0195f0a0-0000-7000-8000-000000000001";
const OUTSIDER = "0195f0a0-0000-7000-8000-000000000002";

type Reply = { status: number; body: unknown };
let handler: (url: URL) => Reply | undefined;
let paths: string[];

const shellRules = new Set([
  "page-has-heading-one",
  "landmark-unique",
  "region",
  "bypass",
]);
const pageViolations = (container: HTMLElement) =>
  checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  paths = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      const url = new URL(input, "http://localhost");
      paths.push(url.pathname + url.search);
      const reply = handler(url) ?? {
        status: 404,
        body: { code: "TEST-404", detail: "no fake" },
      };
      return new Response(JSON.stringify(reply.body), {
        status: reply.status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {hr.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const worker = (id: string, name: string) => ({
  id,
  personnelNumber: `W-${name}`,
  workerType: "EMPLOYEE",
  partyPersonId: id,
  legalName: name,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
});
const asOf = {
  workerId: REP,
  date: "2026-10-31",
  employments: [],
  positionId: null,
  positionNumber: "P-REP",
  fteShare: "1.00",
  managerWorkerId: null,
};

describe("self service", () => {
  it("shows the caller's own masked record without sending any worker id", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/hr/me"
        ? {
            status: 200,
            body: {
              ...worker(REP, "Rita"),
              pii: { nationalId: "***-**-6789", masked: true },
              asOf,
            },
          }
        : undefined;
    const { container } = renderAt("/hr/my/profile");
    expect(await screen.findByText("***-**-6789")).toBeTruthy();
    expect(paths).toEqual(["/api/v1/hr/me", "/api/v1/hr/me/profile-changes"]);
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains an unlinked sign-in", async () => {
    handler = () => ({
      status: 404,
      body: { code: "HCM-ESS-001", detail: "no worker" },
    });
    renderAt("/hr/my/profile");
    expect(
      await screen.findByText("No worker record is linked to your sign-in"),
    ).toBeTruthy();
  });

  it("lists the team and opens a report", async () => {
    handler = (url) =>
      url.pathname === "/api/v1/hr/me/team"
        ? { status: 200, body: { items: [worker(REP, "Rita")] } }
        : undefined;
    const { container } = renderAt("/hr/my/team");
    expect(await screen.findByText("W-Rita")).toBeTruthy();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByText("W-Rita"));
  });

  it("shows a team member's job and no personal data", async () => {
    handler = (url) =>
      url.pathname === `/api/v1/hr/me/team/${REP}`
        ? { status: 200, body: { ...worker(REP, "Rita"), asOf } }
        : undefined;
    renderAt(`/hr/my/team/${REP}`);
    expect(await screen.findByText("P-REP")).toBeTruthy();
    expect(screen.queryByText(/National ID/)).toBeNull();
  });

  it("treats someone outside the team as not found", async () => {
    handler = () => ({
      status: 404,
      body: { code: "HCM-WRK-NOT-FOUND", detail: "worker not found" },
    });
    renderAt(`/hr/my/team/${OUTSIDER}`);
    await waitFor(() =>
      expect(screen.getByText("This person isn't on your team")).toBeTruthy(),
    );
  });

  it("gates the routes and nav by duty", () => {
    expect(
      hr.routes
        .filter((r) => r.path?.startsWith("/hr/my/"))
        .every((r) => (r.handle as { duty?: string }).duty),
    ).toBe(true);
    expect(hr.nav.find((n) => n.to === "/hr/my/team")?.duty).toBe("hr.team.manage");
  });
});
