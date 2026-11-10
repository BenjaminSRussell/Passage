import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { id, json, pageViolations, problem, renderAt, routes, useFakeApi } from "./hrTestKit";
import hr from "./module";
import { validateChange } from "./ProfileChangesCard";

const ME = id(1);
const REPORT = id(2);

const me = {
  id: ME,
  personnelNumber: "W-1",
  workerType: "EMPLOYEE",
  partyPersonId: ME,
  legalName: "Rita Ray",
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
  pii: { nationalId: "***-**-6789", bankAccount: "****3000", homeAddress: "Springfield, US", masked: true },
  asOf: { workerId: ME, date: "2026-11-05", employments: [], positionId: null, positionNumber: "P-1", fteShare: "1.00", managerWorkerId: null },
};

const change = (over: Record<string, unknown> = {}) => ({
  id: id(10),
  workerId: REPORT,
  workerName: "Sam Lee",
  personnelNumber: "W-2",
  kind: "HOME_ADDRESS",
  status: "PENDING",
  summary: "Springfield, US",
  requestedBy: "u2",
  createdAt: "2026-11-04T10:00:00Z",
  ...over,
});

describe("ESS profile changes", () => {
  const api = useFakeApi();

  it("shows the masked address and sends an address change for approval without a worker id", async () => {
    let changes: unknown[] = [];
    api.handler = routes({
      "GET /api/v1/hr/me": json(200, me),
      "GET /api/v1/hr/me/profile-changes": () => json(200, { items: changes }),
      "POST /api/v1/hr/me/profile-changes": () => {
        const created = change({ workerId: ME, workerName: "Rita Ray" });
        changes = [created];
        return json(201, created);
      },
    });
    const { container } = renderAt("/hr/my/profile", ["hr.profile.self"]);
    expect(await screen.findByText("No changes yet")).toBeTruthy();
    expect(screen.getAllByText("Springfield, US").length).toBeGreaterThan(0);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Change home address" }));
    const form = screen.getByRole("form", { name: "Change home address" });
    fireEvent.click(within(form).getByRole("button", { name: "Send for approval" }));
    expect(await within(form).findByText("Enter the street address.")).toBeTruthy();
    expect(api.posts("/profile-changes")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("Street address"), { target: { value: " 1 Main St " } });
    fireEvent.change(within(form).getByLabelText("City"), { target: { value: "Springfield" } });
    fireEvent.change(within(form).getByLabelText("Postal code"), { target: { value: "62701" } });
    fireEvent.change(within(form).getByLabelText("Country code"), { target: { value: "us" } });
    fireEvent.click(within(form).getByRole("button", { name: "Send for approval" }));
    expect(await screen.findByText("Home address change sent for approval.")).toBeTruthy();
    expect(api.posts("/me/profile-changes")[0].body).toEqual({
      kind: "HOME_ADDRESS",
      address: { line1: "1 Main St", city: "Springfield", postalCode: "62701", country: "US" },
    });
    expect(await screen.findByText("Waiting for approval")).toBeTruthy();
  });

  it("keeps the notice for an applied preferred name while the profile reloads", async () => {
    let changes: unknown[] = [];
    api.handler = routes({
      "GET /api/v1/hr/me": json(200, me),
      "GET /api/v1/hr/me/profile-changes": () => json(200, { items: changes }),
      "POST /api/v1/hr/me/profile-changes": () => {
        const created = change({ workerId: ME, workerName: "Rita Ray", kind: "PREFERRED_NAME", status: "APPLIED", summary: "Rae" });
        changes = [created];
        return json(201, created);
      },
    });
    // Answer the profile reload late, as a slow network does, so the page is mid-reload when the notice should show.
    const fakeFetch = globalThis.fetch;
    let profileReads = 0;
    vi.stubGlobal("fetch", async (input: string, init?: RequestInit) => {
      if (new URL(input, "http://localhost").pathname === "/api/v1/hr/me" && ++profileReads > 1) {
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      return fakeFetch(input, init);
    });
    renderAt("/hr/my/profile", ["hr.profile.self"]);
    expect(await screen.findByText("No changes yet")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Change preferred name" }));
    const form = screen.getByRole("form", { name: "Change preferred name" });
    fireEvent.change(within(form).getByLabelText("Preferred name"), { target: { value: "Rae" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Preferred name updated.")).toBeTruthy();
    await waitFor(() => expect(api.calls.filter((c) => c.method === "GET" && c.url.pathname === "/api/v1/hr/me")).toHaveLength(2));
    expect(screen.getByText("Preferred name updated.")).toBeTruthy();
  });

  it("withdraws a pending change and shows the service's conflict", async () => {
    api.handler = routes({
      "GET /api/v1/hr/me": json(200, me),
      "GET /api/v1/hr/me/profile-changes": json(200, { items: [change({ kind: "BANK_ACCOUNT", summary: "****1234", workerId: ME })] }),
      [`POST /api/v1/hr/me/profile-changes/${id(10)}/cancel`]: problem(409, "HCM-ESS-004", "the change is no longer waiting for a decision"),
    });
    renderAt("/hr/my/profile", ["hr.profile.self"]);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw the bank account change" }));
    expect(await screen.findByText(/no longer waiting for a decision/)).toBeTruthy();
  });

  it("validates the bank account and preferred name like the service", () => {
    const blank = { preferredName: "", address: { line1: "", city: "", postalCode: "", country: "" }, bankAccount: "" };
    expect(validateChange("BANK_ACCOUNT", { ...blank, bankAccount: "de89 3704-0044" }).input).toEqual({ kind: "BANK_ACCOUNT", bankAccount: "DE8937040044" });
    expect(validateChange("BANK_ACCOUNT", { ...blank, bankAccount: "12-34" }).errors.bankAccount).toBeTruthy();
    expect(validateChange("PREFERRED_NAME", { ...blank, preferredName: "  Sam " }).input).toEqual({ kind: "PREFERRED_NAME", preferredName: "Sam" });
    expect(validateChange("PREFERRED_NAME", { ...blank, preferredName: "x".repeat(101) }).errors.preferredName).toBeTruthy();
  });
});

describe("MSS change approvals", () => {
  const api = useFakeApi();

  it("lists the team queue and approves through the team route", async () => {
    let status = "PENDING";
    api.handler = routes({
      "GET /api/v1/hr/me/team/profile-changes": () => json(200, { items: [change({ status })] }),
      [`POST /api/v1/hr/me/team/profile-changes/${id(10)}/decide`]: () => {
        status = "APPROVED";
        return json(200, change({ status }));
      },
    });
    const { container } = renderAt("/hr/my/team/changes", ["hr.team.approve"]);
    const approve = await screen.findByRole("button", { name: "Approve Sam Lee's home address change" });
    expect(api.calls[0].url.search).toBe("?status=PENDING");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(approve);
    expect(await screen.findByText("Sam Lee's home address change is approved.")).toBeTruthy();
    expect(api.posts("/decide")[0].body).toEqual({ decision: "APPROVE" });
  });

  it("uses the HR route and shows a self-decision refusal", async () => {
    api.handler = routes({
      "GET /api/v1/hr/profile-changes": json(200, { items: [change({ kind: "BANK_ACCOUNT", summary: "****1234" })] }),
      [`POST /api/v1/hr/profile-changes/${id(10)}/decide`]: problem(403, "HCM-ESS-006", "you cannot decide a change to your own record"),
    });
    renderAt("/hr/profile-changes", ["hr.worker.maintain"]);
    fireEvent.click(await screen.findByRole("button", { name: "Reject Sam Lee's bank account change" }));
    await waitFor(() => expect(screen.getByText(/HCM-ESS-006/)).toBeTruthy());
  });

  it("opens my absence for the signed-in worker", async () => {
    api.handler = routes({
      "GET /api/v1/hr/me": json(200, me),
      [`GET /api/v1/hr/workers/${ME}/absence/balances`]: json(200, { workerId: ME, asOf: "2026-11-05", balances: [] }),
      "GET /api/v1/hr/absence/requests": json(200, { items: [] }),
      "GET /api/v1/hr/absence/plans": json(200, { items: [] }),
    });
    renderAt("/hr/my/absence", ["hr.absence.self"]);
    expect(await screen.findByText(/no absence plans yet/)).toBeTruthy();
    expect(api.calls.some((c) => c.url.pathname === "/api/v1/hr/absence/requests" && c.url.searchParams.get("workerId") === ME)).toBe(true);
  });

  it("gates the new routes and nav by duty", () => {
    const duty = (path: string) => (hr.routes.find((r) => r.path === path)?.handle as { duty?: string } | undefined)?.duty;
    expect(duty("/hr/my/absence")).toBe("hr.absence.self");
    expect(duty("/hr/my/team/changes")).toBe("hr.team.approve");
    expect(duty("/hr/profile-changes")).toBe("hr.worker.maintain");
    expect(hr.nav.find((n) => n.to === "/hr/my/team/changes")?.duty).toBe("hr.team.approve");
  });
});
