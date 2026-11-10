import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { validateCriteria, newCriterion } from "./RfqTypePages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const TYPE_ID = "44444444-4444-4444-8444-444444444444";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const sealedType = {
  id: TYPE_ID,
  legalEntityId: LE,
  code: "SEALED",
  name: "Sealed bid",
  sealed: true,
  amendmentsAllowed: false,
  rankingRule: "WEIGHTED_SCORE",
  status: "ACTIVE",
  criteria: [
    { id: "c1", seq: 1, name: "Quality", weight: "2" },
    { id: "c2", seq: 2, name: "Delivery", weight: "1" },
  ],
  version: 1,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};
const body = (init: RequestInit) => JSON.parse(String(init.body));

describe("validateCriteria", () => {
  it("wants a criterion for weighted ranking, unique names and positive weights", () => {
    expect(validateCriteria("WEIGHTED_SCORE", [])).toHaveProperty("criteria");
    expect(validateCriteria("LOWEST_PRICE", [])).toEqual({});
    const dup = [newCriterion("Quality", "1"), newCriterion("quality", "0")];
    const errors = validateCriteria("LEAD_TIME", dup);
    expect(errors["criteria[1].name"]).toBe("Use each name once.");
    expect(errors["criteria[1].weight"]).toBe("Enter a positive weight.");
  });
});

describe("RFQ types list", () => {
  it("shows a loading table, then the types, and opens one", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((r) => (resolve = r)));
    const { router } = renderAt("/purchasing/rfq-types");
    expect(await screen.findByRole("table", { name: "RFQ types" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [sealedType] }));
    const row = await screen.findByText("SEALED");
    expect(screen.getByText("Not allowed")).toBeInTheDocument();
    fireEvent.click(row);
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/rfq-types/${TYPE_ID}`));
  });

  it("explains the empty state without accessibility violations", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/rfq-types");
    expect(await screen.findByText("No RFQ types yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem code and retries, then filters by status and pages", async () => {
    const mock = stubFetch(() => problem(403, "AUTH-0003", "missing duty"));
    renderAt("/purchasing/rfq-types");
    expect(await screen.findByText(/AUTH-0003/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "INACTIVE" } });
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("status=INACTIVE"))).toBe(true));
  });
});

describe("RFQ type create", () => {
  it("validates before sending", async () => {
    const mock = stubFetch(() => json({}));
    const { container } = renderAt("/purchasing/rfq-types/new");
    fireEvent.change(await screen.findByLabelText("Rank replies by"), { target: { value: "WEIGHTED_SCORE" } });
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ type" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Use 1 to 20 letters, digits, _ or -.")).toBeInTheDocument();
    expect(screen.getByText("Enter the name.")).toBeInTheDocument();
    expect(screen.getAllByText("Add at least one scoring criterion to rank by weighted score.").length).toBeGreaterThan(0);
    expect(mock).not.toHaveBeenCalled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts the type with its criteria and opens it", async () => {
    const mock = stubFetch(() => json(sealedType, 201));
    const { router } = renderAt("/purchasing/rfq-types/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "sealed" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Sealed bid" } });
    fireEvent.click(screen.getByLabelText(/Sealed bids/));
    fireEvent.click(screen.getByLabelText(/Amendments allowed/));
    fireEvent.change(screen.getByLabelText("Rank replies by"), { target: { value: "WEIGHTED_SCORE" } });
    fireEvent.click(screen.getByRole("button", { name: "Add criterion" }));
    const first = screen.getByRole("group", { name: "Criterion 1" });
    fireEvent.change(within(first).getByLabelText("Criterion"), { target: { value: "Quality" } });
    fireEvent.change(within(first).getByLabelText("Weight"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ type" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/rfq-types/${TYPE_ID}`));
    const [url, init] = mock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${BASE}/rfq-types`);
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(body(init)).toMatchObject({
      legalEntityId: LE,
      code: "SEALED",
      name: "Sealed bid",
      sealed: true,
      amendmentsAllowed: false,
      rankingRule: "WEIGHTED_SCORE",
      criteria: [{ name: "Quality", weight: "2" }],
    });
  });

  it("shows the service's refusal with its code", async () => {
    stubFetch(() => problem(409, "PRC-RFQ-TYPE-002", "the legal entity already has an RFQ type SEALED"));
    renderAt("/purchasing/rfq-types/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "SEALED" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Sealed bid" } });
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ type" }));
    expect(await screen.findByText(/PRC-RFQ-TYPE-002/)).toBeInTheDocument();
  });
});

describe("RFQ type detail", () => {
  it("changes the type under its version and offers a reload when it is stale", async () => {
    let call = 0;
    const mock = stubFetch((_url, init) => {
      if (init.method === "PUT") {
        call += 1;
        return call === 1 ? json({ ...sealedType, name: "Sealed tender", version: 2 }, 200) : problem(412, "PRC-RFQ-TYPE-003", "stale");
      }
      return json(sealedType, 200, { ETag: '"1"' });
    });
    const { container } = renderAt(`/purchasing/rfq-types/${TYPE_ID}`);
    expect(await screen.findByText("RFQ type SEALED")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Sealed tender" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("RFQ type saved as version 2.")).toBeInTheDocument();
    const put = mock.mock.calls.find((c) => (c[1] as RequestInit).method === "PUT") as [string, RequestInit];
    expect(put[0]).toBe(`${BASE}/rfq-types/${TYPE_ID}`);
    expect((put[1].headers as Record<string, string>)["If-Match"]).toBe('"1"');
    expect(body(put[1]).criteria).toEqual([
      { name: "Quality", weight: "2" },
      { name: "Delivery", weight: "1" },
    ]);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/Someone else changed this RFQ type/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload RFQ type" })).toBeInTheDocument();
  });

  it("explains a type that cannot be loaded", async () => {
    stubFetch(() => problem(404, "PRC-RFQ-TYPE-NOT-FOUND", "RFQ type not found"));
    renderAt(`/purchasing/rfq-types/${TYPE_ID}`);
    expect(await screen.findByText(/PRC-RFQ-TYPE-NOT-FOUND/)).toBeInTheDocument();
  });
});
