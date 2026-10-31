import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RouteVersionCreatePage } from "./routeCreate";
import { RouteVersionsPage } from "./screens";

afterEach(() => vi.unstubAllGlobals());

const problem = (status: number, code: string, title: string, extra: object = {}) => ({ ok: false, status, json: async () => ({ code, title, ...extra }) });

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function fill() {
  fireEvent.change(screen.getByLabelText(/^Product/), { target: { value: "BIKE" } });
  fireEvent.change(screen.getByLabelText(/^Site/), { target: { value: "S1" } });
  fireEvent.change(screen.getByLabelText(/^Route code/), { target: { value: "R1" } });
  fireEvent.change(screen.getByLabelText(/Effective from/), { target: { value: "2026-07-01" } });
}

const submitButton = () => screen.getByRole("button", { name: "Create route version" });

describe("RouteVersionCreatePage", () => {
  it("posts the operations in order, chained by next number, with an idempotency key", async () => {
    const fetchMock = stub(() => ({ ok: true, status: 201, json: async () => ({ id: "new" }) }));
    const created: string[] = [];
    render(<RouteVersionCreatePage onCreated={(id) => created.push(id)} />);
    fill();
    fireEvent.change(screen.getByLabelText(/Capability 1/), { target: { value: "weld" } });
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    fireEvent.change(screen.getByLabelText(/Run h 2/), { target: { value: "1.5" } });
    fireEvent.click(submitButton());
    await waitFor(() => expect(created).toEqual(["new"]));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/engineering/route-versions");
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBeTruthy();
    const body = JSON.parse(init?.body as string);
    expect(body).toMatchObject({ productId: "BIKE", siteId: "S1", routeCode: "R1", effectiveFrom: "2026-07-01" });
    expect(body.operations.map((o: { no: number; nextNo: number }) => [o.no, o.nextNo])).toEqual([[10, 20], [20, 0]]);
    expect(body.operations[0].requirements).toEqual([{ kind: "capability", refId: "weld", level: 1, quantity: 1 }]);
    expect(body.operations[1]).toMatchObject({ runH: "1.5" });
    expect(body.operations[1].requirements).toBeUndefined();
  });

  it("removes an operation and shows a permission error", async () => {
    stub(() => problem(403, "COM-0003", "Forbidden"));
    render(<RouteVersionCreatePage />);
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove operation 2" }));
    expect(screen.queryByLabelText(/Operation no 2/)).toBeNull();
    fireEvent.click(submitButton());
    expect(await findAlert()).toHaveTextContent("You do not have permission to create route versions");
  });

  it("shows the service detail for a rule failure", async () => {
    stub(() => problem(422, "ENG-ROUTE-CYCLE", "Unprocessable Entity", { detail: "operation 10 is part of a cycle" }));
    render(<RouteVersionCreatePage />);
    fill();
    fireEvent.click(submitButton());
    expect(await findAlert()).toHaveTextContent("operation 10 is part of a cycle");
  });

  it("places validation messages on header fields and the failing operation", async () => {
    stub(() =>
      problem(400, "COM-0001", "Bad Request", {
        detail: "request failed validation",
        errors: { effectiveTo: "must not be before effectiveFrom", "operations[0].no": "must be 1 or more" },
      }),
    );
    render(<RouteVersionCreatePage />);
    fill();
    fireEvent.click(submitButton());
    expect(await screen.findByText("must not be before effectiveFrom")).toBeInTheDocument();
    expect(screen.getByText("must be 1 or more")).toBeInTheDocument();
  });

  it("lists a validation message with no matching field in the page alert", async () => {
    stub(() => problem(400, "COM-0001", "Bad Request", { detail: "request failed validation", errors: { versionNo: "must be 1 to 2147483647" } }));
    render(<RouteVersionCreatePage />);
    fill();
    fireEvent.click(submitButton());
    expect(await findAlert()).toHaveTextContent("request failed validation: versionNo must be 1 to 2147483647");
  });

  it("reuses the idempotency key for a retry of the same body and cancels", async () => {
    const fetchMock = stub(() => problem(500, "X", "Server broke"));
    const cancelled = vi.fn();
    render(<RouteVersionCreatePage onCancel={cancelled} />);
    fill();
    fireEvent.click(submitButton());
    await findAlert();
    fireEvent.click(submitButton());
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const keys = fetchMock.mock.calls.map(([, init]) => new Headers(init?.headers).get("Idempotency-Key"));
    expect(keys[0]).toBe(keys[1]);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(cancelled).toHaveBeenCalled();
  });
});

describe("RouteVersionsPage create action", () => {
  it("offers New route version", async () => {
    stub(() => ({ ok: true, status: 200, json: async () => ({ items: [] }) }));
    const onCreate = vi.fn();
    render(<RouteVersionsPage onCreate={onCreate} />);
    fireEvent.click(screen.getByRole("button", { name: "New route version" }));
    expect(onCreate).toHaveBeenCalled();
    await screen.findByText("No route versions match");
  });
});
