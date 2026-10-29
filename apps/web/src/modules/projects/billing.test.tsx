import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findStatus, findTable } from "../../test-utils";
import type { Category, InvoiceProposal, Project, Transaction } from "./api/client";
import { blankTransaction, transactionInput } from "./BillingPages";
import projects from "./module";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown };
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
      const { status, body } = await handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p>At {location.pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {projects.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

const problem = (status: number, code: string, detail: string, errors?: Record<string, string>): Reply => ({
  status,
  body: { type: `https://passage.dev/problems/${code}`, title: "Error", status, code, detail, ...(errors ? { errors } : {}) },
});

const writes = () => calls.filter((c) => c.method !== "GET");

const WORKER = "0b1c2d3e-0000-4000-8000-0000000000c1";
const TXN_A = "0b1c2d3e-0000-4000-8000-0000000000d1";
const TXN_B = "0b1c2d3e-0000-4000-8000-0000000000d2";
const PROP = "0b1c2d3e-0000-4000-8000-0000000000e1";

const project: Project = {
  id: "ES-1001",
  legalEntityId: "0b1c2d3e-0000-4000-8000-000000000101",
  projectGroupId: "ENG",
  projectType: "TIME_MATERIAL",
  currency: "USD",
  contractValue: "0.00",
  stage: "IN_PROCESS",
  startDate: "2026-09-01",
  version: 2,
  createdAt: "2026-09-01T09:00:00Z",
  updatedAt: "2026-09-01T09:00:00Z",
};

const categories: Category[] = [
  { id: "LABOR", name: "Labour", txnType: "HOUR", version: 1, createdAt: "", updatedAt: "" },
  { id: "TRAVEL", name: "Travel", txnType: "COST", version: 1, createdAt: "", updatedAt: "" },
];

function txn(overrides: Partial<Transaction> = {}): Transaction {
  return {
    id: TXN_A,
    projectId: "ES-1001",
    txnType: "HOUR",
    categoryId: "LABOR",
    lineProperty: "CHARGEABLE",
    txnDate: "2026-09-02",
    quantity: "8.000",
    costPrice: "50.000000",
    salesPrice: "90.005000",
    currency: "USD",
    costAmount: "400.00",
    salesAmount: "720.04",
    workerId: WORKER,
    sourceDocType: "MANUAL",
    description: "",
    createdBy: "acct-1",
    createdAt: "2026-09-02T09:00:00Z",
    ...overrides,
  };
}

function proposal(overrides: Partial<InvoiceProposal> = {}): InvoiceProposal {
  return {
    id: PROP,
    projectId: "ES-1001",
    status: "DRAFT",
    currency: "USD",
    fromDate: "2026-09-01",
    toDate: "2026-09-30",
    txnTypes: ["HOUR"],
    totalCost: "500.00",
    totalSales: "900.05",
    lineCount: 2,
    lines: [
      { lineNo: 1, transactionId: TXN_A, txnType: "HOUR", txnDate: "2026-09-02", categoryId: "LABOR", description: "", quantity: "8.000", unitPrice: "90.005000", costAmount: "400.00", amount: "720.04" },
      { lineNo: 2, transactionId: TXN_B, txnType: "HOUR", txnDate: "2026-09-20", categoryId: "LABOR", description: "Site visit", quantity: "2.000", unitPrice: "90.005000", costAmount: "100.00", amount: "180.01" },
    ],
    createdBy: "acct-1",
    version: 1,
    createdAt: "2026-10-01T09:00:00Z",
    updatedAt: "2026-10-01T09:00:00Z",
    ...overrides,
  };
}

describe("routes", () => {
  it("registers the billing pages and the categories setup", () => {
    const paths = projects.routes.map((r) => r.path);
    for (const path of ["/projects/categories", "/projects/:id/transactions", "/projects/:id/invoice-proposals", "/projects/:id/invoice-proposals/:proposalId"]) {
      expect(paths).toContain(path);
    }
    expect(projects.nav).toContainEqual({ label: "Project categories", to: "/projects/categories", duty: "ProjInquire" });
  });
});

describe("transactionInput", () => {
  const filled = { ...blankTransaction, categoryId: "LABOR", txnDate: "2026-09-02", quantity: "7.5", costPrice: "50", salesPrice: "90.005", workerId: WORKER };

  it("builds the request with the project currency and only the fields the type needs", () => {
    const { errors, input } = transactionInput("ES-1001", "USD", { ...filled, itemId: "ignored", description: "  Design review " });
    expect(errors).toEqual({});
    expect(input).toEqual({
      projectId: "ES-1001",
      txnType: "HOUR",
      categoryId: "LABOR",
      lineProperty: "CHARGEABLE",
      txnDate: "2026-09-02",
      quantity: "7.5",
      costPrice: "50",
      salesPrice: "90.005",
      currency: "USD",
      workerId: WORKER,
      description: "Design review",
    });
  });

  it("mirrors the service checks", () => {
    expect(transactionInput("ES-1001", "USD", { ...blankTransaction, quantity: "1.2345", costPrice: "-1", salesPrice: "x" }).errors).toEqual({
      categoryId: expect.any(String),
      txnDate: expect.any(String),
      quantity: expect.any(String),
      costPrice: expect.any(String),
      salesPrice: expect.any(String),
      workerId: expect.any(String),
    });
    expect(transactionInput("ES-1001", "USD", { ...filled, txnType: "ITEM", itemId: "" }).errors).toHaveProperty("itemId");
    expect(transactionInput("ES-1001", "USD", { ...filled, wbsNodeId: "node-1" }).errors).toHaveProperty("wbsNodeId");
    // A negative sales price is a retainer applied on account (BR-41.4), not on hours.
    expect(transactionInput("ES-1001", "USD", { ...filled, salesPrice: "-100" }).errors).toHaveProperty("salesPrice");
    const retainer = transactionInput("ES-1001", "USD", { ...filled, txnType: "ON_ACCOUNT", categoryId: "MILESTONE", salesPrice: "-100", workerId: "" });
    expect(retainer.errors).toEqual({});
    expect(retainer.input).not.toHaveProperty("workerId");
  });
});

describe("project categories", () => {
  it("lists categories, adds one without a version and renames with If-Match", async () => {
    handler = (_url, init) =>
      init.method === "PUT"
        ? { status: 200, body: { ...categories[0], name: "Professional labour", version: 2 } }
        : { status: 200, body: { items: categories } };
    const { container } = renderAt("/projects/categories");
    const table = await findTable({ name: "Project categories" });
    expect(table).toHaveTextContent("Travel");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Category id"), { target: { value: "fees" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Fees" } });
    fireEvent.change(screen.getByLabelText("Transaction type"), { target: { value: "FEE" } });
    fireEvent.click(screen.getByRole("button", { name: "Add category" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].url.pathname).toBe("/api/v1/projects/categories/FEES");
    expect(writes()[0].headers.get("If-Match")).toBeNull();
    expect(writes()[0].body).toEqual({ name: "Fees", txnType: "FEE" });

    fireEvent.click(within(await findTable({ name: "Project categories" })).getByRole("button", { name: "LABOR" }));
    expect(screen.getByLabelText("Category id")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Transaction type")).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Professional labour" } });
    fireEvent.click(screen.getByRole("button", { name: "Save category" }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1].headers.get("If-Match")).toBe('"1"');
    expect(await findStatus()).toHaveTextContent("Category LABOR is saved.");
  });

  it("validates before sending and shows load failures with a retry", async () => {
    let fail = true;
    handler = () => (fail ? problem(503, "HTTP-503", "unavailable") : { status: 200, body: { items: [] } });
    renderAt("/projects/categories");
    expect(await findAlert()).toHaveTextContent("Could not load the categories");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No categories yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Category id"), { target: { value: "bad id" } });
    fireEvent.click(screen.getByRole("button", { name: "Add category" }));
    expect(await screen.findByText("Use 1 to 20 capitals, digits, hyphens or underscores.")).toBeInTheDocument();
    expect(writes()).toHaveLength(0);
  });
});

