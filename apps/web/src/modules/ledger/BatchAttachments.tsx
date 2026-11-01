import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  deleteBatchAttachment,
  DOCUMENT_TYPES,
  fetchBatchAttachments,
  fetchEntryAttachments,
  LedgerApiError,
  newKey,
  putBatchAttachment,
  RETENTION_CLASSES,
  type BatchAttachment,
  type BatchAttachmentInput,
} from "./api/client";
import styles from "./pages.module.css";

/** The limits of FR-11.11 / FR-11.18, checked here so the user is told before the service says it. */
export const MAX_FILE_BYTES = 25 * 1024 * 1024;
export const CONTENT_TYPES = [
  "application/pdf",
  "image/png",
  "image/jpeg",
  "image/tiff",
  "text/plain",
  "text/csv",
  "application/xml",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "message/rfc822",
] as const;

export interface AttachmentDraft {
  kind: "attachment" | "note";
  name: string;
  documentType: string;
  retentionClass: string;
  storeRef: string;
  contentType: string;
  sizeBytes: string;
  note: string;
}

export const emptyAttachmentDraft: AttachmentDraft = {
  kind: "attachment",
  name: "",
  documentType: "other",
  retentionClass: "financial_record_10y",
  storeRef: "",
  contentType: "application/pdf",
  sizeBytes: "",
  note: "",
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const KEY_SEGMENT = /^[A-Za-z0-9._=+-]{1,255}$/;
const DMS_URI = /^dms:\/\/[a-z0-9][a-z0-9-]{0,62}\/[A-Za-z0-9._~=+-]+(\/[A-Za-z0-9._~=+-]+)*$/;
const dotSegment = (s: string) => s === "." || s === "..";

/**
 * The shape the service accepts: an object key whose first segment is a tenant id (the service checks it is the
 * caller's) or a dms://{connector}/{document} address. Web URLs are refused so the reference can't point anywhere else.
 */
export function validStoreRef(ref: string): boolean {
  if (ref === "" || ref.length > 1024) return false;
  if (ref.startsWith("dms://")) return DMS_URI.test(ref) && !ref.slice(6).split("/").some(dotSegment);
  const [tenant, ...rest] = ref.split("/");
  return UUID.test(tenant) && rest.length > 0 && rest.every((s) => KEY_SEGMENT.test(s) && !dotSegment(s));
}

/** Field errors of a draft; empty when the service would accept it. */
export function validateAttachment(d: AttachmentDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (d.kind === "note") {
    const n = d.note.trim();
    if (n === "" || n.length > 2000) errors.note = "Enter a note of 1 to 2000 characters.";
    return errors;
  }
  if (!/^[^\x00-\x1f/\\]{1,200}$/.test(d.name.trim())) errors.name = "Enter the file name (up to 200 characters, no slashes).";
  if (!validStoreRef(d.storeRef.trim())) errors.storeRef = "Enter the object-storage key ({tenant id}/…) or the dms:// address of the stored file.";
  const size = Number(d.sizeBytes);
  if (!/^\d+$/.test(d.sizeBytes) || size < 1 || size > MAX_FILE_BYTES) errors.sizeBytes = "Enter the size in bytes, from 1 to 26214400 (25 MB).";
  return errors;
}

export function toAttachmentInput(d: AttachmentDraft): BatchAttachmentInput {
  if (d.kind === "note") return { kind: "note", note: d.note.trim() };
  return {
    kind: "attachment",
    name: d.name.trim(),
    documentType: d.documentType,
    retentionClass: d.retentionClass,
    storeRef: d.storeRef.trim(),
    contentType: d.contentType,
    sizeBytes: Number(d.sizeBytes),
  };
}

function problemText(title: string, error: unknown): { title: string; lines: string[] } {
  if (!(error instanceof LedgerApiError)) return { title, lines: [(error as Error).message] };
  return { title, lines: [`${error.message} (${error.code})`, ...error.violations.map((v) => `${v.message} (${v.code})`)] };
}

function formatSize(bytes: number | undefined): string {
  if (!bytes) return "";
  return bytes >= 1024 * 1024 ? `${(bytes / (1024 * 1024)).toFixed(1)} MB` : bytes >= 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${bytes} B`;
}

const readColumns: DataTableColumn<BatchAttachment>[] = [
  { key: "kind", header: "Type", render: (a) => (a.kind === "note" ? "Note" : "File") },
  { key: "name", header: "Name or note", render: (a) => (a.kind === "note" ? a.note : a.name) },
  { key: "documentType", header: "Document type", render: (a) => (a.documentType ?? "").replaceAll("_", " ") },
  { key: "sizeBytes", header: "Size", align: "end", render: (a) => formatSize(a.sizeBytes) },
  { key: "retainedUntil", header: "Retained until", render: (a) => a.retainedUntil ?? "" },
  { key: "createdBy", header: "Added by" },
];

/** The attachments and notes of the batch a posted entry came from, read-only (FR-11.18). */
export function EntryAttachmentsCard({ entryId }: { entryId: string }) {
  const result = useQuery({ queryKey: ["ledger", "entry", entryId, "attachments"], queryFn: () => fetchEntryAttachments(entryId) });
  return (
    <Card title="Attachments and notes">
      {result.isError ? (
        <Alert tone="danger" title="Could not load the attachments" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Entry attachments"
          columns={readColumns}
          rows={result.data ?? []}
          getRowKey={(a) => a.id}
          loading={result.isPending}
          empty={{ title: "No attachments or notes", description: "Entries posted from a journal batch carry the batch's files and notes." }}
        />
      )}
    </Card>
  );
}

/**
 * Attachments and notes of a journal batch (FR-11.11, FR-11.18). They are added while the batch is
 * editable; after posting they are read-only and a file shows how long it is retained.
 */
export function BatchAttachmentsCard({ batchId, status }: { batchId: string; status: string }) {
  const queryClient = useQueryClient();
  const queryKey = ["ledger", "journal-batch", batchId, "attachments"];
  const result = useQuery({ queryKey, queryFn: () => fetchBatchAttachments(batchId) });
  const editable = status !== "posted" && status !== "cancelled";
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState<AttachmentDraft>(emptyAttachmentDraft);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [problem, setProblem] = useState<{ title: string; lines: string[] } | null>(null);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState<string | null>(null);
  // One id per user-intended add: pressing Add again after a timeout repeats the same request.
  const [attemptId, setAttemptId] = useState(() => newKey());

  async function add(event: FormEvent) {
    event.preventDefault();
    const found = validateAttachment(draft);
    setErrors(found);
    setProblem(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putBatchAttachment(batchId, attemptId, toAttachmentInput(draft));
      await queryClient.invalidateQueries({ queryKey });
      setDraft(emptyAttachmentDraft);
      setAttemptId(newKey());
      setAdding(false);
    } catch (error) {
      setProblem(problemText("Could not add it", error));
    } finally {
      setBusy(false);
    }
  }

  async function remove(a: BatchAttachment) {
    setRemoving(a.id);
    setProblem(null);
    try {
      await deleteBatchAttachment(batchId, a.id);
      await queryClient.invalidateQueries({ queryKey });
    } catch (error) {
      setProblem(problemText("Could not remove it", error));
    } finally {
      setRemoving(null);
    }
  }

  // After posting a note is permanent and a file stays until its retention date (the service refuses earlier).
  const today = new Date().toISOString().slice(0, 10);
  const removable = (a: BatchAttachment) => status !== "posted" || (a.kind === "attachment" && a.retainedUntil !== undefined && a.retainedUntil <= today);

  const columns: DataTableColumn<BatchAttachment>[] = [
    ...readColumns.map((c) =>
      c.key === "retainedUntil" ? { ...c, render: (a: BatchAttachment) => a.retainedUntil ?? (a.kind === "attachment" ? "After posting" : "") } : c,
    ),
    {
      key: "actions",
      header: "Actions",
      render: (a) =>
        !removable(a) ? null : (
          <Button size="sm" variant="ghost" loading={removing === a.id} aria-label={`Remove ${a.kind === "note" ? "note" : a.name}`} onClick={() => void remove(a)}>
            Remove
          </Button>
        ),
    },
  ];

  return (
    <Card title="Attachments and notes">
      {problem ? (
        <Alert tone="danger" title={problem.title} live="alert">
          <ul className={styles.problems}>
            {problem.lines.map((l) => (
              <li key={l}>{l}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {result.isError ? (
        <Alert tone="danger" title="Could not load the attachments" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <div aria-busy="true">
          <span className="visually-hidden">Loading the attachments…</span>
          <Skeleton height="control" />
        </div>
      ) : (
        <DataTable
          label="Batch attachments"
          columns={columns}
          rows={result.data}
          getRowKey={(a) => a.id}
          empty={{ title: "No attachments or notes", description: editable ? "Add the supporting document or a note for the approver." : undefined }}
        />
      )}
      {editable && !adding ? (
        <div className={styles.formActions}>
          <Button onClick={() => setAdding(true)}>Add attachment or note</Button>
        </div>
      ) : null}
      {editable && adding ? (
        <form className={styles.form} onSubmit={add} noValidate aria-label="Add attachment or note">
          <FormField id="att-kind" label="Add a">
            {(c) => (
              <Select {...c} value={draft.kind} onChange={(e) => setDraft({ ...draft, kind: e.target.value as AttachmentDraft["kind"] })}>
                <option value="attachment">File</option>
                <option value="note">Note</option>
              </Select>
            )}
          </FormField>
          {draft.kind === "note" ? (
            <div className={styles.wide}>
              <FormField id="att-note" label="Note" error={errors.note}>
                {(c) => <Textarea {...c} value={draft.note} maxLength={2000} onChange={(e) => setDraft({ ...draft, note: e.target.value })} invalid={Boolean(errors.note)} />}
              </FormField>
            </div>
          ) : (
            <>
              <FormField id="att-name" label="File name" error={errors.name}>
                {(c) => <Input {...c} value={draft.name} maxLength={200} onChange={(e) => setDraft({ ...draft, name: e.target.value })} invalid={Boolean(errors.name)} />}
              </FormField>
              <FormField id="att-doctype" label="Document type">
                {(c) => (
                  <Select {...c} value={draft.documentType} onChange={(e) => setDraft({ ...draft, documentType: e.target.value })}>
                    {DOCUMENT_TYPES.map((t) => (
                      <option key={t} value={t}>
                        {t.replaceAll("_", " ")}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="att-retention" label="Retention class" hint="Counted from the posting date.">
                {(c) => (
                  <Select {...c} value={draft.retentionClass} onChange={(e) => setDraft({ ...draft, retentionClass: e.target.value })}>
                    {RETENTION_CLASSES.map((t) => (
                      <option key={t} value={t}>
                        {t.replaceAll("_", " ")}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="att-store" label="Storage reference" hint="The object-storage key ({tenant id}/…) or dms:// address the document service returned." error={errors.storeRef}>
                {(c) => <Input {...c} value={draft.storeRef} onChange={(e) => setDraft({ ...draft, storeRef: e.target.value })} invalid={Boolean(errors.storeRef)} />}
              </FormField>
              <FormField id="att-content" label="Content type">
                {(c) => (
                  <Select {...c} value={draft.contentType} onChange={(e) => setDraft({ ...draft, contentType: e.target.value })}>
                    {CONTENT_TYPES.map((t) => (
                      <option key={t} value={t}>
                        {t}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="att-size" label="Size in bytes" hint="Up to 25 MB." error={errors.sizeBytes}>
                {(c) => <Input {...c} inputMode="numeric" value={draft.sizeBytes} onChange={(e) => setDraft({ ...draft, sizeBytes: e.target.value })} invalid={Boolean(errors.sizeBytes)} />}
              </FormField>
            </>
          )}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Add
            </Button>
            <Button variant="ghost" onClick={() => { setAdding(false); setErrors({}); setProblem(null); }}>
              Cancel
            </Button>
          </div>
        </form>
      ) : null}
      {status === "posted" ? <p className={styles.note}>Files are retained from the posting date and stay readable from the journal entry.</p> : null}
    </Card>
  );
}
