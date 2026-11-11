import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EcoCreatePage, EcoPage, EcosPage } from "./eco";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, title: string, extra: object = {}) => ({ ok: false, status, json: async () => ({ code, title, ...extra }) });
const eco = (id: string, extra: object = {}) => ({ id, number: `ECO-${id}`, state: "submitted", replaces: [{ oldVersionId: "old1", newVersionId: "new1" }], ...extra });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("EcosPage", () => {
  it("shows loading, rows and opens a row", async () => {
    stub(() => ok({ items: [eco("a"), eco("b", { state: "released", decidedBy: "alice", releaseDate: "2026-07-01" })] }));
    const opened: string[] = [];
    render(<EcosPage onOpen={(id) => opened.push(id)} />);
    expect(screen.getByRole("table", { name: "Engineering change orders" })).toHaveAttribute("aria-busy", "true");
    const table = await findTable({ name: "Engineering change orders" });
    expect(table).toHaveTextContent("alice");
    expect(table).toHaveTextContent("2026-07-01");
    fireEvent.click(screen.getByRole("button", { name: "ECO-a" }));
    expect(opened).toEqual(["a"]);
  });

  it("shows the empty state", async () => {
    stub(() => ok({ items: [] }));
    render(<EcosPage />);
    expect(await screen.findByText("No engineering change orders match")).toBeInTheDocument();
  });

  it("shows an error with retry", async () => {
    let calls = 0;
    stub(() => (calls++ === 0 ? problem(500, "X", "Server broke") : ok({ items: [eco("a")] })));
    render(<EcosPage />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByText("ECO-a");
  });

  it("filters by state, pages with the cursor and offers create", async () => {
    const fetchMock = stub((url) => (url.includes("cursor=c1") ? ok({ items: [eco("b")] }) : ok({ items: [eco("a")], nextCursor: "c1" })));
    const created = vi.fn();
    render(<EcosPage onCreate={created} />);
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("ECO-b");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecos?cursor=c1", expect.anything());
    fireEvent.change(screen.getByLabelText(/State/), { target: { value: "released" } });
    await waitFor(() => expect(fetchMock).toHaveBeenLastCalledWith("/api/v1/engineering/ecos?state=released", expect.anything()));
    fireEvent.click(screen.getByRole("button", { name: "New ECO" }));
    expect(created).toHaveBeenCalled();
  });
});

describe("EcoPage change request link", () => {
  it("links an ECO to the ECR it carries out", async () => {
    stub(() => ok(eco("a", { ecrId: "ecr-1" })));
    const opened: string[] = [];
    render(<EcoPage id="a" onOpenEcr={(id) => opened.push(id)} />);
    fireEvent.click(await screen.findByRole("button", { name: "Open change request ecr-1" }));
    expect(opened).toEqual(["ecr-1"]);
  });
});

describe("EcoPage", () => {
  it("shows the facts, links to versions and releases with an idempotency key", async () => {
    const fetchMock = stub((url) => (url.endsWith(":release") ? ok(eco("a", { state: "released", decidedBy: "alice" })) : ok(eco("a"))));
    const opened: string[] = [];
    render(<EcoPage id="a" onOpenBom={(v) => opened.push(v)} />);
    await findTable({ name: "Replacements" });
    fireEvent.click(screen.getByRole("button", { name: "Open new new1" }));
    expect(opened).toEqual(["new1"]);
    fireEvent.click(screen.getByRole("button", { name: "Approve and release" }));
    await waitFor(() => expect(screen.getByText("ECO ECO-a is released")).toBeInTheDocument());
    const [url, init] = fetchMock.mock.calls[1];
    expect(url).toBe("/api/v1/engineering/ecos/a:release");
    expect(init?.method).toBe("POST");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Approve and release" })).toBeNull();
  });

  it("reports a refused release and a missing duty", async () => {
    let status = 503;
    stub((url) => {
      if (!url.endsWith(":release")) return ok(eco("a"));
      return status === 503 ? problem(503, "ENG-ECO-APPROVER-UNAVAILABLE", "x", { detail: "approval service unavailable, release blocked" }) : problem(403, "AUTH-0002", "Forbidden");
    });
    render(<EcoPage id="a" />);
    fireEvent.click(await screen.findByRole("button", { name: "Approve and release" }));
    expect(await findAlert()).toHaveTextContent("approval service unavailable");
    status = 403;
    fireEvent.click(screen.getByRole("button", { name: "Approve and release" }));
    expect(await screen.findByText("You do not have permission to release ECOs")).toBeInTheDocument();
  });

  it("shows the persisted rejection after the approver refuses the release", async () => {
    let state = "submitted";
    stub((url) => {
      if (!url.endsWith(":release")) return ok(eco("a", { state }));
      state = "rejected";
      return problem(422, "ENG-ECO-NOT-APPROVED", "x", { detail: "ECO ECO-a must be approved before release." });
    });
    render(<EcoPage id="a" />);
    fireEvent.click(await screen.findByRole("button", { name: "Approve and release" }));
    expect(await findAlert()).toHaveTextContent("must be approved before release");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Approve and release" })).toBeNull());
    expect(screen.getByText(/^rejected$/i)).toBeInTheDocument();
  });

  it("explains a missing ECO and shows other errors with retry", async () => {
    stub(() => problem(404, "ENG-ECO-NOTFOUND", "ECO not found"));
    const { unmount } = render(<EcoPage id="zzz" />);
    expect(await findAlert()).toHaveTextContent("This ECO does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    unmount();
    stub(() => problem(500, "X", "Server broke"));
    render(<EcoPage id="a" />);
    expect(await findAlert()).toHaveTextContent("Server broke");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("EcoCreatePage", () => {
  function fill() {
    fireEvent.change(screen.getByLabelText(/ECO number/), { target: { value: "ECO-9" } });
    fireEvent.change(screen.getByLabelText(/Old version 1/), { target: { value: "old1" } });
    fireEvent.change(screen.getByLabelText(/New version 1/), { target: { value: "new1" } });
  }

  it("posts the ECO with an idempotency key and reports the new id", async () => {
    const fetchMock = stub(() => ok(eco("new"), 201));
    const created: string[] = [];
    render(<EcoCreatePage onCreated={(id) => created.push(id)} />);
    fill();
    fireEvent.change(screen.getByLabelText(/Release date/), { target: { value: "2026-08-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Add replacement" }));
    fireEvent.change(screen.getByLabelText(/Old version 2/), { target: { value: "old2" } });
    fireEvent.change(screen.getByLabelText(/New version 2/), { target: { value: "new2" } });
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    await waitFor(() => expect(created).toEqual(["new"]));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/ecos");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    expect(JSON.parse(init?.body as string)).toEqual({
      number: "ECO-9",
      releaseDate: "2026-08-01",
      replaces: [{ oldVersionId: "old1", newVersionId: "new1" }, { oldVersionId: "old2", newVersionId: "new2" }],
    });
  });

  it("shows field errors, a duplicate number and a missing duty", async () => {
    let next: unknown = problem(400, "COM-0001", "Bad", { detail: "invalid request", errors: { replaces: "each replacement needs two different valid version ids" } });
    stub(() => next);
    render(<EcoCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    expect(await screen.findByText("invalid request")).toBeInTheDocument();
    expect(screen.getByText("each replacement needs two different valid version ids")).toBeInTheDocument();
    next = problem(403, "AUTH-0002", "Forbidden");
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    expect(await screen.findByText("You do not have permission to create ECOs")).toBeInTheDocument();
  });

  it("puts ENG-ECO-HORIZON on the release date field", async () => {
    stub(() => problem(422, "ENG-ECO-HORIZON", "Horizon", { detail: "Release date is more than 365 days ahead." }));
    render(<EcoCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    expect((await screen.findAllByText("Release date is more than 365 days ahead.")).length).toBeGreaterThan(0);
    expect(screen.getByLabelText(/Release date/)).toHaveAccessibleDescription(/365 days ahead/);
  });

  it("puts ENG-ECO-BACKDATE-DUTY on the release date field, not the permission message", async () => {
    stub(() => problem(403, "ENG-ECO-BACKDATE-DUTY", "Backdate", { detail: "Backdated release requires the Backdate duty." }));
    render(<EcoCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    expect(screen.getByLabelText(/Release date/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByLabelText(/Release date/)).toHaveAccessibleDescription(/requires the Backdate duty/));
    expect(screen.queryByText("You do not have permission to create ECOs")).toBeNull();
  });

  it("carries the change request id from the ECR into the body", async () => {
    const fetchMock = stub(() => ok(eco("new"), 201));
    render(<EcoCreatePage initialEcrId="ecr-1" onCreated={() => undefined} />);
    expect(screen.getByLabelText(/Change request/)).toHaveAttribute("data-value", "ecr-1");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create ECO" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).toMatchObject({ number: "ECO-9", ecrId: "ecr-1" });
  });

  it("removes a replacement row and cancels", () => {
    const cancel = vi.fn();
    render(<EcoCreatePage onCancel={cancel} />);
    fireEvent.click(screen.getByRole("button", { name: "Add replacement" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove replacement 2" }));
    expect(screen.queryByLabelText(/Old version 2/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancel).toHaveBeenCalled();
  });
});
