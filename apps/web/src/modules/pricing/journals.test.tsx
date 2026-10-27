import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ConditionRecord, Journal } from "./api/client";
import { emptyJournal, parseJournalLines, validateJournal } from "./NewJournalPage";
import { idempotencyKey, json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const J1 = "0190f0a0-0000-7000-8000-000000000301";
const J2 = "0190f0a0-0000-7000-8000-000000000302";
const R1 = "0190f0a0-0000-7000-8000-000000000201";
const R2 = "0190f0a0-0000-7000-8000-000000000202";

function rec(patch: Partial<ConditionRecord>): ConditionRecord {
  return {
    id: R1,
    legalEntityId: LE,
    type: "PRICE",
    customerKind: "customer",
    customerKey: "C1",
    productKind: "product",
    productKey: "BOLT",
    variant: "",
    currency: "USD",
    unit: "EA",
    fromQuantity: "0",
    amount: "7.41",
    discPct1: "0",
    discPct2: "0",
    validFrom: "2027-01-01",
    validTo: "2027-12-31",
    searchNext: false,
    exclusive: false,
    status: "ACTIVE",
    version: 2,
    ...patch,
  };
}

function journal(patch: Partial<Journal>): Journal {
  return {
    id: J1,
    legalEntityId: LE,
    name: "Autumn prices",
    status: "OPEN",
    smartRound: true,
    lineCount: 2,
    errorCount: 0,
    createdAt: "2027-09-01T10:00:00Z",
    errors: [],
    ...patch,
  };
}

const validated = journal({
  status: "VALIDATED",
  lines: [
    { lineNo: 1, op: "UPDATE", recordId: R1, expectedVersion: 2, adjustPct: "5", before: rec({}), after: rec({ amount: "7.99", version: 3 }), errors: [] },
    { lineNo: 2, op: "DELETE", recordId: R2, expectedVersion: 2, adjustPct: "0", before: rec({ id: R2, amount: "20" }), after: rec({ id: R2, amount: "20", status: "WITHDRAWN", version: 3 }), errors: [] },
  ],
});

describe("Price journals", () => {
  it("lists journals from the service, filters by status and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/journals")) return json(200, { items: [journal({}), journal({ id: J2, name: "Spring", status: "POSTED", postedAt: "2027-03-01T08:00:00Z" })] });
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, validated);
      return undefined;
    };
    const { container } = renderAt("/pricing/journals");
    const table = await screen.findByRole("table", { name: "Price journals" });
    await waitFor(() => expect(table).toHaveTextContent("Autumn prices"));
    expect(table).toHaveTextContent("2027-03-01");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "POSTED" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("POSTED"));
    fireEvent.click(within(table).getByRole("button", { name: "Autumn prices" }));
    expect(await screen.findByRole("heading", { name: "Autumn prices" })).toBeInTheDocument();
  });

  it("shows the empty state and the error state", async () => {
    server.handler = () => json(200, { items: [] });
    const first = renderAt("/pricing/journals");
    expect(await screen.findByText("No price journals")).toBeInTheDocument();
    first.unmount();
    server.handler = () => json(403, { code: "AUTH-0003", detail: "missing duty pricing.view" });
    renderAt("/pricing/journals");
    expect(await screen.findByRole("alert")).toHaveTextContent("missing duty pricing.view (AUTH-0003)");
  });
});

