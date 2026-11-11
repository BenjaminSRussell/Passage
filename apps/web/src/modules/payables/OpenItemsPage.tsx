import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { holdOpenItem, listOpenItems, listVendorBlocks, OPEN_ITEM_STATUSES, releaseOpenItem, unblockVendor, type OpenItem, type OpenItemStatus, type VendorBlock } from "./api/client";
import { formatAmount, holdText, isOverdue, OPEN_ITEM_LABELS, today } from "./format";
import { VendorHoldDialog, vendorHoldText, VendorHoldsCard } from "./VendorHolds";

const CLOSED: OpenItemStatus[] = ["SETTLED", "REVERSED"];

export function OpenItemsPage() {
  const { legalEntityId, identity } = useWorkContext();
  const duties = usePrincipalDuties();
  const canHold = holdsDuty(duties, "payment-hold.manage");
  const canHoldVendor = holdsDuty(duties, "vendor-block.manage");
  const canSeeVendorHolds = canHoldVendor || holdsDuty(duties, "open-item.read");
  const queryClient = useQueryClient();
  const [vendorId, setVendorId] = useState("");
  const [dueTo, setDueTo] = useState("");
  const [status, setStatus] = useState<OpenItemStatus | "">("OPEN");
  const [holding, setHolding] = useState<OpenItem | null>(null);
  const [reason, setReason] = useState("");
  const [notice, setNotice] = useState("");
  const [holdingVendor, setHoldingVendor] = useState(false);
  const day = today();

  const result = useInfiniteQuery({
    queryKey: ["payables", "open-items", legalEntityId, vendorId, dueTo, status],
    queryFn: ({ pageParam }) => listOpenItems({ legalEntityId, vendorId, dueTo, status, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];

  // Vendors on payment hold in the legal entity: shown next to their invoices and listed with the release.
  const vendorHolds = useQuery({ queryKey: ["payables", "vendor-blocks", legalEntityId], queryFn: () => listVendorBlocks(legalEntityId || undefined), enabled: canSeeVendorHolds });
  const blocks = vendorHolds.data?.items ?? [];
  const blockOf = new Map(blocks.map((b) => [b.vendorId, b]));
  const releaseVendor = useMutation({
    mutationFn: (v: { block: VendorBlock; name: string }) => unblockVendor(v.block.vendorId, v.block.legalEntityId),
    onSuccess: (_done, v) => setNotice(`The hold on ${v.name} is released. Its invoices can be paid again.`),
    onSettled: () => void queryClient.invalidateQueries({ queryKey: ["payables", "vendor-blocks"] }),
  });

  const change = useMutation({
    mutationFn: (v: { item: OpenItem; hold: boolean; reason: string }) => (v.hold ? holdOpenItem(v.item.id, v.reason) : releaseOpenItem(v.item.id)),
    onSuccess: (_item, v) => {
      setNotice(v.hold ? `Invoice ${v.item.invoiceNumber} is on hold and will be left out of payments.` : `Invoice ${v.item.invoiceNumber} is released and can be paid.`);
      setHolding(null);
      setReason("");
      void queryClient.invalidateQueries({ queryKey: ["payables", "open-items"] });
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: ["payables", "open-items"] });
    },
  });

  const busy = change.isPending || releaseVendor.isPending;
  const inFlight = useRef(false);

  function send(v: { item: OpenItem; hold: boolean; reason: string }) {
    if (inFlight.current) return;
    inFlight.current = true;
    change.mutate(v, { onSettled: () => { inFlight.current = false; } });
  }
  const filtered = vendorId !== "" || dueTo !== "" || status !== "OPEN";

  function clearFilters() {
    setVendorId("");
    setDueTo("");
    setStatus("OPEN");
  }

  function release(item: OpenItem) {
    send({ item, hold: false, reason: "" });
  }

  function startHold(item: OpenItem) {
    change.reset();
    setNotice("");
    setReason("");
    setHolding(item);
  }

  function submitHold(event: FormEvent) {
    event.preventDefault();
    if (holding) send({ item: holding, hold: true, reason: reason.trim() });
  }

  const columns: DataTableColumn<OpenItem>[] = [
    { key: "vendor", header: "Vendor", render: (i) => <EntityName entity="vendor" id={i.vendorId} /> },
    { key: "invoiceNumber", header: "Document", mono: true },
    {
      key: "dueDate",
      header: "Due",
      mono: true,
      render: (i) => (
        <>
          {i.dueDate} {isOverdue(i, day) ? <Badge tone="danger">Overdue</Badge> : null}
        </>
      ),
    },
    { key: "openAmount", header: "Amount open", align: "end", mono: true, render: (i) => formatAmount(i.openAmount, i.currency) },
    {
      key: "status",
      header: "Status",
      render: (i) => (
        <>
          <StatusPill status={OPEN_ITEM_LABELS[i.status]} /> {i.paymentHold ? <Badge tone="warning">{holdText(i)}</Badge> : null}{" "}
          {blockOf.has(i.vendorId) && !CLOSED.includes(i.status) ? <Badge tone="warning">{vendorHoldText(blockOf.get(i.vendorId)!)}</Badge> : null}
        </>
      ),
    },
    ...(canHold
      ? [
          {
            key: "actions",
            header: "Actions",
            render: (i: OpenItem) =>
              CLOSED.includes(i.status) ? null : i.paymentHold ? (
                <Button size="sm" disabled={busy} loading={busy && change.variables?.item.id === i.id} onClick={() => release(i)}>
                  {`Release hold on ${i.invoiceNumber}`}
                </Button>
              ) : (
                <Button size="sm" disabled={busy} onClick={() => startHold(i)}>
                  {`Hold payment of ${i.invoiceNumber}`}
                </Button>
              ),
          },
        ]
      : []),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Vendor open items"
        description="Invoices you owe vendors. Put an invoice or a whole vendor on hold to keep it out of payment proposals."
        actions={
          canHoldVendor ? (
            <Button
              disabled={busy}
              onClick={() => {
                releaseVendor.reset();
                setNotice("");
                setHoldingVendor(true);
              }}
            >
              Hold payments to a vendor
            </Button>
          ) : null
        }
      />
      <div className={styles.filters} role="group" aria-label="Filter open items">
        <FormField id="oi-vendor" label="Vendor" optional>
          {(c) => <EntityLookup entity="vendor" {...c} scope={{ legalEntityId: legalEntityId || undefined }} value={vendorId} onChange={(e) => setVendorId(e.target.value)} />}
        </FormField>
        <FormField id="oi-due" label="Due by" optional hint="Shows invoices due on or before this date.">
          {(c) => <Input {...c} type="date" value={dueTo} onChange={(e) => setDueTo(e.target.value)} />}
        </FormField>
        <FormField id="oi-status" label="Status">
          {(c) => (
            <Select {...c} value={status} onChange={(e) => setStatus(e.target.value as OpenItemStatus | "")}>
              <option value="">Any status</option>
              {OPEN_ITEM_STATUSES.map((s) => (
                <option key={s} value={s}>
                  {OPEN_ITEM_LABELS[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        {filtered ? (
          <Button onClick={clearFilters}>Clear filters</Button>
        ) : null}
      </div>
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {change.isError && !holding ? (
        <Alert tone="danger" title="Could not change the hold" live="alert">
          <ProblemMessage problem={change.error} />
        </Alert>
      ) : null}
      {releaseVendor.isError ? (
        <Alert tone="danger" title="Could not release the vendor hold" live="alert">
          <ProblemMessage problem={releaseVendor.error} />
        </Alert>
      ) : null}
      {vendorHolds.isError && !result.isError ? (
        <Alert tone="danger" title="Could not load the vendors on payment hold" live="alert" action={<Button size="sm" onClick={() => void vendorHolds.refetch()}>Try again</Button>}>
          <ProblemMessage problem={vendorHolds.error} />
        </Alert>
      ) : null}
      {blocks.length > 0 ? (
        <VendorHoldsCard
          blocks={blocks}
          me={identity}
          canRelease={canHoldVendor}
          busy={busy}
          releasing={releaseVendor.variables?.block.vendorId}
          onRelease={(block, name) => {
            if (releaseVendor.isPending) return;
            setNotice("");
            releaseVendor.mutate({ block, name });
          }}
        />
      ) : null}
      {holdingVendor ? (
        <VendorHoldDialog
          legalEntityId={legalEntityId}
          vendorId={vendorId}
          onCancel={() => setHoldingVendor(false)}
          onHeld={(name) => {
            setHoldingVendor(false);
            setNotice(`Payments to ${name} are on hold. Its invoices are left out of payment proposals.`);
            void queryClient.invalidateQueries({ queryKey: ["payables", "vendor-blocks"] });
          }}
        />
      ) : null}
      {holding ? (
        <Card>
          <form className={styles.form} onSubmit={submitHold} aria-label={`Hold payment of ${holding.invoiceNumber}`}>
            <FormField id="oi-reason" label="Reason for the hold" optional hint="Shown next to the invoice so others know why it is not paid.">
              {(c) => <Textarea {...c} maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />}
            </FormField>
            {change.isError ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="Could not put the invoice on hold" live="alert">
                  <ProblemMessage problem={change.error} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" disabled={busy} loading={busy}>
                Put on hold
              </Button>
              <Button disabled={busy} onClick={() => setHolding(null)}>Cancel</Button>
            </div>
          </form>
        </Card>
      ) : null}
      {result.isError ? (
        <Alert tone="danger" title="Could not load the open items" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : (
        <DataTable
          label="Vendor open items"
          columns={columns}
          rows={items}
          getRowKey={(i) => i.id}
          loading={result.isPending}
          empty={
            filtered
              ? { title: "No open items match these filters", description: "Change or clear the filters to see more invoices." }
              : { title: "Nothing owed to vendors here", description: "Invoices appear here once they are posted." }
          }
        />
      )}
      {result.hasNextPage ? (
        <div className={styles.filters}>
          <Button loading={result.isFetchingNextPage} onClick={() => void result.fetchNextPage()}>
            Load more open items
          </Button>
        </div>
      ) : null}
    </div>
  );
}
