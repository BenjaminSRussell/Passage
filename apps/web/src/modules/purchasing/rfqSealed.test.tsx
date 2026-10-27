import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { biddingLabel, rankingRules, scorable, validateRFQ } from "./RfqPages";
import type { RFQ } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR_A = "22222222-2222-4222-8222-222222222222";
const VENDOR_B = "33333333-3333-4333-8333-333333333333";
const RFQ_ID = "99999999-9999-4999-8999-999999999999";
const LINE_1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const QUALITY = "c1c1c1c1-c1c1-4c1c-8c1c-c1c1c1c1c1c1";
const DELIVERY = "c2c2c2c2-c2c2-4c2c-8c2c-c2c2c2c2c2c2";
const REPLY_A = "a1a1a1a1-a1a1-4a1a-8a1a-a1a1a1a1a1a1";
const REPLY_B = "b1b1b1b1-b1b1-4b1b-8b1b-b1b1b1b1b1b1";

afterEach(() => vi.unstubAllGlobals());

const criteria = [
  { id: QUALITY, seq: 1, name: "Quality", weight: "2" },
  { id: DELIVERY, seq: 2, name: "Delivery", weight: "1" },
];

const reply = (id: string, vendorId: string, unitPrice: string) => ({
  id,
  lineId: LINE_1,
  vendorId,
  unitPrice,
  priceUnit: "1",
  currency: "USD",
  leadTimeDays: 5,
  receivedAt: "2026-10-30T09:00:00Z",
  late: false,
  awarded: false,
});

