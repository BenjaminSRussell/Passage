import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { decisionsFor } from "./CapexPage";
import { validateMidQuarter } from "./MidQuarterPage";
import assets from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const ASSET = "22222222-0000-4000-8000-000000000002";
const BOOK = "33333333-0000-4000-8000-000000000003";
const CAPEX = "66666666-0000-4000-8000-000000000006";
const VARIANT = "77777777-0000-4000-8000-000000000007";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {assets.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

const posts = () => calls.filter((c) => c.method !== "GET");

const inApproval = {
  id: CAPEX,
  legalEntityId: LE,
  number: "CR-1",
  title: "Press line",
  requesterId: "plant.manager",
  status: "InApproval",
  currency: "USD",
  selectedVariantId: VARIANT,
  approvalLevel: "CFO",
  requestedAmount: "320000",
  approvedAmount: null,
  createdBy: "creator",
  version: 2,
  createdAt: "2026-04-01T08:00:00Z",
  variants: [{ id: VARIANT, position: 1, description: "Full line", amountsByYear: { "2026": "200000", "2027": "120000" }, totalAmount: "320000", inServiceDate: "2027-06-01", profileAssumption: "SL-120" }],
};

const approval = { id: "d1", decision: "approve", fromStatus: "InApproval", toStatus: "Approved", approvalLevel: "CFO", amount: "320000", comment: null, decidedBy: "cfo", decidedAt: "2026-04-02T09:30:00Z" };

describe("module registration, slice 4", () => {
  it("registers the approvals list and the mid-quarter test with their duties", () => {
    const paths = assets.routes.map((r) => r.path);
    expect(paths).toEqual(expect.arrayContaining(["/assets/capex/approvals", "/assets/mid-quarter-test"]));
    expect(assets.nav.find((n) => n.to === "/assets/capex/approvals")?.duty).toBe("CapexRequestApprove");
    expect(assets.nav.find((n) => n.to === "/assets/mid-quarter-test")?.duty).toBe("AssetDepreciate");
  });
});

describe("capex decisions", () => {
  it("offers only the legal moves of each status (BR-37.12)", () => {
    expect(decisionsFor("InApproval").map((a) => a.decision)).toEqual(["approve", "postpone", "reject"]);
    expect(decisionsFor("Postponed").map((a) => a.decision)).toEqual(["reopen"]);
    expect(decisionsFor("Approved").map((a) => a.decision)).toEqual(["release"]);
    expect(decisionsFor("Released").map((a) => a.decision)).toEqual(["close"]);
    for (const status of ["Draft", "Submitted", "Rejected", "Closed"] as const) expect(decisionsFor(status)).toEqual([]);
    expect(decisionsFor("InApproval").filter((a) => a.needsComment).map((a) => a.decision)).toEqual(["postpone", "reject"]);
  });

  it("approves with If-Match, shows the approved amount and the trail, then offers release (AC-37.11)", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { ...inApproval, status: "Approved", approvedAmount: "320000", version: 3, decisions: [approval] }, etag: '"3"' }
        : { status: 200, body: inApproval, etag: '"2"' };
    const { container } = renderAt(`/assets/capex/${CAPEX}`);
    expect(await screen.findByRole("heading", { name: "CR-1 Press line" })).toBeInTheDocument();
    expect(screen.getByText(/Waiting for the CFO/)).toBeInTheDocument();
    expect(within(screen.getByRole("table", { name: "Decision trail" })).getByText("No decisions yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.queryByRole("button", { name: "Submit request" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Approved 320000 USD.");
    const post = posts()[0];
    expect(post.url.pathname).toBe(`/api/v1/assets/capex-requests/${CAPEX}/approve`);
    expect(post.headers.get("If-Match")).toBe('"2"');
    expect(post.body).toBeUndefined();
    const trail = screen.getByRole("table", { name: "Decision trail" });
    expect(within(trail).getByText("cfo")).toBeInTheDocument();
    expect(within(trail).getByText("2026-04-02 09:30")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Release" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("uses the new ETag for the next decision", async () => {
    let version = 2;
    handler = (url, init) => {
      if (init.method !== "POST") return { status: 200, body: inApproval, etag: '"2"' };
      version += 1;
      const status = url.pathname.endsWith("/approve") ? "Approved" : "Released";
      return { status: 200, body: { ...inApproval, status, approvedAmount: "320000", version }, etag: `"${version}"` };
    };
    renderAt(`/assets/capex/${CAPEX}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    fireEvent.click(await screen.findByRole("button", { name: "Release" }));
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect(posts()[1].url.pathname.endsWith("/release")).toBe(true);
    expect(posts()[1].headers.get("If-Match")).toBe('"3"');
    expect(await screen.findByRole("button", { name: "Close" })).toBeInTheDocument();
  });

  it("asks for a reason before postponing and sends it", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { ...inApproval, status: "Postponed", version: 3, decisions: [{ ...approval, decision: "postpone", toStatus: "Postponed", comment: "Next budget year" }] }, etag: '"3"' }
        : { status: 200, body: inApproval, etag: '"2"' };
    renderAt(`/assets/capex/${CAPEX}`);
    fireEvent.click(await screen.findByRole("button", { name: "Postpone" }));
    expect(await screen.findByText("Say why you postpone the request.")).toBeInTheDocument();
    expect(posts()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Comment"), { target: { value: "Next budget year" } });
    fireEvent.click(screen.getByRole("button", { name: "Postpone" }));
    expect(await screen.findByRole("status")).toHaveTextContent("The request is now postponed.");
    expect(posts()[0].body).toEqual({ comment: "Next budget year" });
    expect(within(screen.getByRole("table", { name: "Decision trail" })).getByText("Next budget year")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Return to draft" })).toBeInTheDocument();
  });

  it("explains a refused decision on the requester's own request", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 403, body: { code: "AST-0028", detail: "you created or requested this capex request; another approver must decide it" } }
        : { status: 200, body: inApproval, etag: '"2"' };
    renderAt(`/assets/capex/${CAPEX}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("You cannot decide your own request");
    expect(alert).toHaveTextContent("(AST-0028)");
    expect(screen.getByRole("button", { name: "Approve" })).toBeInTheDocument();
  });

  it("offers a reload when the request changed", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 412, body: { code: "AST-0006", detail: "the request changed since you read it" } } : { status: 200, body: inApproval, etag: '"2"' };
    renderAt(`/assets/capex/${CAPEX}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The request changed since you opened it");
    fireEvent.click(within(alert).getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "GET")).toHaveLength(2));
  });

  it("shows no decision for a closed request", async () => {
    handler = () => ({ status: 200, body: { ...inApproval, status: "Closed", approvedAmount: "320000", decisions: [approval] }, etag: '"5"' });
    renderAt(`/assets/capex/${CAPEX}`);
    expect(await screen.findByRole("table", { name: "Decision trail" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Decide the request" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("opens the approvals list on requests in approval", async () => {
    handler = () => ({ status: 200, body: { items: [{ ...inApproval, variants: undefined }] } });
    renderAt("/assets/capex/approvals");
    const table = await screen.findByRole("table", { name: "Capex requests" });
    await waitFor(() => expect(within(table).getByText("CR-1")).toBeInTheDocument());
    expect(calls[0].url.search).toBe("?status=InApproval");
    expect(screen.getByLabelText("Status")).toHaveValue("InApproval");
  });
});

const report = {
  legalEntityId: LE,
  bookCode: "TAX",
  currency: "USD",
  year: 2026,
  bookCount: 4,
  totalBasis: "110000",
  quarterBasis: ["30000", "10000", "0", "70000"],
  q4Share: "0.6364",
  applies: true,
  warnings: ["AST-0511"],
  reviewCount: 1,
  review: [{ assetId: ASSET, assetNumber: "FA-1", bookId: BOOK, method: "DecliningBalance", convention: "HalfYear", inServiceDate: "2026-02-10", quarter: 1, cost: "30000" }],
};

describe("mid-quarter test", () => {
  it("validates the entity, book code and year", () => {
    expect(validateMidQuarter({ legalEntityId: LE, bookCode: "TAX", year: "2026" }).filter).toEqual({ legalEntityId: LE, bookCode: "TAX", year: 2026 });
    const bad = validateMidQuarter({ legalEntityId: "x", bookCode: "tax", year: "26" }).errors;
    expect(Object.keys(bad).sort()).toEqual(["bookCode", "legalEntityId", "year"]);
  });

  it("runs the test and lists the books to switch (BR-37.5, AST-0511)", async () => {
    handler = () => ({ status: 200, body: report });
    const { container } = renderAt("/assets/mid-quarter-test");
    expect(screen.getByText("Choose a tax book and year")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Run test" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Tax year"), { target: { value: "2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Run test" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Q4 placed-in-service share 63.64% exceeds 40%");
    expect(calls[0].url.search).toBe(`?legalEntityId=${LE}&bookCode=TAX&year=2026`);
    const tiles = screen.getByRole("list", { name: "Basis by quarter" });
    expect(within(tiles).getByText("70000 USD")).toBeInTheDocument();
    expect(within(tiles).getByText("63.64% of the year")).toBeInTheDocument();
    const review = screen.getByRole("table", { name: "Books to review" });
    expect(within(review).getByText("FA-1")).toBeInTheDocument();
    expect(within(review).getByText("Switch to mid-quarter")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when mid-quarter does not apply and when nothing was placed in service", async () => {
    handler = (url) =>
      url.searchParams.get("year") === "2026"
        ? { status: 200, body: { ...report, applies: false, q4Share: "0.4", warnings: [], reviewCount: 0, review: [] } }
        : { status: 200, body: { ...report, year: 2024, bookCount: 0, currency: null, applies: false, warnings: [], reviewCount: 0, review: [] } };
    renderAt("/assets/mid-quarter-test");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Tax year"), { target: { value: "2026" } });
    fireEvent.click(screen.getByRole("button", { name: "Run test" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Q4 placed-in-service share 40% is 40% or less");
    expect(within(screen.getByRole("table", { name: "Books to review" })).getByText("Every convention follows the test")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Tax year"), { target: { value: "2024" } });
    fireEvent.click(screen.getByRole("button", { name: "Run test" }));
    expect(await screen.findByText("Nothing placed in service")).toBeInTheDocument();
  });

  it("reports a refused test", async () => {
    handler = () => ({ status: 422, body: { code: "AST-0029", detail: "books TAX placed in service in the year use more than one currency" } });
    renderAt("/assets/mid-quarter-test");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Run test" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("more than one currency (AST-0029)");
  });
});
