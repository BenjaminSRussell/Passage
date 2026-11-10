import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const WORKER = "33333333-3333-4333-8333-333333333333";
const ID = "44444444-4444-4444-8444-444444444444";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const assignment = {
  id: ID,
  legalEntityId: LE,
  category: "IT hardware",
  vendorId: VENDOR,
  status: "APPROVED",
  validFrom: "2026-03-01",
  version: 1,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};

describe("category vendors list", () => {
  it("shows a loading table, then the assignments, and opens one", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const { router } = renderAt("/purchasing/category-vendors");
    expect(await screen.findByRole("table", { name: "Category vendor assignments" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [assignment] }));
    const row = await screen.findByText("IT hardware");
    expect(screen.getByText("From 2026-03-01, open-ended")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/category-vendors/${ID}`));
  });

  it("explains the empty state without accessibility violations", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/category-vendors");
    expect(await screen.findByText("No assignments yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem code and retries", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/category-vendors");
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
  });

  it("filters by status and category on the server", async () => {
    const mock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/category-vendors");
    await screen.findByText("No assignments yet");
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "BLOCKED" } });
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("status=BLOCKED"))).toBe(true));
    fireEvent.change(document.getElementById("cv-category-filter") as HTMLInputElement, { target: { value: "IT hardware" } });
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("category=IT+hardware"))).toBe(true));
  });

  it("loads the next page on request", async () => {
    const second = { ...assignment, id: "55555555-5555-4555-8555-555555555555", category: "MRO" };
    const mock = stubFetch((url) =>
      String(url).includes("cursor=") ? json({ items: [second] }) : json({ items: [assignment], nextCursor: ID }),
    );
    renderAt("/purchasing/category-vendors");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("MRO")).toBeInTheDocument();
    expect(screen.getByText("IT hardware")).toBeInTheDocument();
    expect(mock).toHaveBeenCalledTimes(2);
  });
});

describe("category vendor create", () => {
  it("validates before sending", async () => {
    const mock = stubFetch(() => json({}));
    const { container } = renderAt("/purchasing/category-vendors/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save assignment" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the category.")).toBeInTheDocument();
    expect(screen.getByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(mock).not.toHaveBeenCalled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts the assignment with an Idempotency-Key and opens it", async () => {
    const mock = stubFetch(() => json(assignment, 201, { ETag: '"1"' }));
    const { router } = renderAt("/purchasing/category-vendors/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: " IT hardware " } });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "PROVISIONAL" } });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/category-vendors/${ID}`));
    const [url, init] = mock.mock.calls[0];
    expect(url).toBe(`${BASE}/category-vendors`);
    expect((init as RequestInit).headers).toHaveProperty("Idempotency-Key");
    expect(JSON.parse((init as RequestInit).body as string)).toMatchObject({
      legalEntityId: LE,
      category: "IT hardware",
      vendorId: VENDOR,
      status: "PROVISIONAL",
    });
  });

  it("shows a duplicate assignment as a problem with its code", async () => {
    stubFetch(() => problem(409, "PRC-CAT-011", "the vendor already has an assignment for this category"));
    renderAt("/purchasing/category-vendors/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Category"), { target: { value: "IT hardware" } });
    fireEvent.change(screen.getByLabelText("Vendor id"), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Save assignment" }));
    expect(await screen.findByText(/already has an assignment/)).toBeInTheDocument();
  });
});

