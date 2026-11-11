import { useMutation } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { ProblemMessage } from "../../shell/errors";
import { useFocusTrap } from "../../shell/hooks/useFocusTrap";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { formatOption, resolveOption } from "../../shell/lookup/entities";
import { Alert, Button, Card, DataTable, FormField, Textarea, type DataTableColumn } from "../../shell/ui";
import dialog from "../../shell/ui/ConfirmDialog.module.css";
import styles from "../../shell/pageLayout.module.css";
import { blockVendor, type VendorBlock } from "./api/client";
import { personLabel } from "./format";

// The service calls this a vendor block (`/vendors/{id}:block`). People read "hold": it stops every payment to
// the vendor until someone releases it, the same word the page uses for one invoice.

/** The vendor's display name once it is known, "" until then or when it can't be read. */
export function useVendorName(id: string): string {
  const [name, setName] = useState("");
  useEffect(() => {
    let live = true;
    setName("");
    if (id !== "") {
      void resolveOption("vendor", id).then((found) => {
        if (live && found) setName(formatOption(found));
      });
    }
    return () => {
      live = false;
    };
  }, [id]);
  return name;
}

export function vendorHoldText(block: Pick<VendorBlock, "reason">): string {
  return `Vendor on hold: ${block.reason}`;
}

export interface VendorHoldDialogProps {
  legalEntityId: string;
  /** The vendor the page is filtered to, offered as the vendor to hold. */
  vendorId: string;
  onHeld(vendorName: string): void;
  onCancel(): void;
}

/** Asks which vendor to hold and why, then puts every payment to the vendor on hold. */
export function VendorHoldDialog({ legalEntityId, vendorId: initialVendor, onHeld, onCancel }: VendorHoldDialogProps) {
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  const opener = useRef<Element | null>(document.activeElement);
  const [vendorId, setVendorId] = useState(initialVendor);
  const [picked, setPicked] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ vendor?: string; reason?: string }>({});
  const knownName = useVendorName(vendorId);
  useFocusTrap(panel, true);
  useEffect(() => {
    const previous = opener.current;
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);

  const hold = useMutation({
    mutationFn: () => blockVendor(vendorId, legalEntityId || undefined, reason.trim()),
    onSuccess: () => onHeld(picked || knownName || "the vendor"),
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = {
      vendor: vendorId === "" ? "Choose the vendor to put on hold." : undefined,
      reason: reason.trim() === "" ? "Say why payments to the vendor are on hold." : undefined,
    };
    setErrors(found);
    if (found.vendor || found.reason || hold.isPending) return;
    hold.mutate();
  }

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.key === "Escape" && !hold.isPending) {
      event.stopPropagation();
      onCancel();
    }
  };

  return (
    <div className={dialog.overlay} onMouseDown={(event) => event.target === event.currentTarget && !hold.isPending && onCancel()}>
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={descriptionId} className={dialog.panel} onKeyDown={onKeyDown}>
        <h2 id={titleId} className={dialog.title}>
          Hold payments to a vendor
        </h2>
        <p id={descriptionId} className={dialog.description}>
          The vendor's invoices are left out of payment proposals until you release the hold.
        </p>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="vh-vendor" label="Vendor" required error={errors.vendor}>
            {(c) => (
              <EntityLookup
                entity="vendor"
                {...c}
                scope={{ legalEntityId: legalEntityId || undefined }}
                value={vendorId}
                onChange={(e) => {
                  setVendorId(e.target.value);
                  setPicked(e.option ? formatOption(e.option) : "");
                }}
              />
            )}
          </FormField>
          <FormField id="vh-reason" label="Reason for the hold" required hint="Shown next to the vendor's invoices so others know why they are not paid." error={errors.reason}>
            {(c) => <Textarea {...c} maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />}
          </FormField>
          {hold.isError ? (
            <Alert tone="danger" title="Could not put the vendor on hold" live="alert">
              <ProblemMessage problem={hold.error} />
            </Alert>
          ) : null}
          <div className={dialog.actions}>
            <Button disabled={hold.isPending} onClick={onCancel}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={hold.isPending} loading={hold.isPending}>
              Put vendor on hold
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}

function ReleaseButton({ block, busy, loading, onRelease }: { block: VendorBlock; busy: boolean; loading: boolean; onRelease(block: VendorBlock, name: string): void }) {
  const name = useVendorName(block.vendorId) || "this vendor";
  return (
    <Button size="sm" disabled={busy} loading={loading} onClick={() => onRelease(block, name)}>
      {`Release hold on ${name}`}
    </Button>
  );
}

export interface VendorHoldsCardProps {
  blocks: VendorBlock[];
  me: string;
  canRelease: boolean;
  busy: boolean;
  releasing?: string;
  onRelease(block: VendorBlock, vendorName: string): void;
}

/** The vendors whose payments are on hold in the legal entity, with the reason and, for a holder of the duty, the release. */
export function VendorHoldsCard({ blocks, me, canRelease, busy, releasing, onRelease }: VendorHoldsCardProps) {
  const columns: DataTableColumn<VendorBlock>[] = [
    { key: "vendor", header: "Vendor", render: (b) => <EntityName entity="vendor" id={b.vendorId} /> },
    { key: "reason", header: "Reason" },
    { key: "blockedAt", header: "On hold since", mono: true, render: (b) => b.blockedAt.slice(0, 10) },
    { key: "blockedBy", header: "Put on hold by", render: (b) => personLabel(b.blockedBy, me) },
    ...(canRelease
      ? [{ key: "actions", header: "Actions", render: (b: VendorBlock) => <ReleaseButton block={b} busy={busy} loading={busy && releasing === b.vendorId} onRelease={onRelease} /> }]
      : []),
  ];
  return (
    <Card>
      <h2 className={styles.legend}>Vendors on payment hold</h2>
      <p className={styles.note}>Invoices of these vendors are left out of payment proposals until the hold is released.</p>
      <DataTable label="Vendors on payment hold" columns={columns} rows={blocks} getRowKey={(b) => b.vendorId} />
    </Card>
  );
}