describe("New price journal", () => {
  it("checks the form and every row before calling the service", async () => {
    const { container } = renderAt("/pricing/journals/new");
    fireEvent.change(screen.getByLabelText("Lines"), { target: { value: `UPDATE, ${R1}, 2, 5\nMERGE, ${R2}, 1` } });
    fireEvent.click(screen.getByRole("button", { name: "Save journal" }));
    expect(await screen.findByText("Row 2: start with UPDATE or DELETE.")).toBeInTheDocument();
    expect(screen.getByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates the journal with an Idempotency-Key and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith("/journals")) return json(201, journal({}));
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, journal({ lines: [] }));
      return undefined;
    };
    renderAt("/pricing/journals/new");
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Journal name"), { target: { value: "Autumn prices" } });
    fireEvent.change(screen.getByLabelText("Lines"), { target: { value: `UPDATE, ${R1}, 2, 5\n\ndelete, ${R2}, 2` } });
    fireEvent.click(screen.getByLabelText("Smart rounding"));
    fireEvent.click(screen.getByRole("button", { name: "Save journal" }));
    expect(await screen.findByRole("heading", { name: "Autumn prices" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(idempotencyKey(post!)).toBeTruthy();
    expect(post?.body).toEqual({
      legalEntityId: LE,
      name: "Autumn prices",
      smartRound: true,
      lines: [
        { op: "UPDATE", recordId: R1, expectedVersion: 2, adjustPct: "5" },
        { op: "DELETE", recordId: R2, expectedVersion: 2 },
      ],
    });
  });

  it("puts a line error from the service on the lines field", async () => {
    server.handler = () => json(422, { code: "COM-0001", detail: "request is invalid", errors: { "lines[0].adjustPct": "must be a percentage above -100" } });
    renderAt("/pricing/journals/new");
    fireEvent.change(screen.getByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Journal name"), { target: { value: "x" } });
    fireEvent.change(screen.getByLabelText("Lines"), { target: { value: `UPDATE, ${R1}, 2, 5` } });
    fireEvent.click(screen.getByRole("button", { name: "Save journal" }));
    expect(await screen.findByText("Row 1: must be a percentage above -100")).toBeInTheDocument();
  });
});

describe("Price journal detail", () => {
  it("shows each line's before and after values and posts with an Idempotency-Key", async () => {
    let current: Journal = validated;
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":post")) {
        current = { ...validated, status: "POSTED", postedAt: "2027-09-02T09:30:00Z" };
        return json(200, { ...current, lines: undefined });
      }
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, current);
      return undefined;
    };
    const { container } = renderAt(`/pricing/journals/${J1}`);
    const lines = await screen.findByRole("table", { name: "Journal lines" });
    expect(lines).toHaveTextContent("Update +5%");
    expect(lines).toHaveTextContent("7.41 USD");
    expect(lines).toHaveTextContent("7.99 USD");
    expect(lines).toHaveTextContent("20 USD · withdrawn");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Post journal" }));
    expect(await screen.findByText(/every line took effect together/)).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/pricing/journals/${J1}:post`);
    expect(idempotencyKey(post!)).toBeTruthy();
    await waitFor(() => expect(screen.getByRole("button", { name: "Reverse journal" })).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Post journal" })).toBeNull();
  });

  it("validates an open journal and lists the failing lines", async () => {
    let current: Journal = journal({ lines: [] });
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":validate")) {
        current = journal({
          errorCount: 1,
          errors: [{ lineNo: 2, code: "PRC-0002", detail: `overlaps active record ${R1}` }],
          lines: [{ lineNo: 2, op: "UPDATE", recordId: R2, expectedVersion: 2, adjustPct: "5", errors: [{ lineNo: 2, code: "PRC-0002", detail: `overlaps active record ${R1}` }] }],
        });
        return json(200, current);
      }
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, current);
      return undefined;
    };
    renderAt(`/pricing/journals/${J1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Validate" }));
    expect(await screen.findByText("1 line needs attention")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Journal lines" })).toHaveTextContent(`PRC-0002: overlaps active record ${R1}`);
    expect(screen.queryByRole("button", { name: "Post journal" })).toBeNull();
  });

  it("reverses a posted journal and opens the reversing journal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":reverse")) return json(201, journal({ id: J2, name: "Reversal of Autumn prices", status: "POSTED", reversesJournalId: J1 }));
      if (url.pathname.endsWith(`/journals/${J2}`)) return json(200, journal({ id: J2, name: "Reversal of Autumn prices", status: "POSTED", reversesJournalId: J1, lines: [] }));
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, { ...validated, status: "POSTED" });
      return undefined;
    };
    renderAt(`/pricing/journals/${J1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Reverse journal" }));
    expect(await screen.findByRole("heading", { name: "Reversal of Autumn prices" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: J1 })).toBeInTheDocument();
  });

  it("explains a refused post and keeps the journal", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(422, { code: "PRC-0005", detail: "Journal has 1 validation errors; nothing was posted (line 2: conflict_version ...)" });
      if (url.pathname.endsWith(`/journals/${J1}`)) return json(200, validated);
      return undefined;
    };
    renderAt(`/pricing/journals/${J1}`);
    fireEvent.click(await screen.findByRole("button", { name: "Post journal" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Nothing was posted");
    expect(alert).toHaveTextContent("line 2");
  });

  it("shows a load error with a retry", async () => {
    server.handler = () => json(404, { code: "PRC-0034", detail: `no price journal ${J1}` });
    renderAt(`/pricing/journals/${J1}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("PRC-0034");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("journal line parsing", () => {
  it("accepts UPDATE with a signed percentage and DELETE, and names the failing row", () => {
    expect(parseJournalLines(`UPDATE, ${R1}, 2, -2.5`)).toEqual({ ok: true, lines: [{ op: "UPDATE", recordId: R1, expectedVersion: 2, adjustPct: "-2.5" }] });
    expect(parseJournalLines(`UPDATE, ${R1}, 2, -100`)).toMatchObject({ ok: false, error: /Row 1/ });
    expect(parseJournalLines(`UPDATE, ${R1}, 2, 0`)).toMatchObject({ ok: false });
    expect(parseJournalLines(`UPDATE, ${R1}, 2, 1.00001`)).toMatchObject({ ok: false });
    expect(parseJournalLines(`DELETE, ${R1}, 0`)).toMatchObject({ ok: false, error: /version/ });
    expect(parseJournalLines(`DELETE, ${R1}, 2, 5`)).toMatchObject({ ok: false, error: /too many/ });
    expect(parseJournalLines(`UPDATE, not-a-uuid, 2, 5`)).toMatchObject({ ok: false, error: /UUID/ });
    expect(parseJournalLines("\n\n")).toEqual({ ok: false, error: "Add at least one line." });
    expect(validateJournal({ ...emptyJournal, legalEntityId: LE, name: "x", lines: `DELETE, ${R1}, 3` })).toEqual({});
  });
});
