import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toLocalInput, validateAmendment } from "./RfqAmend";
import type { RFQ } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const RFQ_ID = "99999999-9999-4999-8999-999999999999";
const LINE_1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const VENDOR_A = "22222222-2222-4222-8222-222222222222";
const VENDOR_B = "33333333-3333-4333-8333-333333333333";
const TYPE_ID = "44444444-4444-4444-8444-444444444444";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const base: RFQ = {
  id: RFQ_ID,
  number: "RFQ-00000007",
  legalEntityId: LE,
  status: "DRAFT",
  replyDeadline: "2099-11-01T17:00:00Z",
  rankingRule: "LOWEST_PRICE",
  sealed: false,
  amendmentsAllowed: true,
  amendmentNo: 0,
  amendments: [],
  version: 3,
  lines: [{ id: LINE_1, lineNo: 1, description: "Pump", qty: "10", unit: "EA" }],
  invitations: [],
  replies: [],
  criteria: [{ id: "c1", seq: 1, name: "Quality", weight: "2" }],
  scores: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};
const sent: RFQ = {
  ...base,
  status: "SENT",
  invitations: [
    { vendorId: VENDOR_A, status: "INVITED", sentAt: "2026-10-26T11:00:00Z" },
    { vendorId: VENDOR_B, status: "INVITED", sentAt: "2026-10-26T11:00:00Z" },
  ],
};
const amended: RFQ = {
  ...sent,
  version: 4,
  amendmentNo: 1,
  replyDeadline: "2099-11-08T17:00:00Z",
  amendments: [
    {
      amendmentNo: 1,
      summary: "Delivery moves to April",
      previousDeadline: "2099-11-01T17:00:00Z",
      newDeadline: "2099-11-08T17:00:00Z",
      criteriaChanged: false,
      lateClearedVendorIds: [VENDOR_B],
      issuedAt: "2026-10-27T09:00:00Z",
      acknowledgements: [
        { vendorId: VENDOR_A, acknowledgedAt: "2026-10-27T10:00:00Z" },
        { vendorId: VENDOR_B },
      ],
    },
  ],
};
const body = (init: RequestInit) => JSON.parse(String(init.body));

describe("validateAmendment", () => {
  const now = new Date("2099-10-01T00:00:00Z");
  const form = { summary: "x", deadline: "", replaceCriteria: false, criteria: [] };
  it("wants a summary and a later deadline", () => {
    expect(validateAmendment(sent, form, now)).toEqual({});
    expect(validateAmendment(sent, { ...form, summary: " " }, now).summary).toBe("Say what changed.");
    expect(validateAmendment(sent, { ...form, deadline: "2099-09-01T10:00" }, now).replyDeadline).toMatch(/after now/);
    expect(validateAmendment(sent, { ...form, deadline: "2099-12-01T10:00" }, now)).toEqual({});
  });
  it("needs a new deadline once the old one has passed", () => {
    expect(validateAmendment(sent, form, new Date("2100-01-01T00:00:00Z")).replyDeadline).toMatch(/passed/);
  });
  it("checks replacement criteria against the ranking rule", () => {
    const weighted = { ...sent, rankingRule: "WEIGHTED_SCORE" as const };
    expect(validateAmendment(weighted, { ...form, replaceCriteria: true }, now)).toHaveProperty("criteria");
  });
});

describe("toLocalInput", () => {
  it("formats an instant for a datetime-local field and tolerates junk", () => {
    expect(toLocalInput("2099-11-01T17:00:00Z")).toMatch(/^2099-11-0[12]T\d\d:\d\d$/);
    expect(toLocalInput("nope")).toBe("");
  });
});

