import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { BatchAttachment, Entry, JournalBatch } from "./api/client";
import { emptyAttachmentDraft, toAttachmentInput, validateAttachment } from "./BatchAttachments";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";
import { findTable } from "../../test-utils";

useFakeServer();

const LEDGER = "0190a000-0000-7000-8000-000000000001";
const BATCH = "0190a000-0000-7000-8000-000000000010";
const FILE = "0190a000-0000-7000-8000-0000000000f1";
const TENANT = "0190a000-0000-7000-8000-0000000000aa";

function batch(patch: Partial<JournalBatch> = {}): JournalBatch {
  return {
    id: BATCH,
    ledgerId: LEDGER,
    legalEntityId: "0190a000-0000-7000-8000-000000000099",
    journalName: "GJ",
    description: "March accrual",
    postingDate: "2026-03-31",
    currency: "USD",
    status: "draft",
    version: 1,
    requiresApproval: false,
    selfApprovalAllowed: false,
    totalDebit: "125.00",
    totalCredit: "125.00",
    lineCount: 2,
    createdBy: "clerk",
    createdAt: "2026-03-31T09:00:00Z",
    updatedAt: "2026-03-31T09:00:00Z",
    lines: [],
    ...patch,
  };
}

function file(patch: Partial<BatchAttachment> = {}): BatchAttachment {
  return {
    id: FILE,
    batchId: BATCH,
    kind: "attachment",
    name: "invoice.pdf",
    documentType: "invoice_image",
    retentionClass: "financial_record_10y",
    storeRef: `${TENANT}/journal-batch/invoice.pdf`,
    contentType: "application/pdf",
    sizeBytes: 2048,
    createdBy: "clerk",
    createdAt: "2026-03-31T09:30:00Z",
    ...patch,
  };
}

const attachmentsPath = `/api/v1/ledger/journal-batches/${BATCH}/attachments`;

describe("attachment drafts", () => {
  it("checks a file like the service and maps it to the request", () => {
    expect(validateAttachment(emptyAttachmentDraft)).toEqual({
      name: "Enter the file name (up to 200 characters, no slashes).",
      storeRef: "Enter the object-storage key ({tenant id}/…) or the dms:// address of the stored file.",
      sizeBytes: "Enter the size in bytes, from 1 to 26214400 (25 MB).",
    });
    const ok = { ...emptyAttachmentDraft, name: " a.pdf ", storeRef: "dms://archive/1", sizeBytes: "26214400" };
    expect(validateAttachment(ok)).toEqual({});
    expect(validateAttachment({ ...ok, sizeBytes: "26214401" }).sizeBytes).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: "file:///etc/passwd" }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: "a/../b" }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: "https://169.254.169.254/latest" }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: "vouchers/a.pdf" }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: `${TENANT}/../other/a.pdf` }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: "dms://archive:8080/1" }).storeRef).toBeDefined();
    expect(validateAttachment({ ...ok, storeRef: `${TENANT}/journal-batch/a.pdf` })).toEqual({});
    expect(validateAttachment({ ...ok, name: "../a.pdf" }).name).toBeDefined();
    expect(toAttachmentInput(ok)).toEqual({
      kind: "attachment",
      name: "a.pdf",
      documentType: "other",
      retentionClass: "financial_record_10y",
      storeRef: "dms://archive/1",
      contentType: "application/pdf",
      sizeBytes: 26214400,
    });
    expect(validateAttachment({ ...emptyAttachmentDraft, kind: "note", note: "  " }).note).toBeDefined();
    expect(toAttachmentInput({ ...emptyAttachmentDraft, kind: "note", note: " Checked " })).toEqual({ kind: "note", note: "Checked" });
  });
});

