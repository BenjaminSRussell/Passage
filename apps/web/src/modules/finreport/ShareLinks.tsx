import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, ConfirmDialog, DataTable, FormField, Input, type DataTableColumn } from "../../shell/ui";
import {
  createShareLink,
  describeProblem,
  listShareLinks,
  MAX_SHARE_DAYS,
  MIN_SHARE_DAYS,
  newKey,
  revokeShareLink,
  shareLinkUrl,
  type Problem,
  type ShareLink,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The lifetime field as the service would read it: whole days from 1 to 90. */
export function validateShareDays(raw: string): { days?: number; error?: string } {
  const text = raw.trim();
  const days = Number(text);
  if (text === "" || !Number.isInteger(days) || days < MIN_SHARE_DAYS || days > MAX_SHARE_DAYS) {
    return { error: `Enter a whole number of days from ${MIN_SHARE_DAYS} to ${MAX_SHARE_DAYS}.` };
  }
  return { days };
}

const STATUS_TONE = { active: "success", expired: "neutral", revoked: "danger" } as const;

/**
 * Share links of one library file (FR-15.8). A link lets anyone who holds it download the file, with no sign-in,
 * until it expires or is revoked. The link is shown once, when it is made; the list never has it.
 */
export function ShareLinks({ libraryId }: { libraryId: string }) {
  const links = useLoad((signal) => listShareLinks(libraryId, signal), libraryId);
  const [days, setDays] = useState("14");
  const [daysError, setDaysError] = useState<string | undefined>();
  const [created, setCreated] = useState<ShareLink | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [revoking, setRevoking] = useState<ShareLink | null>(null);
  const [revokeFailure, setRevokeFailure] = useState<Problem | null>(null);
  // One key per request, so a retried click after a timeout returns the link already stored.
  const attempt = useRef<{ days: number; key: string } | null>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateShareDays(days);
    setDaysError(checked.error);
    setFailure(null);
    if (checked.days === undefined) return;
    if (attempt.current?.days !== checked.days) attempt.current = { days: checked.days, key: newKey() };
    setBusy(true);
    try {
      const link = await createShareLink(libraryId, checked.days, attempt.current.key);
      attempt.current = null;
      setCreated(link);
      links.reload();
    } catch (e) {
      setFailure(describeProblem(e));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(link: ShareLink) {
    setRevokeFailure(null);
    setRevoking(null);
    try {
      await revokeShareLink(link.id, link.revision);
      if (created?.id === link.id) setCreated(null);
      links.reload();
    } catch (e) {
      setRevokeFailure(describeProblem(e));
    }
  }

  const columns: DataTableColumn<ShareLink>[] = [
    { key: "status", header: "Status", render: (l) => <Badge tone={STATUS_TONE[l.status]}>{l.status[0]!.toUpperCase() + l.status.slice(1)}</Badge> },
    { key: "createdAt", header: "Created", mono: true, render: (l) => <time dateTime={l.createdAt}>{l.createdAt}</time> },
    { key: "createdBy", header: "Created by", mono: true },
    { key: "expiresAt", header: "Expires", mono: true, render: (l) => <time dateTime={l.expiresAt}>{l.expiresAt}</time> },
    { key: "downloadCount", header: "Downloads", mono: true, align: "end" },
    {
      key: "actions",
      header: "Actions",
      render: (l) =>
        l.status === "revoked" ? null : (
          <Button size="sm" variant="danger" aria-label={`Revoke the link created ${l.createdAt}`} onClick={() => setRevoking(l)}>
            Revoke
          </Button>
        ),
    },
  ];

  return (
    <Card title="Share this file">
      <div className={styles.stack}>
        <p className={styles.note}>
          A share link lets anyone who has it download this file, with no sign-in, until it expires or you revoke it. Send it only to people who should see this statement.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Create a share link">
          <FormField id="fr-share-days" label="Link lifetime (days)" error={daysError} hint={`From ${MIN_SHARE_DAYS} to ${MAX_SHARE_DAYS}. The link also ends when the file reaches its retention date.`}>
            {(c) => <Input {...c} inputMode="numeric" value={days} invalid={Boolean(daysError)} onChange={(e) => setDays(e.target.value)} />}
          </FormField>
          {failure ? (
            <Alert tone="danger" title="The link could not be created" live="alert">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? "Creating…" : "Create link"}
            </Button>
          </div>
        </form>
        {created?.token ? (
          <Alert tone="warning" title="Copy the link now" live="status">
            <p>It is shown only once. It works until {created.expiresAt}.</p>
            <FormField id="fr-share-url" label="Share link">
              {(c) => <Input {...c} className={styles.mono} readOnly value={shareLinkUrl(created.token ?? "")} onFocus={(e) => e.currentTarget.select()} />}
            </FormField>
          </Alert>
        ) : created ? (
          <Alert tone="info" title="This link was created before" live="status">
            The service had already made a link for this request, and its secret is shown only once, when it is made. If you did not copy it, revoke that link and create a new one.
          </Alert>
        ) : null}
        {revokeFailure ? (
          <Alert tone="danger" title="The link could not be revoked" live="alert">
            {revokeFailure.message} ({revokeFailure.code})
          </Alert>
        ) : null}
        {links.error ? (
          <Alert tone="danger" title="Could not load the share links" live="alert" action={<Button size="sm" onClick={links.reload}>Try again</Button>}>
            {links.error.message} ({links.error.code})
          </Alert>
        ) : (
          <DataTable
            label="Share links"
            columns={columns}
            rows={links.data ?? []}
            getRowKey={(l) => l.id}
            loading={links.loading && !links.data}
            empty={{ title: "This file has not been shared", description: "Create a link to let someone download it." }}
          />
        )}
      </div>
      {revoking ? (
        <ConfirmDialog
          title="Revoke this share link?"
          description="Anyone who has the link can no longer download the file with it. This cannot be undone; create a new link to share the file again."
          confirmLabel="Revoke link"
          cancelLabel="Keep link"
          onConfirm={() => void revoke(revoking)}
          onCancel={() => setRevoking(null)}
        />
      ) : null}
    </Card>
  );
}