describe("edit a draft RFQ", () => {
  it("saves the deadline, ranking, seal and criteria under the RFQ version", async () => {
    const mock = stubFetch((_url, init) =>
      init.method === "PUT" ? json({ ...base, version: 4, sealed: true, rankingRule: "LEAD_TIME" }, 200, { ETag: '"4"' }) : json(base, 200, { ETag: '"3"' }),
    );
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const form = await screen.findByRole("form", { name: "Edit draft" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(within(form).getByLabelText("Reply deadline"), { target: { value: "2099-12-01T09:00" } });
    fireEvent.change(within(form).getByLabelText("Rank replies by"), { target: { value: "LEAD_TIME" } });
    fireEvent.click(within(form).getByLabelText(/Sealed bids/));
    fireEvent.click(within(form).getByRole("button", { name: "Save draft changes" }));
    expect(await screen.findByText("Draft changes saved.")).toBeInTheDocument();
    const put = mock.mock.calls.find((c) => (c[1] as RequestInit).method === "PUT") as [string, RequestInit];
    expect(put[0]).toBe(`${BASE}/rfqs/${RFQ_ID}`);
    expect((put[1].headers as Record<string, string>)["If-Match"]).toBe('"3"');
    expect(body(put[1])).toEqual({
      replyDeadline: new Date("2099-12-01T09:00").toISOString(),
      rankingRule: "LEAD_TIME",
      sealed: true,
      criteria: [{ name: "Quality", weight: "2" }],
    });
  });

  it("locks the sealed flag a type requires and shows a refusal with its code", async () => {
    stubFetch((_url, init) =>
      init.method === "PUT" ? problem(422, "PRC-RFQ-009", "the type of RFQ RFQ-00000007 requires sealed bids") : json({ ...base, sealed: true, sealedRequired: true, rfqTypeId: TYPE_ID }),
    );
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const form = await screen.findByRole("form", { name: "Edit draft" });
    expect(within(form).getByLabelText(/Sealed bids/)).toBeDisabled();
    fireEvent.click(within(form).getByRole("button", { name: "Save draft changes" }));
    expect(await screen.findByText(/PRC-RFQ-009/)).toBeInTheDocument();
  });

  it("does not offer a draft edit once the RFQ is sent", async () => {
    stubFetch(() => json(sent));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByRole("form", { name: "New amendment" })).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Edit draft" })).toBeNull();
  });
});

describe("amend a sent RFQ", () => {
  it("validates, then posts the summary, the new deadline and replacement criteria with the version", async () => {
    const mock = stubFetch((_url, init) => (init.method === "POST" ? json(amended, 201, { ETag: '"4"' }) : json(sent, 200, { ETag: '"3"' })));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const form = await screen.findByRole("form", { name: "New amendment" });
    fireEvent.click(within(form).getByRole("button", { name: "Issue amendment" }));
    expect(await screen.findByText("Say what changed.")).toBeInTheDocument();
    expect(mock.mock.calls.some((c) => (c[1] as RequestInit).method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText("What changed"), { target: { value: "Delivery moves to April" } });
    fireEvent.change(within(form).getByLabelText(/New reply deadline/), { target: { value: "2099-11-08T17:00" } });
    fireEvent.click(within(form).getByLabelText(/Replace the scoring criteria/));
    const crit = await screen.findByRole("group", { name: "Criterion 1" });
    fireEvent.change(within(crit).getByLabelText("Weight"), { target: { value: "3" } });
    fireEvent.click(within(form).getByRole("button", { name: "Issue amendment" }));
    expect(await screen.findByText("Amendment 1 issued to 2 vendors.")).toBeInTheDocument();
    const post = mock.mock.calls.find((c) => (c[1] as RequestInit).method === "POST") as [string, RequestInit];
    expect(post[0]).toBe(`${BASE}/rfqs/${RFQ_ID}/amendments`);
    const headers = post[1].headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"3"');
    expect(headers["Idempotency-Key"]).toBeTruthy();
    expect(body(post[1])).toEqual({
      summary: "Delivery moves to April",
      replyDeadline: new Date("2099-11-08T17:00").toISOString(),
      criteria: [{ name: "Quality", weight: "3" }],
    });
    expect(screen.getByText("RFQ-00000007-A1")).toBeInTheDocument();
    expect(screen.getByText("1 of 2")).toBeInTheDocument();
    // The audit of the moved deadline: vendor B's late reply now counts as on time.
    const amendments = screen.getByRole("table", { name: "Amendments" });
    expect(within(amendments).getByRole("columnheader", { name: "Late replies now on time" })).toBeInTheDocument();
    expect(await within(amendments).findByText(`Vendor ${VENDOR_B.slice(0, 8)}`)).toBeInTheDocument();
  });

  it("locks the scoring criteria once an unsealed RFQ has replies the buyer can read, but not on a sealed one", async () => {
    const reply = { id: "r1", lineId: LINE_1, vendorId: VENDOR_A, unitPrice: "10", priceUnit: "1", currency: "USD", leadTimeDays: 5, receivedAt: "2026-10-27T09:00:00Z", late: false, awarded: false };
    stubFetch(() => json({ ...sent, replies: [reply], replyCount: 1 }));
    const { unmount } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const form = await screen.findByRole("form", { name: "New amendment" });
    expect(within(form).getByLabelText(/Replace the scoring criteria/)).toBeDisabled();
    expect(within(form).getByText(/not possible: replies are already open to you/)).toBeInTheDocument();
    unmount();
    vi.unstubAllGlobals();
    stubFetch(() => json({ ...sent, sealed: true, repliesSealed: true, replies: [], replyCount: 1 }));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const sealedForm = await screen.findByRole("form", { name: "New amendment" });
    expect(within(sealedForm).getByLabelText(/Replace the scoring criteria/)).toBeEnabled();
  });

  it("records a vendor's acknowledgement without moving the RFQ version", async () => {
    const acked = { ...amended, amendments: [{ ...amended.amendments![0], acknowledgements: amended.amendments![0].acknowledgements.map((a) => ({ ...a, acknowledgedAt: "2026-10-28T08:00:00Z" })) }] };
    const mock = stubFetch((_url, init) => (init.method === "PUT" ? json(acked) : json(amended, 200, { ETag: '"4"' })));
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: `Record acknowledgement from Vendor ${VENDOR_B.slice(0, 8)}` }));
    expect(await screen.findByText("2 of 2")).toBeInTheDocument();
    const put = mock.mock.calls.find((c) => (c[1] as RequestInit).method === "PUT") as [string, RequestInit];
    expect(put[0]).toBe(`${BASE}/rfqs/${RFQ_ID}/amendments/1/acknowledgements/${VENDOR_B}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("says when the RFQ type forbids amendments and shows the service's refusal code", async () => {
    stubFetch(() => json({ ...sent, amendmentsAllowed: false }));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByText("The type of this RFQ does not allow amendments.")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "New amendment" })).toBeNull();
  });

  it("shows a refused amendment with its code", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(422, "PRC-RFQ-008", "scores are recorded") : json(sent)));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const form = await screen.findByRole("form", { name: "New amendment" });
    fireEvent.change(within(form).getByLabelText("What changed"), { target: { value: "Clarified" } });
    fireEvent.click(within(form).getByRole("button", { name: "Issue amendment" }));
    expect(await screen.findByText(/PRC-RFQ-008/)).toBeInTheDocument();
  });
});

describe("new RFQ from a type", () => {
  const type = {
    id: TYPE_ID,
    legalEntityId: LE,
    code: "SEALED",
    name: "Sealed bid",
    sealed: true,
    amendmentsAllowed: true,
    rankingRule: "WEIGHTED_SCORE",
    status: "ACTIVE",
    criteria: [{ id: "c1", seq: 1, name: "Quality", weight: "2" }],
    version: 1,
    createdAt: "2026-03-01T10:00:00Z",
    updatedAt: "2026-03-01T10:00:00Z",
  };

  it("fills the bidding rules from the chosen type and sends its id", async () => {
    const mock = stubFetch((url, init) => {
      if (init.method === "POST") return json({ ...base, id: RFQ_ID }, 201);
      if (String(url).includes("/rfq-types")) return json({ items: [type] });
      return json(base);
    });
    const { router } = renderAt("/purchasing/rfqs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    const select = await screen.findByLabelText(/RFQ type/);
    fireEvent.change(select, { target: { value: TYPE_ID } });
    expect(screen.getByLabelText("Rank replies by")).toHaveValue("WEIGHTED_SCORE");
    expect(screen.getByLabelText(/Sealed bids/)).toBeDisabled();
    expect(screen.getByLabelText(/Sealed bids/)).toBeChecked();
    expect(screen.getByRole("group", { name: "Criterion 1" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reply deadline"), { target: { value: "2099-11-01T17:00" } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Pump" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/rfqs/${RFQ_ID}`));
    const post = mock.mock.calls.find((c) => (c[1] as RequestInit).method === "POST") as [string, RequestInit];
    expect(body(post[1])).toMatchObject({ rfqTypeId: TYPE_ID, sealed: true, rankingRule: "WEIGHTED_SCORE", criteria: [{ name: "Quality", weight: "2" }] });
    expect(mock.mock.calls.some(([url]) => String(url).includes(`rfq-types?status=ACTIVE&legalEntityId=${LE}`))).toBe(true);
  });
});