describe("category vendor detail", () => {
  it("changes the status under If-Match", async () => {
    const mock = stubFetch((_url, init) => {
      if (init.method === "PUT") return json({ ...assignment, status: "BLOCKED", version: 2 }, 200, { ETag: '"2"' });
      return json(assignment, 200, { ETag: '"1"' });
    });
    const { container } = renderAt(`/purchasing/category-vendors/${ID}`);
    expect(await screen.findByText("IT hardware")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "BLOCKED" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Assignment saved as version 2.")).toBeInTheDocument();
    const put = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "PUT")!;
    expect(put[0]).toBe(`${BASE}/category-vendors/${ID}`);
    expect((put[1] as RequestInit).headers).toMatchObject({ "If-Match": '"1"' });
    expect(JSON.parse((put[1] as RequestInit).body as string)).toMatchObject({ status: "BLOCKED", validFrom: "2026-03-01" });
  });

  it("explains a stale version and reloads on request", async () => {
    let reads = 0;
    stubFetch((_url, init) => {
      if (init.method === "PUT") return problem(412, "PRC-CAT-013", "category assignment is at version 3, not 1");
      reads += 1;
      return reads === 1 ? json(assignment) : json({ ...assignment, status: "PROVISIONAL", version: 3 });
    });
    renderAt(`/purchasing/category-vendors/${ID}`);
    await screen.findByText("IT hardware");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Someone else changed this assignment/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload assignment" }));
    expect(await screen.findByText("3")).toBeInTheDocument();
    expect(screen.queryByText(/PRC-CAT-013/)).toBeNull();
  });

  it("shows not-found as a load failure", async () => {
    stubFetch(() => problem(404, "PRC-CAT-VENDOR-NOT-FOUND", "category vendor assignment not found"));
    renderAt(`/purchasing/category-vendors/${ID}`);
    expect(await screen.findByText(/PRC-CAT-VENDOR-NOT-FOUND/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });
});

describe("category managers", () => {
  const manager = { id: ID, legalEntityId: LE, category: "IT hardware", workerId: WORKER, createdAt: "2026-03-01T10:00:00Z" };

  it("explains the empty state without accessibility violations", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/category-managers");
    expect(await screen.findByText("No category managers yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("adds a manager and refreshes the list", async () => {
    let rows: unknown[] = [];
    const mock = stubFetch((_url, init) => {
      if (init.method === "POST") {
        rows = [manager];
        return json(manager, 201);
      }
      return json({ items: rows });
    });
    renderAt("/purchasing/category-managers");
    await screen.findByText("No category managers yet");
    fireEvent.click(screen.getByRole("button", { name: "Add manager" }));
    expect(await screen.findByText("Enter the worker id (a UUID).")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(document.getElementById("cm-category") as HTMLInputElement, { target: { value: "IT hardware" } });
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Add manager" }));
    expect(await screen.findByText("IT hardware manager added.")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Remove IT hardware manager" })).toBeInTheDocument();
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`${BASE}/category-managers`);
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ legalEntityId: LE, category: "IT hardware", workerId: WORKER });
  });

  it("removes a manager and reports a failure with its code", async () => {
    let removed = false;
    stubFetch((_url, init) => {
      if (init.method === "DELETE") return removed ? problem(404, "PRC-CAT-MANAGER-NOT-FOUND", "category manager not found") : ((removed = true), new Response(null, { status: 204 }));
      return json({ items: removed ? [] : [manager] });
    });
    renderAt("/purchasing/category-managers");
    fireEvent.click(await screen.findByRole("button", { name: "Remove IT hardware manager" }));
    expect(await screen.findByText("IT hardware manager removed.")).toBeInTheDocument();
    expect(await screen.findByText("No category managers yet")).toBeInTheDocument();
  });

  it("shows a duplicate manager as a problem", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-CAT-012", "the worker already manages this category") : json({ items: [] })));
    renderAt("/purchasing/category-managers");
    await screen.findByText("No category managers yet");
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(document.getElementById("cm-category") as HTMLInputElement, { target: { value: "IT hardware" } });
    fireEvent.change(screen.getByLabelText("Worker id"), { target: { value: WORKER } });
    fireEvent.click(screen.getByRole("button", { name: "Add manager" }));
    expect(await screen.findByText("the worker already manages this category")).toBeInTheDocument();
    expect(screen.getByText("This worker already manages the category. Choose a different worker.")).toBeInTheDocument();
  });
});

describe("category access on requisition submit and consolidation", () => {
  const REQ_ID = "66666666-6666-4666-8666-666666666666";
  const L1 = "c1c1c1c1-c1c1-4c1c-8c1c-c1c1c1c1c1c1";
  const requisition = {
    id: REQ_ID,
    number: "PR-00000007",
    legalEntityId: LE,
    requesterId: "77777777-7777-4777-8777-777777777777",
    status: "DRAFT",
    currency: "USD",
    total: "5000",
    approverChain: [],
    version: 1,
    lines: [
      {
        id: "88888888-8888-4888-8888-888888888888",
        lineNo: 1,
        description: "Laptop",
        category: "IT hardware",
        qty: "2",
        unit: "EA",
        estimatedUnitPrice: "2500",
        amount: "5000",
        requestedDate: "2026-11-02",
        preferredVendorId: VENDOR,
        status: "OPEN",
      },
    ],
    createdAt: "2026-03-01T10:00:00Z",
    updatedAt: "2026-03-01T10:00:00Z",
  };
  const warning = { code: "PRC-CAT-002", lineNo: 1, detail: `requisition PR-00000007 line 1: vendor ${VENDOR} is not assigned to category IT hardware` };

  it("lists the category warnings that come back with the submitted requisition", async () => {
    stubFetch((_url, init) =>
      init.method === "POST"
        ? json({ ...requisition, status: "IN_REVIEW", version: 2, approverChain: ["MANAGER"], warnings: [warning] }, 200, { ETag: '"2"' })
        : json(requisition, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText("Check the vendors against their categories")).toBeInTheDocument();
    expect(screen.getByText(/is not assigned to category IT hardware.*PRC-CAT-002/)).toBeInTheDocument();
  });

  it("shows the HARD refusal at submit with its code and keeps the requisition open", async () => {
    stubFetch((_url, init) =>
      init.method === "POST"
        ? problem(422, "PRC-CAT-002", "requisition PR-00000007 line 1: vendor is not assigned to category IT hardware")
        : json(requisition, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Submit for approval" }));
    expect(await screen.findByText(/not assigned to category IT hardware/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Submit for approval" })).toBeInTheDocument();
  });

  it("lists the category warnings after consolidation", async () => {
    const line = {
      id: L1,
      lineNo: 1,
      description: "Item 1",
      category: "IT hardware",
      qty: "2",
      unit: "EA",
      estimatedUnitPrice: "10",
      amount: "20",
      requestedDate: "2027-03-01",
      preferredVendorId: VENDOR,
      status: "OPEN",
      requisitionId: REQ_ID,
      requisitionNumber: "PR-00000007",
      requisitionStatus: "APPROVED",
      legalEntityId: LE,
      currency: "USD",
    };
    stubFetch((_url, init) =>
      init.method === "POST"
        ? json({ purchaseOrders: [{ id: ID, number: "PO-00000011", currency: "USD", total: "20", lines: [{ lineNo: 1 }] }], warnings: [warning] }, 201)
        : json({ items: [line] }),
    );
    renderAt("/purchasing/consolidation");
    fireEvent.click(await screen.findByLabelText("Select PR-00000007 line 1"));
    fireEvent.click(screen.getByRole("button", { name: "Create purchase orders" }));
    expect(await screen.findByText("Check the vendors against their categories")).toBeInTheDocument();
    expect(await screen.findByRole("list", { name: "Purchase orders created" })).toBeInTheDocument();
  });
});
