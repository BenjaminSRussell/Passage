import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EcrCreatePage, EcrPage, EcrsPage } from "./ecr";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, title: string, extra: object = {}) => ({ ok: false, status, json: async () => ({ code, title, ...extra }) });
const ecr = (id: string, extra: object = {}) => ({ id, number: `ECR-${id}`, title: `Change ${id}`, priority: "high", state: "draft", products: ["BIKE", "WHEEL"], requestedBy: "rita", ...extra });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("EcrsPage", () => {
  it("shows loading, rows and opens a row", async () => {
    stub(() => ok({ items: [ecr("a"), ecr("b", { state: "under_review" })] }));
    const opened: string[] = [];
    render(<EcrsPage onOpen={(id) => opened.push(id)} />);
    expect(screen.getByRole("table", { name: "Engineering change requests" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Engineering change requests" });
    expect(table).toHaveTextContent("BIKE, WHEEL");
    expect(table).toHaveTextContent("Under review");
    fireEvent.click(screen.getByRole("button", { name: "ECR-a" }));
    expect(opened).toEqual(["a"]);
  });

  it("shows the empty state", async () => {
    stub(() => ok({ items: [] }));
    render(<EcrsPage />);
    expect(await screen.findByText("No engineering change requests match")).toBeInTheDocument();
  });

  it("shows an error with retry", async () => {
    let calls = 0;
    stub(() => (calls++ === 0 ? problem(500, "X", "Server broke") : ok({ items: [ecr("a")] })));
    render(<EcrsPage />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("ECR-a");
  });

  it("filters by state and product, pages with the cursor and offers create", async () => {
    const fetchMock = stub((url) => (url.includes("cursor=c1") ? ok({ items: [ecr("b")] }) : ok({ items: [ecr("a")], nextCursor: "c1" })));
    const created = vi.fn();
    render(<EcrsPage onCreate={created} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("ECR-b");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecrs?cursor=c1", expect.anything());
    fireEvent.change(screen.getByLabelText(/State/), { target: { value: "approved" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecrs?state=approved", expect.anything()));
    fireEvent.change(screen.getByLabelText(/Affected product/), { target: { value: " BIKE " } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecrs?state=approved&productId=BIKE", expect.anything()));
    fireEvent.click(screen.getByRole("button", { name: "New ECR" }));
    expect(created).toHaveBeenCalled();
  });
});

describe("EcrPage", () => {
  it("submits a draft", async () => {
    const fetchMock = stub((url) => (url.endsWith(":submit") ? ok(ecr("a", { state: "submitted" })) : ok(ecr("a"))));
    render(<EcrPage id="a" canWrite />);
    await findTable({ name: "Affected products" });
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    fireEvent.change(screen.getByLabelText(/Note for the decision/), { target: { value: "draft note" } });
    fireEvent.click(screen.getByRole("button", { name: "Submit for review" }));
    await waitFor(() => expect(screen.getByText("ECR ECR-a is submitted")).toBeInTheDocument());
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe("/api/v1/engineering/ecrs/a:submit");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({});
    expect(screen.queryByRole("button", { name: "Submit for review" })).toBeNull();
  });

  it("rejects an ECR under review with the note", async () => {
    const fetchMock = stub((url) => (url.endsWith(":reject") ? ok(ecr("a", { state: "rejected", decidedBy: "carol", decisionReason: "too costly" })) : ok(ecr("a", { state: "under_review" }))));
    render(<EcrPage id="a" canReview />);
    await findTable({ name: "Affected products" });
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
    fireEvent.change(screen.getByLabelText(/Note for the decision/), { target: { value: " too costly " } });
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(screen.getByText("ECR ECR-a is rejected")).toBeInTheDocument());
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({ reason: "too costly" });
    expect(screen.getByText("too costly")).toBeInTheDocument();
  });

  it("shows the service refusal and reloads the current state", async () => {
    let state = "under_review";
    stub((url) => {
      if (url.endsWith(":approve")) {
        state = "rejected";
        return problem(403, "ENG-ECR-SELF-APPROVAL", "Forbidden", { detail: "the user who requested ECR ECR-a cannot review or decide it" });
      }
      return ok(ecr("a", { state }));
    });
    render(<EcrPage id="a" canReview />);
    await findTable({ name: "Affected products" });
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await findAlert()).toHaveTextContent("cannot review or decide it");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Approve" })).toBeNull());
  });

  it("shows the refusal when someone other than the requester withdraws", async () => {
    stub((url) => (url.endsWith(":cancel") ? problem(403, "ENG-ECR-NOT-REQUESTER", "Forbidden", { detail: "only rita, who requested ECR ECR-a, can withdraw it; a reviewer rejects it instead" }) : ok(ecr("a", { state: "submitted" }))));
    render(<EcrPage id="a" canWrite />);
    await findTable({ name: "Affected products" });
    fireEvent.click(screen.getByRole("button", { name: "Withdraw" }));
    expect(await findAlert()).toHaveTextContent("only rita, who requested ECR ECR-a, can withdraw it");
    expect(screen.getByRole("button", { name: "Withdraw" })).toBeInTheDocument();
  });

  it("offers to create a change order from an approved ECR and links an existing one", async () => {
    stub(() => ok(ecr("a", { state: "approved" })));
    const create = vi.fn();
    const first = render(<EcrPage id="a" canWrite onCreateEco={create} />);
    fireEvent.click(await screen.findByRole("button", { name: "Create change order" }));
    expect(create).toHaveBeenCalledWith("a");
    first.unmount();
    stub(() => ok(ecr("a", { state: "approved", ecoId: "eco1" })));
    const open = vi.fn();
    render(<EcrPage id="a" canWrite onCreateEco={create} onOpenEco={open} />);
    fireEvent.click(await screen.findByRole("button", { name: "Open change order eco1" }));
    expect(open).toHaveBeenCalledWith("eco1");
    expect(screen.queryByRole("button", { name: "Create change order" })).toBeNull();
  });

  it("reports a missing ECR and a failed load with retry", async () => {
    stub(() => problem(404, "ENG-ECR-NOTFOUND", "Not Found"));
    const first = render(<EcrPage id="zzz" />);
    expect(await findAlert()).toHaveTextContent("This ECR does not exist");
    first.unmount();
    let calls = 0;
    stub(() => (calls++ === 0 ? problem(500, "X", "Server broke") : ok(ecr("a"))));
    render(<EcrPage id="a" />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("ECR ECR-a");
  });

  it("shows a loading skeleton first", () => {
    stub(() => new Promise(() => undefined));
    render(<EcrPage id="a" />);
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByText("Engineering change request")).toBeInTheDocument();
  });
});

describe("EcrCreatePage", () => {
  it("posts the request with an idempotency key, splitting the products", async () => {
    const fetchMock = stub(() => ok(ecr("new"), 201));
    const created = vi.fn();
    render(<EcrCreatePage onCreated={created} />);
    fireEvent.change(screen.getByLabelText(/ECR number/), { target: { value: " ECR-9 " } });
    fireEvent.change(screen.getByLabelText(/Title/), { target: { value: "Swap the wheel" } });
    fireEvent.change(screen.getByLabelText(/Priority/), { target: { value: "critical" } });
    fireEvent.change(screen.getByLabelText(/Affected products/), { target: { value: "BIKE, WHEEL  FRAME" } });
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "crack" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ECR" }));
    await waitFor(() => expect(created).toHaveBeenCalledWith("new"));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/ecrs");
    expect(JSON.parse(String(init?.body))).toEqual({ number: "ECR-9", title: "Swap the wheel", reason: "crack", priority: "critical", products: ["BIKE", "WHEEL", "FRAME"] });
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("shows field errors and a missing duty", async () => {
    let status = 400;
    stub(() => (status === 400 ? problem(400, "COM-0001", "Bad", { detail: "request failed validation", errors: { products: "must list 1 to 100 affected products" } }) : problem(403, "AUTH-0003", "Forbidden")));
    render(<EcrCreatePage />);
    fireEvent.change(screen.getByLabelText(/ECR number/), { target: { value: "E" } });
    fireEvent.change(screen.getByLabelText(/Title/), { target: { value: "t" } });
    fireEvent.change(screen.getByLabelText(/Affected products/), { target: { value: "A" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ECR" }));
    expect(await screen.findByText("must list 1 to 100 affected products")).toBeInTheDocument();
    status = 403;
    fireEvent.click(screen.getByRole("button", { name: "Create ECR" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("You do not have permission to raise ECRs"));
  });
});