const sealedBefore: RFQ = {
  id: RFQ_ID,
  number: "RFQ-00000011",
  legalEntityId: LE,
  status: "REPLIES_OPEN",
  replyDeadline: "2026-11-01T17:00:00Z",
  rankingRule: "WEIGHTED_SCORE",
  sealed: true,
  repliesSealed: true,
  replyCount: 3,
  version: 5,
  lines: [{ id: LINE_1, lineNo: 1, description: "Pump", qty: "10", unit: "EA" }],
  invitations: [
    { vendorId: VENDOR_A, status: "REPLIED", sentAt: "2026-10-26T11:00:00Z" },
    { vendorId: VENDOR_B, status: "REPLIED", sentAt: "2026-10-26T11:00:00Z" },
  ],
  replies: [],
  criteria,
  scores: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

const opened: RFQ = {
  ...sealedBefore,
  repliesSealed: false,
  openedAt: "2026-11-01T17:00:01Z",
  openedBy: "44444444-4444-4444-8444-444444444444",
  replies: [reply(REPLY_A, VENDOR_A, "10.00"), reply(REPLY_B, VENDOR_B, "9.50")],
  scores: [{ vendorId: VENDOR_A, criterionId: QUALITY, score: 8 }],
};

const comparison = {
  rfqId: RFQ_ID,
  rankingRule: "WEIGHTED_SCORE",
  lines: [
    {
      lineId: LINE_1,
      lineNo: 1,
      description: "Pump",
      qty: "10",
      unit: "EA",
      ranked: [
        { rank: 1, replyId: REPLY_A, vendorId: VENDOR_A, unitPrice: "10", currency: "USD", leadTimeDays: 5, total: "100", weightedScore: "6.3333" },
        { rank: 2, replyId: REPLY_B, vendorId: VENDOR_B, unitPrice: "9.5", currency: "USD", leadTimeDays: 5, total: "95" },
      ],
      excluded: [],
    },
  ],
};

type Init = RequestInit & { headers?: Record<string, string> };

describe("sealed bids and scoring rules", () => {
  it("describes how the RFQ takes bids", () => {
    expect(biddingLabel({ ...sealedBefore, sealed: false })).toBe("Open: replies are visible as they arrive");
    expect(biddingLabel(sealedBefore)).toBe("Sealed until 2026-11-01 17:00 UTC");
    expect(biddingLabel(opened)).toBe("Sealed, opened 2026-11-01 17:00 UTC by 44444444");
  });

  it("offers the weighted score only with criteria, and scoring only once sealed replies are open", () => {
    expect(rankingRules(opened)).toContain("WEIGHTED_SCORE");
    expect(rankingRules({ ...opened, criteria: [] })).toEqual(["LOWEST_PRICE", "LEAD_TIME"]);
    expect(scorable(opened)).toBe(true);
    expect(scorable(sealedBefore)).toBe(false);
    expect(scorable({ ...opened, status: "AWARDED" })).toBe(false);
    expect(scorable({ ...opened, criteria: [] })).toBe(false);
  });

  it("needs a criterion to rank by weighted score and checks each criterion", () => {
    const header = { legalEntityId: LE, deadline: "2099-11-01T17:00", rankingRule: "WEIGHTED_SCORE" as const };
    const line = [{ key: "l", productId: "", description: "Pump", qty: "1", unit: "EA", requiredDate: "" }];
    expect(validateRFQ({ ...header, criteria: [] }, line).criteria).toBe("Add at least one scoring criterion to rank by weighted score.");
    const errors = validateRFQ(
      {
        ...header,
        criteria: [
          { key: "a", name: "Quality", weight: "2" },
          { key: "b", name: "quality", weight: "0" },
        ],
      },
      line,
    );
    expect(errors["criteria[1].name"]).toBe("Use each name once.");
    expect(errors["criteria[1].weight"]).toBe("Enter a positive weight.");
    expect(errors["criteria[0].name"]).toBeUndefined();
  });
});

describe("sealed RFQ pages", () => {
  it("creates a sealed RFQ with weighted criteria", async () => {
    const fetchMock = stubFetch(() => json(sealedBefore, 201, { ETag: '"1"' }));
    const { container, router } = renderAt("/purchasing/rfqs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Reply deadline"), { target: { value: "2099-11-01T17:00" } });
    fireEvent.change(screen.getByLabelText("Rank replies by"), { target: { value: "WEIGHTED_SCORE" } });
    fireEvent.click(screen.getByRole("checkbox", { name: /Sealed bids/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add criterion" }));
    fireEvent.click(screen.getByRole("button", { name: "Add criterion" }));
    const first = screen.getByRole("group", { name: "Criterion 1" });
    fireEvent.change(within(first).getByLabelText("Criterion"), { target: { value: "Quality" } });
    fireEvent.change(within(first).getByLabelText("Weight"), { target: { value: "2" } });
    const second = screen.getByRole("group", { name: "Criterion 2" });
    fireEvent.change(within(second).getByLabelText("Criterion"), { target: { value: " Delivery " } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Pump" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/rfqs/${RFQ_ID}`));
    const sent = JSON.parse(String((fetchMock.mock.calls[0] as [string, RequestInit])[1].body));
    expect(sent.sealed).toBe(true);
    expect(sent.rankingRule).toBe("WEIGHTED_SCORE");
    expect(sent.criteria).toEqual([
      { name: "Quality", weight: "2" },
      { name: "Delivery", weight: "1" },
    ]);
  });

  it("keeps a sealed RFQ's replies hidden and offers no comparison before the deadline", async () => {
    const fetchMock = stubFetch(() => json(sealedBefore, 200, { ETag: '"5"' }));
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByRole("heading", { name: "3 sealed replies" })).toBeInTheDocument();
    expect(screen.getByText("Sealed until 2026-11-01 17:00 UTC")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Scoring criteria" })).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Replies" })).toBeNull();
    expect(screen.queryByText("Compare and award")).toBeNull();
    expect(screen.queryByText("Score vendors")).toBeNull();
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/compare"))).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("scores a vendor and ranks by weighted score once the bids are open", async () => {
    const rescored: RFQ = { ...opened, version: 6, scores: [...opened.scores!, { vendorId: VENDOR_A, criterionId: DELIVERY, score: 3 }] };
    const fetchMock = stubFetch((url, init) => {
      if ((init as RequestInit).method === "PUT") return json(rescored, 200, { ETag: '"6"' });
      if (url.includes("/compare")) return json(comparison);
      return json(opened, 200, { ETag: '"5"' });
    });
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByText("Sealed, opened 2026-11-01 17:00 UTC by 44444444")).toBeInTheDocument();
    const award = await screen.findByRole("group", { name: /Line 1: Pump/ });
    expect(await within(award).findByLabelText(/#1 vendor 22222222: 10 USD each, 5 days, total 100 USD, score 6.3333/)).toBeChecked();
    expect(within(screen.getByRole("table", { name: "Vendor scores" })).getByText("8")).toBeInTheDocument();
    expect(screen.getByLabelText("Quality (weight 2)")).toHaveValue("8");
    fireEvent.change(screen.getByLabelText("Delivery (weight 1)"), { target: { value: "3" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save scores" }));
    expect(await screen.findByText("Scores saved for vendor 22222222.")).toBeInTheDocument();
    const put = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "PUT") as [string, Init];
    expect(put[0]).toBe(`/api/v1/procurement/rfqs/${RFQ_ID}/scores/${VENDOR_A}`);
    expect(put[1].headers?.["If-Match"]).toBe('"5"');
    expect(JSON.parse(String(put[1].body))).toEqual({
      scores: [
        { criterionId: QUALITY, score: 8 },
        { criterionId: DELIVERY, score: 3 },
      ],
    });
  });

  it("shows a refused scoring with its code", async () => {
    stubFetch((url, init) => {
      if ((init as RequestInit).method === "PUT") return problem(403, "AUTH-0003", "missing duty RFQAward");
      if (url.includes("/compare")) return json(comparison);
      return json(opened, 200, { ETag: '"5"' });
    });
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save scores" }));
    expect(await screen.findByText("missing duty RFQAward (AUTH-0003)")).toBeInTheDocument();
  });

  it("marks sealed RFQs in the list", async () => {
    stubFetch(() =>
      json({
        items: [
          { id: RFQ_ID, number: "RFQ-00000011", legalEntityId: LE, status: "SENT", replyDeadline: "2026-11-01T17:00:00Z", rankingRule: "WEIGHTED_SCORE", sealed: true, lineCount: 1, vendorCount: 2, replyCount: 0, version: 2, createdAt: "2026-10-26T10:00:00Z" },
        ],
      }),
    );
    renderAt("/purchasing/rfqs");
    const table = await findTable({ name: "Requests for quotation" });
    expect(await within(table).findByText("Sealed")).toBeInTheDocument();
  });
});
