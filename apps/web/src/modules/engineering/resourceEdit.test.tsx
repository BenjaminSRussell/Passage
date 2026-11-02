import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ResourceDetailPage } from "./resources";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
const problem = (status: number, code: string, detail: string) => ({ ok: false, status, json: async () => ({ code, detail, title: "x" }) });
const mill = { id: "a", code: "M1", name: "Mill", type: "machine", siteId: "S1", exclusive: false, efficiencyPct: "90", capacityPerDayH: "8", version: 3, capabilities: [] };

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const open = (props: { onDeleted?: () => void } = {}) =>
  render(
    <MemoryRouter>
      <ResourceDetailPage id="a" canWrite {...props} />
    </MemoryRouter>,
  );

describe("resource edit and delete", () => {
  it("saves the edited fields with the loaded version and reloads the resource", async () => {
    let saved = mill;
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") {
        saved = { ...mill, ...JSON.parse(String(init.body)), version: 4 };
        return ok(saved);
      }
      return ok(saved);
    });
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Edit resource" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Mill 2" } });
    fireEvent.click(screen.getByLabelText("Exclusive"));
    fireEvent.click(screen.getByRole("button", { name: "Save resource" }));
    expect(await screen.findByText("Mill 2")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT")!;
    expect(String(put[0])).toContain("/resources/a");
    expect(JSON.parse(String(put[1]?.body))).toMatchObject({ name: "Mill 2", exclusive: true, version: 3, code: "M1", siteId: "S1" });
    expect((put[1]?.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save resource" })).toBeNull();
  });

  it("tells the editor to reload after a version conflict and keeps the form open", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(409, "ENG-RES-VERSION-CONFLICT", "stale") : ok(mill)));
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Edit resource" }));
    fireEvent.click(screen.getByRole("button", { name: "Save resource" }));
    expect(await findAlert()).toHaveTextContent("Someone else saved this resource first");
    expect(screen.getByRole("button", { name: "Save resource" })).toBeEnabled();
  });

  it("confirms before deleting and calls back when the resource is gone", async () => {
    const onDeleted = vi.fn();
    const fetchMock = stub((_url, init) => (init?.method === "DELETE" ? ok(undefined, 204) : ok(mill)));
    open({ onDeleted });
    fireEvent.click(await screen.findByRole("button", { name: "Delete resource" }));
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
  });

  it("shows why a resource in use cannot be deleted", async () => {
    stub((_url, init) => (init?.method === "DELETE" ? problem(409, "ENG-RES-IN-USE", "resource M1 is required by active route BIKE R1 v1; change the route first") : ok(mill)));
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Delete resource" }));
    fireEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
    expect(await findAlert()).toHaveTextContent("required by active route BIKE R1 v1");
    expect(screen.getByRole("button", { name: "Delete resource" })).toBeInTheDocument();
  });

  it("reports a missing permission on save", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(403, "AUTH-0003", "forbidden") : ok(mill)));
    open();
    fireEvent.click(await screen.findByRole("button", { name: "Edit resource" }));
    fireEvent.click(screen.getByRole("button", { name: "Save resource" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to change resources.");
  });
});

describe("resource edit and delete are gated on the write duty", () => {
  it("hides Edit resource and Delete resource without canWrite", async () => {
    stub(() => ({ ok: true, status: 200, json: async () => mill }));
    render(
      <MemoryRouter>
        <ResourceDetailPage id="a" />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("heading", { name: /Resource/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit resource" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete resource" })).toBeNull();
  });
});