describe("batch attachments card", () => {
  it("lists files and notes, adds a file under one id and removes it", async () => {
    let items: BatchAttachment[] = [file()];
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch());
      if (url.pathname === attachmentsPath) return json(200, { items });
      if (url.pathname.startsWith(`${attachmentsPath}/`) && init.method === "PUT") {
        const id = url.pathname.split("/").pop() as string;
        const added = file({ id, name: "support.csv", contentType: "text/csv", documentType: "supporting_calculation" });
        items = [...items, added];
        return json(201, added);
      }
      if (url.pathname === `${attachmentsPath}/${FILE}` && init.method === "DELETE") {
        items = items.filter((i) => i.id !== FILE);
        return { status: 204, body: null };
      }
      return undefined;
    };
    const { container } = renderAt(`/ledger/journal-batches/${BATCH}`);
    const table = await findTable({ name: "Batch attachments" });
    expect(within(table).getByText("invoice.pdf")).toBeInTheDocument();
    expect(within(table).getByText("After posting")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Add attachment or note" }));
    const form = screen.getByRole("form", { name: "Add attachment or note" });
    fireEvent.click(within(form).getByRole("button", { name: "Add" }));
    expect(await within(form).findByText("Enter the file name (up to 200 characters, no slashes).")).toBeInTheDocument();
    expect(server.calls.some((c) => c.method === "PUT")).toBe(false);

    fireEvent.change(within(form).getByLabelText("File name"), { target: { value: "support.csv" } });
    fireEvent.change(within(form).getByLabelText("Document type"), { target: { value: "supporting_calculation" } });
    fireEvent.change(within(form).getByLabelText("Storage reference"), { target: { value: `${TENANT}/journal-batch/support.csv` } });
    fireEvent.change(within(form).getByLabelText("Content type"), { target: { value: "text/csv" } });
    fireEvent.change(within(form).getByLabelText("Size in bytes"), { target: { value: "900" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add" }));
    expect(await screen.findByText("support.csv")).toBeInTheDocument();
    const put = server.calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      kind: "attachment",
      name: "support.csv",
      documentType: "supporting_calculation",
      retentionClass: "financial_record_10y",
      storeRef: `${TENANT}/journal-batch/support.csv`,
      contentType: "text/csv",
      sizeBytes: 900,
    });

    fireEvent.click(screen.getByRole("button", { name: "Remove invoice.pdf" }));
    await waitFor(() => expect(screen.queryByText("invoice.pdf")).toBeNull());
  });

  it("adds a note", async () => {
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch());
      if (url.pathname === attachmentsPath) return json(200, { items: [] });
      if (init.method === "PUT") return json(201, { id: FILE, batchId: BATCH, kind: "note", note: "Checked", createdBy: "clerk", createdAt: "2026-03-31T09:30:00Z" });
      return undefined;
    };
    renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByText("No attachments or notes")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add attachment or note" }));
    fireEvent.change(screen.getByLabelText("Add a"), { target: { value: "note" } });
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: " Checked " } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() => expect(server.calls.find((c) => c.method === "PUT")?.body).toEqual({ kind: "note", note: "Checked" }));
  });

  it("shows when a posted file is retained, offers no removal until then, and shows the service's refusal", async () => {
    const expired = file({ id: "0190a000-0000-7000-8000-0000000000f2", name: "old.pdf", retainedUntil: "2020-01-01" });
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch({ status: "posted", postedEntryId: "0190a000-0000-7000-8000-000000000003" }));
      if (url.pathname === attachmentsPath) {
        return json(200, { items: [file({ retainedUntil: "2999-03-31" }), expired, { id: "0190a000-0000-7000-8000-0000000000f3", batchId: BATCH, kind: "note", note: "Checked", createdBy: "clerk", createdAt: "2026-03-31T09:30:00Z" }] });
      }
      if (init.method === "DELETE") return json(409, { code: "LEDGER_ATTACHMENT_RETAINED", detail: "Attachment old.pdf is retained until 2030-01-01 and cannot be deleted." });
      return undefined;
    };
    renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByText("2999-03-31")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add attachment or note" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove invoice.pdf" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove note" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Remove old.pdf" }));
    expect(await screen.findByText(/retained until 2030-01-01 and cannot be deleted. \(LEDGER_ATTACHMENT_RETAINED\)/)).toBeInTheDocument();
  });

  it("shows the load error with a retry", async () => {
    let fail = true;
    server.handler = (url) => {
      if (url.pathname === `/api/v1/ledger/journal-batches/${BATCH}`) return json(200, batch());
      return fail ? json(500, { code: "COM-0500", detail: "internal error" }) : json(200, { items: [] });
    };
    renderAt(`/ledger/journal-batches/${BATCH}`);
    expect(await screen.findByText("Could not load the attachments")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No attachments or notes")).toBeInTheDocument();
  });

  it("shows the batch's files and notes, read-only, on the posted entry", async () => {
    const ENTRY = "0190a000-0000-7000-8000-000000000003";
    const entry: Entry = {
      id: ENTRY,
      ledgerId: LEDGER,
      voucher: "GL-000123",
      postingSequence: 1,
      postingDate: "2026-03-31",
      periodKey: "2026-03",
      description: "March accrual",
      traceId: "a".repeat(32),
      totals: { currency: "USD", debit: "125.00", credit: "125.00" },
      lines: [],
    };
    server.handler = (url) => {
      if (url.pathname === `/api/v1/ledger/journal-entries/${ENTRY}`) return json(200, entry);
      if (url.pathname === `/api/v1/ledger/journal-entries/${ENTRY}/attachments`) return json(200, { items: [file({ retainedUntil: "2036-03-31" })] });
      return undefined;
    };
    const { container } = renderAt(`/ledger/entries/${ENTRY}`);
    const table = await findTable({ name: "Entry attachments" });
    expect(within(table).getByText("invoice.pdf")).toBeInTheDocument();
    expect(within(table).getByText("2036-03-31")).toBeInTheDocument();
    expect(within(table).queryByRole("button")).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });
});
