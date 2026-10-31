import { findTable } from "../../test-utils";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { COVERAGE_DEFAULTS, type CoverageGroup } from "./api/client";
import { draftOf, validateCoverage } from "./CoverageGroupsPage";
import planning from "./module";

const req: CoverageGroup = { ...COVERAGE_DEFAULTS, id: "00000000-0000-0000-0000-0000000000c1", code: "REQ", version: 1 };
const autoFirm: CoverageGroup = {
  ...COVERAGE_DEFAULTS,
  id: "00000000-0000-0000-0000-0000000000c2",
  code: "FIRM5",
  version: 3,
  firmingFenceDays: 5,
  autoFirm: true,
  safetyStockPolicy: "first-issue",
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const problem = (status: number, code: string, detail: string) => json({ type: "t", title: "x", status, code, detail }, status);

type Handler = [match: string, respond: (url: string, init?: RequestInit) => Response];

function stubApi(...routes: Handler[]) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    for (const [match, respond] of routes) {
      if (url.includes(match)) return respond(url, init);
    }
    throw new Error(`unexpected fetch ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function Location() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderAt(path: string) {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {planning.routes.map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
      </Routes>
      <Location />
    </MemoryRouter>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("validateCoverage", () => {
  it("accepts the FR-22.2 defaults and returns numbers", () => {
    const { errors, settings } = validateCoverage(draftOf("REQ", COVERAGE_DEFAULTS), true);
    expect(errors).toEqual({});
    expect(settings).toEqual(COVERAGE_DEFAULTS);
  });

  it("names every rule the service enforces", () => {
    const draft = { ...draftOf("x", COVERAGE_DEFAULTS), coverageCode: "period" as const, coverageTimeFenceDays: "0", negativeDays: "1.5", autoFirm: true };
    const { errors, settings } = validateCoverage(draft, true);
    expect(settings).toBeNull();
    expect(Object.keys(errors).sort()).toEqual(["autoFirm", "code", "coverageTimeFenceDays", "negativeDays", "periodDays"]);
  });

  it("does not check the code of an existing group", () => {
    expect(validateCoverage(draftOf("", COVERAGE_DEFAULTS), false).errors).toEqual({});
  });
});

describe("Coverage groups page", () => {
  it("lists the groups with their fences and opens one", async () => {
    stubApi(["/coverage-groups/REQ", () => json(req)], ["/coverage-groups", () => json({ items: [autoFirm, req] })]);
    renderAt("/planning/coverage-groups");
    expect(screen.getByRole("heading", { level: 2, name: "Coverage groups" })).toBeInTheDocument();
    const table = await findTable({ name: "Coverage groups" });
    await waitFor(() => expect(within(table).getByText("FIRM5")).toBeInTheDocument());
    expect(within(table).getByText("Auto-firm, 5 d")).toBeInTheDocument();
    expect(within(table).getByText("First issue")).toBeInTheDocument();
    expect(within(table).getByText("Today")).toBeInTheDocument();
    expect(within(table).getAllByText("Requirement (lot-for-lot)")).toHaveLength(2);
    fireEvent.click(within(table).getByRole("button", { name: "REQ" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/planning/coverage-groups/REQ");
    expect(await screen.findByLabelText("Freeze fence (days)")).toHaveValue("0");
    expect(screen.getByLabelText("Safety-stock policy")).toHaveValue("today");
  });

  it("shows the empty state", async () => {
    stubApi(["/coverage-groups", () => json({ items: [] })]);
    renderAt("/planning/coverage-groups");
    expect(await screen.findByText("No coverage groups yet")).toBeInTheDocument();
  });

  it("shows the service error instead of the table", async () => {
    stubApi(["/coverage-groups", () => problem(403, "AUTH-0003", "missing duty planning.read")]);
    renderAt("/planning/coverage-groups");
    expect(await screen.findByText("Could not load coverage groups")).toBeInTheDocument();
    expect(screen.getByText(/AUTH-0003/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});

describe("New coverage group page", () => {
  it("validates before sending, then creates with an Idempotency-Key and opens the group", async () => {
    const fetchMock = stubApi(
      ["/coverage-groups/FIRM5", () => json(autoFirm)],
      ["/coverage-groups", () => json(autoFirm, 201)],
    );
    renderAt("/planning/coverage-groups/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "firm5" } });
    fireEvent.click(screen.getByLabelText("Auto-firm inside the firming fence"));
    fireEvent.click(screen.getByRole("button", { name: "Create coverage group" }));
    expect(await screen.findByText("Auto-firm needs a firming fence of at least 1 day.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Firming fence (days)"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Create coverage group" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/planning/coverage-groups/FIRM5"));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/planning/coverage-groups");
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(init?.body))).toEqual({ ...COVERAGE_DEFAULTS, code: "FIRM5", firmingFenceDays: 5, autoFirm: true });
  });

  it("sends the safety-stock policy chosen on the form", async () => {
    const fetchMock = stubApi(
      ["/coverage-groups/SAFE", () => json({ ...req, code: "SAFE", safetyStockPolicy: "first-issue" })],
      ["/coverage-groups", () => json({ ...req, code: "SAFE", safetyStockPolicy: "first-issue" }, 201)],
    );
    renderAt("/planning/coverage-groups/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "safe" } });
    fireEvent.change(screen.getByLabelText("Safety-stock policy"), { target: { value: "first-issue" } });
    fireEvent.click(screen.getByRole("button", { name: "Create coverage group" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/planning/coverage-groups/SAFE"));
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ ...COVERAGE_DEFAULTS, code: "SAFE", safetyStockPolicy: "first-issue" });
  });

  it("shows a taken code", async () => {
    stubApi(["/coverage-groups", () => problem(409, "PLAN-COVERAGE-EXISTS", "a coverage group with code REQ already exists")]);
    renderAt("/planning/coverage-groups/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "REQ" } });
    fireEvent.click(screen.getByRole("button", { name: "Create coverage group" }));
    expect(await screen.findByText("The coverage group was not saved")).toBeInTheDocument();
    expect(screen.getByText(/PLAN-COVERAGE-EXISTS/)).toBeInTheDocument();
  });
});

describe("Coverage group page", () => {
  it("edits the group under If-Match and shows the new version", async () => {
    const fetchMock = stubApi([
      "/coverage-groups/REQ",
      (_url, init) => (init?.method === "PUT" ? json({ ...req, version: 2, freezeFenceDays: 3 }) : json(req)),
    ]);
    renderAt("/planning/coverage-groups/REQ");
    const freeze = await screen.findByLabelText("Freeze fence (days)");
    // Flush the page's passive effect that clears `saved` on load; under load it can otherwise run after the save.
    await act(async () => {});
    expect(screen.queryByLabelText("Code")).not.toBeInTheDocument();
    fireEvent.change(freeze, { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved. Version 2.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[0]).toBe("/api/v1/planning/coverage-groups/REQ");
    expect((put?.[1]?.headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({ ...COVERAGE_DEFAULTS, freezeFenceDays: 3 });
  });

  it("offers a reload when someone else changed the group", async () => {
    let gets = 0;
    stubApi([
      "/coverage-groups/REQ",
      (_url, init) => {
        if (init?.method === "PUT") return problem(412, "conflict_version", "the coverage group was changed by another user; current version is 2");
        gets += 1;
        return json(gets === 1 ? req : { ...req, version: 2 });
      },
    ]);
    renderAt("/planning/coverage-groups/REQ");
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this coverage group")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload group" }));
    await waitFor(() => expect(gets).toBe(2));
  });

  it("shows a missing group", async () => {
    stubApi(["/coverage-groups/NOPE", () => problem(404, "PLAN-COVERAGE-NOT-FOUND", "coverage group not found")]);
    renderAt("/planning/coverage-groups/NOPE");
    expect(await screen.findByText("Could not load coverage group NOPE")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});