describe("transactions", () => {
  const reads = (url: URL): Reply | null => {
    if (url.pathname === "/api/v1/projects/ES-1001") return { status: 200, body: project };
    if (url.pathname === "/api/v1/projects/categories") return { status: 200, body: { items: categories } };
    return null;
  };

  it("lists transactions, records a priced line and explains why a line is not billed", async () => {
    let items: Transaction[] = [txn({ proposalId: PROP }), txn({ id: TXN_B, lineProperty: "NON_CHARGEABLE", txnDate: "2026-09-05", salesAmount: "90.01" })];
    handler = (url, init) => {
      if (init.method === "POST") {
        const created = txn({ id: "0b1c2d3e-0000-4000-8000-0000000000d3", quantity: "7.500", costAmount: "375.00", salesAmount: "675.04" });
        items = [created, ...items];
        return { status: 201, body: created };
      }
      if (url.pathname.endsWith("/billing")) return { status: 200, body: { transactionId: TXN_B, billable: false, reason: "NOT_CHARGEABLE" } };
      return reads(url) ?? { status: 200, body: { items } };
    };
    const { container } = renderAt("/projects/ES-1001/transactions");
    const table = await findTable({ name: "Transactions" });
    expect(table).toHaveTextContent("720.04");
    expect(within(table).getByRole("link", { name: "On a proposal" })).toHaveAttribute("href", `/projects/ES-1001/invoice-proposals/${PROP}`);
    await waitFor(() => expect(screen.getByRole("option", { name: "LABOR Labour" })).toBeInTheDocument());
    expect(screen.queryByRole("option", { name: "TRAVEL Travel" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Billing window from"), { target: { value: "2026-09-01" } });
    fireEvent.change(screen.getByLabelText("Billing window to"), { target: { value: "2026-09-30" } });
    fireEvent.click(within(table).getByRole("button", { name: "Check billing for 2026-09-05 LABOR" }));
    expect(await screen.findByText("The line is not chargeable.")).toBeInTheDocument();
    const check = calls.find((c) => c.url.pathname.endsWith("/billing"));
    expect(check?.url.pathname).toBe(`/api/v1/projects/transactions/${TXN_B}/billing`);
    expect(check?.url.searchParams.get("from")).toBe("2026-09-01");
    expect(check?.url.searchParams.get("to")).toBe("2026-09-30");

    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "LABOR" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2026-09-10" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "7.5" } });
    fireEvent.change(screen.getByLabelText("Unit cost"), { target: { value: "50" } });
    fireEvent.change(screen.getByLabelText("Unit sales price"), { target: { value: "90.005" } });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Record transaction" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    const post = writes()[0];
    expect(post.url.pathname).toBe("/api/v1/projects/transactions");
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toMatchObject({ projectId: "ES-1001", txnType: "HOUR", categoryId: "LABOR", currency: "USD", quantity: "7.5", workerId: WORKER });
    expect(await findStatus()).toHaveTextContent("cost 375.00, sales 675.04 USD");
    await waitFor(() => expect(screen.getByRole("table", { name: "Transactions" })).toHaveTextContent("675.04"));
  });

  it("shows the service's rule failure and field errors without losing the form", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? problem(422, "PROJ-TXN-003", "Category LABOR is for HOUR transactions, not FEE")
        : (reads(url) ?? { status: 200, body: { items: [] } });
    renderAt("/projects/ES-1001/transactions");
    expect(await screen.findByText("No transactions yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Record transaction" }));
    expect(await screen.findByText("Choose a category.")).toBeInTheDocument();
    expect(writes()).toHaveLength(0);

    await waitFor(() => expect(screen.getByRole("option", { name: "LABOR Labour" })).toBeInTheDocument());
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "LABOR" } });
    fireEvent.change(screen.getByLabelText("Date"), { target: { value: "2026-09-10" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Unit cost"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Unit sales price"), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Record transaction" }));
    expect(await findAlert()).toHaveTextContent("That category does not fit");
    expect(screen.getByLabelText("Quantity")).toHaveValue("1");
  });

  it("shows not found for an unknown project", async () => {
    handler = (url) => (url.pathname === "/api/v1/projects/categories" ? { status: 200, body: { items: [] } } : problem(404, "not_found", "project not found"));
    renderAt("/projects/NOPE/transactions");
    expect(await screen.findByText("Transactions not found")).toBeInTheDocument();
  });
});

