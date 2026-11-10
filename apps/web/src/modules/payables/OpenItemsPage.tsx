import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { useWorkContext } from "../../shell/lookup/workContextState";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { holdOpenItem, listOpenItems, OPEN_ITEM_STATUSES, releaseOpenItem, type OpenItem, type OpenItemStatus } from "./api/client";
import { formatAmount, holdText, isOverdue, OPEN_ITEM_LABELS, today } from "./format";

const CLOSED: OpenItemStatus[] = ["SETTLED", "REVERSED"];

export function OpenItemsPage() {
  const { legalEntityId } = useWorkContext();
  const canHold = holdsDuty(usePrincipalDuties(), "payment-hold.manage");
  const queryClient = useQueryClient();
  const [vendorId, setVendorId] = useState("");
  const [dueTo, setDueTo] = useState("");
  const [status, setStatus] = useState<OpenItemStatus | "">("OPEN");
  const [holding, setHolding] = useState<OpenItem | null>(null);
  const [reason, setReason] = useState("");
  const [notice, setNotice] = useState("");
  const day = today();

  const result = useInfiniteQuery({
    queryKey: ["payables", "open-items", legalEntityId, vendorId, dueTo, status],
    queryFn: ({ pageParam }) => listOpenItems({ legalEntityId, vendorId, dueTo, status, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const items = result.data?.pages.flatMap((p) => p.items) ?? [];

  const change = useMutation({
    mutationFn: (v: { item: OpenItem; hold: boolean; reason: string }) => (v.hold ? holdOpenItem(v.item.id, v.reason) : releaseOpenItem(v.item.id)),
    onSuccess: (_item, v) => {
      setNotice(v.hold ? `Invoice ${v.item.invoiceNumber} is on hold and will be left out of payments.` : `Invoice ${v.item.invoiceNumber} is released and can be paid.`);
      setHolding(null);
      setReason("");
      void queryClient.invalidateQueries({ queryKey: ["payables", "open-items"] });
    },
  });

  function startHold(item: OpenItem) {
    change.reset();
    setNotice("");
    setReason("");
    setHolding(item);
  }

  function submitHold(event: FormEvent) {
    event.preventDefault();
    if (holding) change.mutate({ item: holding, hold: true, reason: reason.trim() });
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
          <StatusPill status={OPEN_ITEM_LABELS[i.status]} /> {i.paymentHold ? <Badge tone="warning">{holdText(i)}</Badge> : null}
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
                <Button size="sm" loading={change.isPending && change.variables?.item.id === i.id} onClick={() => change.mutate({ item: i, hold: false, reason: "" })}>
                  {`Release hold on ${i.invoiceNumber}`}
                </Button>
              ) : (
                <Button size="sm" onClick={() => startHold(i)}>
                  {`Hold payment of ${i.invoiceNumber}`}
                </Button>
              ),
          },
        ]
      : []),
  ];

  return (
    <div className={styles.stack}>
      <PageHeader title="Vendor open items" description="Invoices you owe vendors. Put an invoice on hold to keep it out of payment proposals." />
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
              <Button type="submit" variant="primary" loading={change.isPending}>
                Put on hold
              </Button>
              <Button onClick={() => setHolding(null)}>Cancel</Button>
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
          empty={{ title: "Nothing owed to vendors here", description: "Invoices appear here once they are posted. Try another status or clear the filters." }}
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
