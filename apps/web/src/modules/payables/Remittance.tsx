import { useMutation, useQuery } from "@tanstack/react-query";
import { DownloadPdfButton } from "../../shell/documents/pdfDocument";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, Card, DataTable, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { downloadRemittanceCsv, getRemittance, type RemittanceLine, type RemittancePayment } from "./api/client";
import { formatAmount, METHOD_LABELS, saveBlob } from "./format";
import { remittanceDocument } from "./remittanceDocument";
import { useVendorName } from "./VendorHolds";

/** The action in a payment row that shows or hides the vendor's remittance advice. */
export function RemittanceButton({ vendorId, shown, onToggle }: { vendorId: string; shown: boolean; onToggle(): void }) {
  const name = useVendorName(vendorId) || "this vendor";
  return (
    <Button size="sm" aria-expanded={shown} onClick={onToggle}>
      {`Remittance advice for ${name}`}
    </Button>
  );
}

const lineColumns = (payment: RemittancePayment): DataTableColumn<RemittanceLine>[] => [
  { key: "invoiceNumber", header: "Document", mono: true },
  { key: "invoiceDate", header: "Invoice date", mono: true },
  { key: "openAmount", header: "Amount open", align: "end", mono: true, render: (l) => formatAmount(l.openAmount, payment.currency) },
  { key: "discount", header: "Discount", align: "end", mono: true, render: (l) => formatAmount(l.discount, payment.currency) },
  { key: "withheld", header: "Tax withheld", align: "end", mono: true, render: (l) => formatAmount(l.withheld, payment.currency) },
  { key: "paid", header: "Paid", align: "end", mono: true, render: (l) => formatAmount(l.paid, payment.currency) },
];

/**
 * The remittance advice of one vendor in a completed payment run: each payment and the invoices it paid, with
 * a CSV of the whole advice and a PDF per payment to send to the vendor.
 */
export function RemittanceCard({ runId, vendorId, onClose }: { runId: string; vendorId: string; onClose(): void }) {
  const advice = useQuery({ queryKey: ["payables", "remittance", runId, vendorId], queryFn: () => getRemittance(runId, vendorId) });
  const csv = useMutation({
    mutationFn: async () => saveBlob(await downloadRemittanceCsv(runId, vendorId), `remittance-advice-${advice.data?.payments[0]?.endToEndId ?? runId}.csv`),
  });
  const data = advice.data;
  const several = (data?.payments.length ?? 0) > 1;

  return (
    <Card>
      <h2 className={styles.legend}>
        Remittance advice for <EntityName entity="vendor" id={vendorId} />
      </h2>
      {advice.isPending ? <p role="status">Loading the remittance advice…</p> : null}
      {advice.isError ? (
        <Alert tone="danger" title="Could not load the remittance advice" live="alert" action={<Button size="sm" onClick={() => void advice.refetch()}>Try again</Button>}>
          <ProblemMessage problem={advice.error} />
        </Alert>
      ) : null}
      {data ? (
        <>
          <p className={styles.note}>Paid on {data.paymentDate}. Send the advice to the vendor so they can match the payment to their invoices.</p>
          {data.payments.map((p) => (
            <section key={p.endToEndId} aria-label={`Payment ${p.endToEndId}`} className={styles.stack}>
              <DataTable label={`Invoices paid with payment ${p.endToEndId}`} columns={lineColumns(p)} rows={p.lines} getRowKey={(l) => l.openItemId} />
              <p>
                Payment <span className={styles.mono}>{p.endToEndId}</span> ({METHOD_LABELS[p.paymentMethod] ?? p.paymentMethod}): <strong>{formatAmount(p.amount, p.currency)}</strong> paid.
              </p>
              <div className={styles.filters}>
                <DownloadPdfButton
                  build={() => remittanceDocument(data, p)}
                  filename={`remittance-advice-${p.endToEndId}.pdf`}
                  label={several ? `Download PDF for payment ${p.endToEndId}` : "Download PDF"}
                />
              </div>
            </section>
          ))}
          {csv.isError ? (
            <Alert tone="danger" title="Could not download the remittance advice" live="alert">
              <ProblemMessage problem={csv.error} />
            </Alert>
          ) : null}
        </>
      ) : null}
      <div className={styles.filters}>
        {data ? (
          <Button loading={csv.isPending} onClick={() => csv.mutate()}>
            Download CSV
          </Button>
        ) : null}
        <Button onClick={onClose}>Hide remittance advice</Button>
      </div>
    </Card>
  );
}