describe("invoice proposals", () => {
  it("lists proposals with a status filter and builds one, then opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return { status: 201, body: proposal() };
      if (url.pathname === `/api/v1/projects/invoice-proposals/${PROP}`) return { status: 200, body: proposal() };
      return { status: 200, body: { items: url.searchParams.get("status") === "POSTED" ? [] : [proposal({ lines: [] })] } };
    };
    const { container } = renderAt("/projects/ES-1001/invoice-proposals");
    const table = await findTable({ name: "Invoice proposals" });
    expect(table).toHaveTextContent("900.05 USD");
    expect(table).toHaveTextContent("Draft");
    expect(calls[0].url.searchParams.get("projectId")).toBe("ES-1001");
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "POSTED" } });
    expect(await screen.findByText("No proposals match")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Build proposal" }));
    expect(await screen.findByText("Choose the first day of the window.")).toBeInTheDocument();
    expect(writes()).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-09-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-09-30" } });
    fireEvent.change(screen.getByLabelText("Transaction type"), { target: { value: "HOUR" } });
    fireEvent.click(screen.getByRole("button", { name: "Build proposal" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0].body).toEqual({ projectId: "ES-1001", from: "2026-09-01", to: "2026-09-30", txnTypes: ["HOUR"] });
    expect(writes()[0].headers.get("Idempotency-Key")).toBeTruthy();
    expect(await screen.findByRole("heading", { name: "Invoice proposal 2026-09-01 to 2026-09-30" })).toBeInTheDocument();
  });

  it("explains an empty selection", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? problem(422, "PROJ-PROP-002", "No billable transactions matched the selection") : { status: 200, body: { items: [] } };
    renderAt("/projects/ES-1001/invoice-proposals");
    expect(await screen.findByText("No invoice proposals yet")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "2026-09-01" } });
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-08-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Build proposal" }));
    expect(await screen.findByText("The window must not end before it starts.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("To"), { target: { value: "2026-09-30" } });
    fireEvent.click(screen.getByRole("button", { name: "Build proposal" }));
    expect(await findAlert()).toHaveTextContent("Nothing to bill");
  });
});

