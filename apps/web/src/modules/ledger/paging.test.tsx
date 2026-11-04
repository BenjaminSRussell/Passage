import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { findTable } from "../../test-utils";
import { putDescriptionTemplate, putDimensionValue, putJournalName, putStructure, type FetchLike } from "./api/client";
import { json, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";

function batchRow(id: string, journalName: string) {
  return {
    id, ledgerId: LEDGER, legalEntityId: "0190a000-0000-7000-8000-0000000000a1", journalName, description: "", postingDate: "2026-03-31",
    currency: "USD", status: "draft", version: 1, requiresApproval: false, selfApprovalAllowed: false, totalDebit: "1.00", totalCredit: "1.00",
    lineCount: 2, createdBy: "clerk", createdAt: "2026-03-01T10:00:00Z", updatedAt: "2026-03-01T10:00:00Z",
  };
}

describe("keyset paged lists", () => {
  it("loads the next journal batch page with the cursor and appends it", async () => {
    server.handler = (url) => {
      if (url.pathname !== "/api/v1/ledger/journal-batches") return undefined;
      return url.searchParams.get("cursor") === "c2"
        ? json(200, { items: [batchRow("0190a000-0000-7000-8000-0000000000b2", "PAGE2")] })
        : json(200, { items: [batchRow("0190a000-0000-7000-8000-0000000000b1", "PAGE1")], nextCursor: "c2" });
    };
    renderAt("/ledger/journal-batches");
    const table = await findTable({ name: "Journal batches" });
    expect(await within(table).findByText("PAGE1")).toBeInTheDocument();
    expect(within(table).queryByText("PAGE2")).toBeNull();

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await within(table).findByText("PAGE2")).toBeInTheDocument();
    expect(within(table).getByText("PAGE1")).toBeInTheDocument();
    expect(server.calls.at(-1)?.url.searchParams.get("cursor")).toBe("c2");
    // The last page has no cursor, so there is nothing more to load.
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });
});

describe("configuration PUTs send the entity tag", () => {
  const ok = { ok: true, status: 200, json: async () => ({}) } as unknown as Response;

  it("sends If-Match when replacing and nothing when creating", async () => {
    const seen: Record<string, string>[] = [];
    const fetchImpl: FetchLike = async (_url, init) => {
      seen.push((init?.headers ?? {}) as Record<string, string>);
      return {
        ...ok,
        json: async () => ({
          id: "i", ledgerId: LEDGER, name: "N", version: 1, priority: 0, effectiveFrom: "2026-01-01", status: "draft", segments: [], accountFilter: [], constraints: [],
          attribute: "DEPT", value: "D1", suspended: false, scope: "tenant", legalEntityId: null, sourceType: "x", language: "en", template: "t", fieldBindings: {},
          updatedAt: "2026-01-01T00:00:00Z", code: "GJ", requiresApproval: false, selfApprovalAllowed: false, reversalReasonRequired: false,
          allowReconAccounts: false, linesLimit: 10, active: true,
        }),
      } as unknown as Response;
    };
    const structure = { name: "N", version: 1, priority: 0, effectiveFrom: "2026-01-01", status: "draft" as const, segments: [], accountFilter: [], constraints: [] };
    await putStructure(LEDGER, "i", structure, 3, fetchImpl);
    await putStructure(LEDGER, "i", structure, undefined, fetchImpl);
    await putDimensionValue(LEDGER, "DEPT", "D1", { name: "", activeFrom: null, activeTo: null, suspended: false }, 4, fetchImpl);
    await putDescriptionTemplate("i", { scope: "tenant", sourceType: "x", language: "en", template: "t", fieldBindings: {} }, 5, fetchImpl);
    await putJournalName(
      "GJ",
      { name: "N", requiresApproval: false, selfApprovalAllowed: false, reversalReasonRequired: false, allowReconAccounts: false, linesLimit: 10, postingRestriction: "none", descriptionLanguage: "legal_entity", active: true },
      6,
      fetchImpl,
    );
    expect(seen.map((h) => h["If-Match"])).toEqual(['"3"', undefined, '"4"', '"5"', '"6"']);
  });
});
