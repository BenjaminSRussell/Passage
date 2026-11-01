import { findAlert } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RouteVersionEditPage, describeOperation, diffOperations } from "./routeEdit";
import { RouteVersionPage } from "./screens";
import { updateRouteVersion } from "./api/client";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) => ({ ok: false, status, json: async () => ({ code, title: "Err", detail, errors }) });
const op = (no: number, nextNo: number, runH = "2", extra: object = {}) => ({
  no, nextNo, operationId: `OP${no}`, linkType: "hard", priority: "primary", setupH: "0.5", runH, processQty: "1", queueBeforeH: "1", requirements: [], ...extra,
});
const draft = (version = 3, operations = [op(10, 20), op(20, 0, "2", { requirements: [{ kind: "capability", refId: "weld", level: 2, quantity: 1 }, { kind: "type", refId: "machine", quantity: 1 }] })], status = "draft") => ({
  id: "r1", productId: "BIKE", siteId: "S1", routeCode: "R1", versionNo: 2, effectiveFrom: "2026-01-01", status, version, operationCount: operations.length, operations,
});

function stub(handler: (url: string, init?: RequestInit) => unknown) {
  const fetchMock = vi.fn(async (url: string, init?: RequestInit) => handler(url, init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const puts = (fetchMock: ReturnType<typeof stub>) => fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === "PUT");

describe("updateRouteVersion client", () => {
  it("puts the body to the escaped id with an idempotency key", async () => {
    const fetchMock = stub(() => ok(draft(4)));
    await updateRouteVersion("a/b", { productId: "BIKE", siteId: "S1", routeCode: "R1", effectiveFrom: "2026-01-01", operations: [], version: 3 }, "k1");
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/engineering/route-versions/a%2Fb");
    expect(init.method).toBe("PUT");
    expect(new Headers(init.headers).get("Idempotency-Key")).toBe("k1");
    expect(JSON.parse(init.body as string)).toMatchObject({ version: 3 });
  });
});

describe("diffOperations", () => {
  it("compares decimals by value and fields the form does not show", () => {
    expect(diffOperations([op(10, 20, "2.50")], [op(10, 20, "2.5")])).toEqual([]);
    expect(diffOperations([op(10, 20)], [op(10, 20, "2", { priority: "secondary1" })]).map((r) => r.no)).toEqual([10]);
    expect(diffOperations([op(10, 20)], [op(10, 20, "2", { queueBeforeH: "4" })]).map((r) => r.no)).toEqual([10]);
  });

  it("flags a change in every stored field, and treats equal decimals and reordered requirements as the same", () => {
    const changes: object[] = [
      { operationId: "CUT" }, { nextNo: 30 }, { linkType: "soft" }, { priority: "secondary1" }, { setupH: "0.75" }, { runH: "3" }, { processQty: "2" },
      { queueBeforeH: "4" }, { queueAfterH: "1" }, { transitH: "0.25" }, { efficiencyPct: "90" }, { overlapQty: "5" }, { scrapPercent: "2.5" },
      { requirements: [{ kind: "capability", refId: "weld", level: 1, quantity: 1 }] },
      { requirements: [{ kind: "capability", refId: "weld", level: 0, quantity: 2 }] },
    ];
    for (const change of changes) {
      expect(diffOperations([op(10, 20)], [op(10, 20, "2", change)]), JSON.stringify(change)).toHaveLength(1);
    }
    const reqs = [{ kind: "type", refId: "machine", quantity: 1 }, { kind: "capability", refId: "weld", level: 2, quantity: 1 }];
    expect(
      diffOperations(
        [op(10, 20, "2", { requirements: reqs, overlapQty: "0.5", scrapPercent: "1.000000", efficiencyPct: "0" })],
        [op(10, 20, "2.0000", { requirements: [...reqs].reverse(), overlapQty: ".5", scrapPercent: "1", efficiencyPct: undefined })],
      ),
    ).toEqual([]);
  });

  it("describes every field that differs from the default, so a flagged row never reads the same twice", () => {
    const changed = op(10, 0, "2", { linkType: "soft", queueAfterH: "1.5", transitH: "0.25", efficiencyPct: "90", overlapQty: "5", scrapPercent: "2.5" });
    expect(describeOperation(changed)).toBe(
      "OP10: setup 0.5 h, run 2 h, per 1, last, soft link, queue before 1 h, queue after 1.5 h, transit 0.25 h, efficiency 90%, overlap 5, scrap 2.5%",
    );
    expect(describeOperation(op(10, 20, "2", { requirements: [{ kind: "group", refId: "G1", quantity: 2 }] }))).toContain("then 20, queue before 1 h, needs 2 x group G1");
    expect(describeOperation(undefined)).toBe("none");
  });

  it("lists only the operations that differ, added, removed or changed", () => {
    const rows = diffOperations([op(10, 20), op(20, 0, "9"), op(40, 0)], [op(10, 20, "2.0"), op(20, 0), op(30, 0)]);
    expect(rows.map((r) => r.no)).toEqual([20, 30, 40]);
    expect(rows[1].yours).toBeUndefined();
    expect(rows[2].server).toBeUndefined();
  });
});

describe("RouteVersionEditPage", () => {
  it("shows a loading skeleton, then the loaded dates and operations", async () => {
    stub(() => ok(draft()));
    render(<RouteVersionEditPage id="r1" />);
    expect(screen.queryByLabelText(/Run h 1/)).toBeNull();
    expect(await screen.findByLabelText(/Run h 1/)).toHaveValue(2);
    expect(screen.getByLabelText(/Capability 2/)).toHaveValue("weld");
    expect(screen.getByLabelText(/Effective from/)).toHaveValue("2026-01-01");
  });

  it("saves every operation with the loaded version, keeping links and fields the form does not edit", async () => {
    const onSaved = vi.fn();
    const fetchMock = stub((_url, init) => (init?.method === "PUT" ? ok(draft(4)) : ok(draft())));
    render(<RouteVersionEditPage id="r1" onSaved={onSaved} />);
    fireEvent.change(await screen.findByLabelText(/Run h 1/), { target: { value: "6" } });
    fireEvent.change(screen.getByLabelText(/Capability level 2/), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Add operation" }));
    fireEvent.change(screen.getByLabelText(/Run h 3/), { target: { value: "1.5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("r1"));
    const body = JSON.parse(puts(fetchMock)[0][1]!.body as string);
    expect(body).toMatchObject({ version: 3, productId: "BIKE", siteId: "S1", routeCode: "R1", effectiveFrom: "2026-01-01" });
    expect(body.operations.map((o: { no: number; nextNo: number }) => [o.no, o.nextNo])).toEqual([[10, 20], [20, 30], [30, 0]]);
    expect(body.operations[0]).toMatchObject({ runH: "6", queueBeforeH: "1", priority: "primary" });
    expect(body.operations[1].requirements).toEqual([
      { kind: "type", refId: "machine", quantity: 1 },
      { kind: "capability", refId: "weld", level: 3, quantity: 1 },
    ]);
    expect(body.operations[2]).toMatchObject({ runH: "1.5" });
  });

  it("removing an operation reconnects the one that pointed at it", async () => {
    const fetchMock = stub((_url, init) => (init?.method === "PUT" ? ok(draft(4)) : ok(draft(3, [op(10, 20), op(20, 30), op(30, 0)]))));
    render(<RouteVersionEditPage id="r1" />);
    await screen.findByLabelText(/Run h 3/);
    fireEvent.click(screen.getByRole("button", { name: "Remove operation 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(1));
    const body = JSON.parse(puts(fetchMock)[0][1]!.body as string);
    expect(body.operations.map((o: { no: number; nextNo: number }) => [o.no, o.nextNo])).toEqual([[10, 30], [30, 0]]);
  });

  it("shows field errors from the service", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(400, "COM-0001", "request failed validation", { effectiveFrom: "is required as YYYY-MM-DD" }) : ok(draft())));
    render(<RouteVersionEditPage id="r1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect(await findAlert()).toHaveTextContent("request failed validation");
    expect(await screen.findByText("is required as YYYY-MM-DD")).toBeInTheDocument();
  });

  it("reports a 403 as a permission message", async () => {
    stub((_url, init) => (init?.method === "PUT" ? problem(403, "AUTH-0003", "forbidden") : ok(draft())));
    render(<RouteVersionEditPage id="r1" />);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect(await findAlert()).toHaveTextContent("You do not have permission to change routes");
  });

  it("explains a version conflict with the operations that differ and lets the editor keep their edit", async () => {
    let served = draft(3);
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return puts(fetchMock).length === 1 ? problem(409, "ENG-ROUTE-VERSION-CONFLICT", "the draft is at version 4") : ok(draft(5));
      return ok(served);
    });
    render(<RouteVersionEditPage id="r1" />);
    fireEvent.change(await screen.findByLabelText(/Run h 1/), { target: { value: "6" } });
    served = draft(4, [op(10, 20), op(20, 0, "3"), op(30, 0)]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    const table = await screen.findByRole("table", { name: "Operations that differ" });
    expect(screen.getByText("Someone else saved this draft first")).toBeInTheDocument();
    expect(within(table).getByText(/OP10: setup 0.5 h, run 6 h/)).toBeInTheDocument();
    expect(within(table).getAllByRole("row")).toHaveLength(4);
    fireEvent.click(screen.getByRole("button", { name: "Keep my values and continue over version 4" }));
    expect(await screen.findByLabelText(/Run h 1/)).toHaveValue(6);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(2));
    expect(JSON.parse(puts(fetchMock)[1][1]!.body as string).version).toBe(4);
  });

  it("takes the saved operation or the saved dates row by row, and saves the merge over the newer version", async () => {
    let served = draft(3);
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return puts(fetchMock).length === 1 ? problem(409, "ENG-ROUTE-VERSION-CONFLICT", "stale") : ok(draft(5));
      return ok(served);
    });
    render(<RouteVersionEditPage id="r1" />);
    fireEvent.change(await screen.findByLabelText(/Run h 1/), { target: { value: "6" } });
    fireEvent.change(screen.getByLabelText(/Run h 2/), { target: { value: "7" } });
    served = { ...draft(4, [op(10, 20, "2", { transitH: "3" }), op(20, 0, "2", { scrapPercent: "4" })]), effectiveFrom: "2026-02-01" };
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    const table = await screen.findByRole("table", { name: "Operations that differ" });
    expect(within(table).getByText(/transit 3 h/)).toBeInTheDocument();
    expect(screen.getByText(/effective from: yours "2026-01-01", saved "2026-02-01"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Use saved operation 20" }));
    fireEvent.click(screen.getByRole("button", { name: "Use saved dates" }));
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(screen.queryByText(/effective from: yours/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Keep my values and continue over version 4" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(2));
    const body = JSON.parse(puts(fetchMock)[1][1]!.body as string);
    expect(body).toMatchObject({ version: 4, effectiveFrom: "2026-02-01" });
    expect(body.operations[0]).toMatchObject({ no: 10, runH: "6" });
    expect(body.operations[0].transitH).toBeUndefined();
    expect(body.operations[1]).toMatchObject({ no: 20, runH: "2", scrapPercent: "4" });
  });

  it("removes an operation the newer draft dropped when the editor takes the saved side", async () => {
    let served = draft(3);
    const fetchMock = stub((_url, init) => {
      if (init?.method === "PUT") return puts(fetchMock).length === 1 ? problem(409, "ENG-ROUTE-VERSION-CONFLICT", "stale") : ok(draft(5));
      return ok(served);
    });
    render(<RouteVersionEditPage id="r1" />);
    await screen.findByLabelText(/Run h 2/);
    served = draft(4, [op(10, 0)]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    fireEvent.click(await screen.findByRole("button", { name: "Use saved operation 10" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove operation 20 as saved" }));
    fireEvent.click(screen.getByRole("button", { name: "Continue editing version 4" }));
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(puts(fetchMock)).toHaveLength(2));
    expect(JSON.parse(puts(fetchMock)[1][1]!.body as string).operations.map((o: { no: number; nextNo: number }) => [o.no, o.nextNo])).toEqual([[10, 0]]);
  });

  it("lets the editor discard their edit and load the newer draft", async () => {
    let served = draft(3);
    stub((_url, init) => (init?.method === "PUT" ? problem(409, "ENG-ROUTE-VERSION-CONFLICT", "stale") : ok(served)));
    render(<RouteVersionEditPage id="r1" />);
    fireEvent.change(await screen.findByLabelText(/Operation id 1/), { target: { value: "CUT" } });
    served = draft(4, [op(10, 0, "9")]);
    fireEvent.click(screen.getByRole("button", { name: "Save draft" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard my edits and load version 4" }));
    expect(await screen.findByLabelText(/Operation id 1/)).toHaveValue("OP10");
    expect(screen.queryByLabelText(/Operation id 2/)).toBeNull();
  });

  it("refuses to edit a version that is not a draft", async () => {
    stub(() => ok(draft(3, [op(10, 0)], "active")));
    render(<RouteVersionEditPage id="r1" onCancel={() => {}} />);
    expect(await findAlert()).toHaveTextContent("Only a draft can be edited; this version is active");
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
  });

  it("shows a not-found message and a retry for a failed load", async () => {
    stub(() => problem(404, "ENG-ROUTE-NOTFOUND", "route version not found"));
    const { unmount } = render(<RouteVersionEditPage id="nope" />);
    expect(await findAlert()).toHaveTextContent("This route version does not exist");
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
    unmount();
    stub(() => problem(500, "COM-9999", "boom"));
    render(<RouteVersionEditPage id="r1" />);
    expect(await findAlert()).toHaveTextContent("boom");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});

describe("RouteVersionPage edit control", () => {
  it("offers Edit draft only for a draft and only when the page can open it", async () => {
    const onEdit = vi.fn();
    stub(() => ok(draft()));
    const { unmount } = render(<RouteVersionPage id="r1" onEdit={onEdit} />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit draft" }));
    expect(onEdit).toHaveBeenCalledWith("r1");
    expect(screen.getByText("Draft version")).toBeInTheDocument();
    unmount();
    stub(() => ok(draft(3, [op(10, 0)], "active")));
    render(<RouteVersionPage id="r1" onEdit={onEdit} />);
    await screen.findByText("Route R1");
    expect(screen.queryByRole("button", { name: "Edit draft" })).toBeNull();
  });
});