describe("invoice proposal detail", () => {
  it("removes a line, approves and cancels with the version as If-Match", async () => {
    let current = proposal();
    handler = (url, init) => {
      if (init.method === "DELETE") {
        current = proposal({ version: 2, lineCount: 1, totalSales: "720.04", totalCost: "400.00", lines: [proposal().lines[0]] });
        return { status: 200, body: current };
      }
      if (url.pathname.endsWith("/approve")) {
        current = { ...current, status: "APPROVED", approvedBy: "acct-1", version: current.version + 1 };
        return { status: 200, body: current };
      }
      if (url.pathname.endsWith("/cancel")) {
        current = { ...current, status: "CANCELED", version: current.version + 1 };
        return { status: 200, body: current };
      }
      return { status: 200, body: current };
    };
    const { container } = renderAt(`/projects/ES-1001/invoice-proposals/${PROP}`);
    const lines = await findTable({ name: "Proposal lines" });
    expect(lines).toHaveTextContent("Site visit");
    expect(screen.getByText("900.05 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(lines).getByRole("button", { name: "Remove line 2" }));
    expect(await findStatus()).toHaveTextContent("Line 2 is removed and back in the pool.");
    expect(writes()[0].method).toBe("DELETE");
    expect(writes()[0].url.pathname).toBe(`/api/v1/projects/invoice-proposals/${PROP}/lines/${TXN_B}`);
    expect(writes()[0].headers.get("If-Match")).toBe('"1"');
    expect(screen.getByText("720.04 USD")).toBeInTheDocument();

    const actions = screen.getByRole("group", { name: "Proposal actions" });
    fireEvent.click(within(actions).getByRole("button", { name: "Approve proposal" }));
    await waitFor(() => expect(screen.getByText("Approved")).toBeInTheDocument());
    expect(writes()[1].headers.get("If-Match")).toBe('"2"');
    expect(within(screen.getByRole("group", { name: "Proposal actions" })).queryByRole("button", { name: "Approve proposal" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Cancel proposal" }));
    expect(await screen.findByText("This proposal is canceled; its lines went back to the pool.")).toBeInTheDocument();
    expect(writes()[2].url.pathname).toBe(`/api/v1/projects/invoice-proposals/${PROP}/cancel`);
    expect(screen.queryByRole("button", { name: "Remove line 1" })).toBeNull();
  });

  it("offers a reload when someone else changed the proposal", async () => {
    let version = 1;
    handler = (_url, init) => (init.method === "POST" ? problem(412, "conflict_version", "stale") : { status: 200, body: proposal({ version }) });
    renderAt(`/projects/ES-1001/invoice-proposals/${PROP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve proposal" }));
    expect(await findAlert()).toHaveTextContent("Someone else changed this proposal");
    version = 3;
    fireEvent.click(screen.getByRole("button", { name: "Reload proposal" }));
    await waitFor(() => expect(screen.getByText("3")).toBeInTheDocument());
  });

  it("shows not found and load failures", async () => {
    handler = () => problem(404, "not_found", "invoice proposal not found");
    const { unmount } = renderAt(`/projects/ES-1001/invoice-proposals/${PROP}`);
    expect(await screen.findByText("Invoice proposal not found")).toBeInTheDocument();
    unmount();
    handler = () => problem(500, "HTTP-500", "boom");
    renderAt(`/projects/ES-1001/invoice-proposals/${PROP}`);
    expect(await findAlert()).toHaveTextContent("Could not load the proposal");
  });
});
