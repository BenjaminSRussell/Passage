import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatusPill, type DataTableColumn } from "../../shell/ui";
import { downloadCsv } from "../../shell/ui/csv";
import styles from "../../shell/pageLayout.module.css";
import { compressJournal, correctJournal, deleteLine, exportJournal, getJournal, transferLines, validateJournal, type IntrastatLine, type NewIntrastatLine } from "./api/client";
import { journalTitle } from "./IntrastatJournalsPage";
import { countryLabel, FailureAlert, LoadAlert, taxKey, useAction } from "./shared";

export interface LineDraft {
  partner_country: string;
  commodity_code: string;
  transaction_code: string;
  transport_mode: string;
  delivery_terms: string;
  port: string;
  origin_country: string;
  net_mass: string;
  supplementary_qty: string;
  invoice_value: string;
  statistical_value: string;
}

const emptyLine: LineDraft = {
  partner_country: "",
  commodity_code: "",
  transaction_code: "11",
  transport_mode: "3",
  delivery_terms: "",
  port: "",
  origin_country: "",
  net_mass: "",
  supplementary_qty: "",
  invoice_value: "",
  statistical_value: "",
};

const AMOUNT = /^\d{1,15}(\.\d{1,6})?$/;

/** Light client checks; whether a code exists in the master data is for the journal's validation to say. */
export function validateLine(d: LineDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!/^[A-Z]{2}$/.test(d.partner_country.trim().toUpperCase())) errors.partner_country = "Enter the partner's 2-letter country code.";
  if (!/^\d{8}$/.test(d.commodity_code.trim())) errors.commodity_code = "Enter the 8-digit commodity code.";
  if (d.transaction_code.trim() === "") errors.transaction_code = "Enter the transaction code.";
  if (d.transport_mode.trim() === "") errors.transport_mode = "Enter the transport mode.";
  if (d.delivery_terms.trim() === "") errors.delivery_terms = "Enter the delivery terms, for example EXW.";
  for (const [field, label] of [
    ["net_mass", "net weight in kilograms"],
    ["invoice_value", "invoice value"],
    ["statistical_value", "statistical value"],
  ] as const) {
    if (!AMOUNT.test(d[field].trim())) errors[field] = `Enter the ${label} as a number.`;
  }
  if (d.supplementary_qty.trim() !== "" && !AMOUNT.test(d.supplementary_qty.trim())) errors.supplementary_qty = "Enter the quantity as a number.";
  return errors;
}

export function toNewLine(d: LineDraft, ref: string): NewIntrastatLine {
  return {
    source_ref: ref,
    partner_country: d.partner_country.trim().toUpperCase(),
    commodity_code: d.commodity_code.trim(),
    transaction_code: d.transaction_code.trim(),
    transport_mode: d.transport_mode.trim(),
    delivery_terms: d.delivery_terms.trim().toUpperCase(),
    port: d.port.trim(),
    origin_country: d.origin_country.trim().toUpperCase(),
    net_mass: d.net_mass.trim(),
    supplementary_qty: d.supplementary_qty.trim() === "" ? "0" : d.supplementary_qty.trim(),
    invoice_value: d.invoice_value.trim(),
    statistical_value: d.statistical_value.trim(),
  };
}

const textFields: { key: keyof LineDraft; label: string; hint?: string; optional?: boolean; mono?: boolean }[] = [
  { key: "partner_country", label: "Partner country", hint: "Where the goods came from or went to.", mono: true },
  { key: "commodity_code", label: "Commodity code", mono: true },
  { key: "transaction_code", label: "Transaction code", mono: true },
  { key: "transport_mode", label: "Transport mode", mono: true },
  { key: "delivery_terms", label: "Delivery terms", mono: true },
  { key: "port", label: "Port", optional: true, mono: true },
  { key: "origin_country", label: "Country of origin", optional: true, hint: "Required on arrivals.", mono: true },
  { key: "net_mass", label: "Net weight (kg)" },
  { key: "supplementary_qty", label: "Quantity", optional: true, hint: "Only for commodities that need one." },
  { key: "invoice_value", label: "Invoice value" },
  { key: "statistical_value", label: "Statistical value" },
];

export function IntrastatJournalPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const journal = useQuery({ queryKey: taxKey("intrastat-journal", id), queryFn: () => getJournal(id) });
  const refresh = [taxKey("intrastat-journal", id), taxKey("intrastat-journals")];
  const [draft, setDraft] = useState<LineDraft>(emptyLine);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const add = useAction((line: NewIntrastatLine) => transferLines(id, [line]), refresh, () => setDraft(emptyLine));
  const validate = useAction(() => validateJournal(id), refresh);
  const compress = useAction(() => compressJournal(id), refresh);
  const removeLine = useAction((n: number) => deleteLine(id, n), refresh);
  const correct = useAction(() => correctJournal(id), refresh, (j) => navigate(`/tax/intrastat/journals/${j.id}`));
  const exportIt = useAction(
    async () => {
      const csv = await exportJournal(id);
      const j = journal.data;
      downloadCsv(`intrastat-${j?.flow ?? "journal"}-${j?.period_from.slice(0, 7) ?? ""}.csv`, csv);
    },
    refresh,
  );

  const j = journal.data;
  const open = j !== undefined && j.status !== "exported";

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLine(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) add.run(toNewLine(draft, `manual-${Date.now().toString(36)}`));
  }

  const columns: DataTableColumn<IntrastatLine>[] = [
    { key: "line_no", header: "#", align: "end" },
    { key: "partner_country", header: "Partner", render: (l) => countryLabel(l.partner_country) },
    { key: "commodity_code", header: "Commodity", mono: true },
    { key: "transaction_code", header: "Transaction", mono: true },
    { key: "transport_mode", header: "Transport", mono: true },
    { key: "delivery_terms", header: "Terms", mono: true },
    { key: "net_mass", header: "Net kg", align: "end", mono: true },
    { key: "invoice_value", header: "Value", align: "end", mono: true },
    { key: "compressed_from", header: "Merged from", align: "end", render: (l) => (l.compressed_from > 1 ? String(l.compressed_from) : "") },
    {
      key: "errors",
      header: "Problems",
      render: (l) =>
        l.errors.length === 0 ? (
          ""
        ) : (
          <ul>
            {l.errors.map((e) => (
              <li key={e}>{e}</li>
            ))}
          </ul>
        ),
    },
    {
      key: "actions",
      header: "Actions",
      render: (l) =>
        open ? (
          <Button size="sm" variant="danger" aria-label={`Remove line ${l.line_no}`} onClick={() => removeLine.run(l.line_no)}>
            Remove
          </Button>
        ) : null,
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={j ? journalTitle(j) : "Intrastat journal"}
        meta={j ? <StatusPill status={j.status} /> : undefined}
        description={<Link to="/tax/intrastat">All Intrastat journals</Link>}
        actions={
          j ? (
            <span className={styles.filters}>
              {open ? (
                <>
                  <Button onClick={() => compress.run(undefined)} loading={compress.pending} disabled={j.line_count < 2}>
                    Merge matching lines
                  </Button>
                  <Button onClick={() => validate.run(undefined)} loading={validate.pending} disabled={j.line_count === 0}>
                    Check lines
                  </Button>
                </>
              ) : (
                <Button onClick={() => correct.run(undefined)} loading={correct.pending}>
                  Start a correction
                </Button>
              )}
              <Button variant="primary" onClick={() => exportIt.run(undefined)} loading={exportIt.pending} disabled={j.status === "draft"}>
                {j.status === "exported" ? "Download again" : "Export declaration"}
              </Button>
            </span>
          ) : undefined
        }
      />
      {j?.correction_of ? <Alert tone="info" title="This is a correction">It replaces a declaration that was already exported. Fix the lines, check them and export it again.</Alert> : null}
      {j && j.status === "draft" && j.line_count > 0 ? <Alert tone="info">Check the lines before exporting. Any edit sends the journal back to draft.</Alert> : null}
      {j && j.error_count > 0 ? (
        <Alert tone="warning" title={`${j.error_count} ${j.error_count === 1 ? "line has" : "lines have"} problems`}>
          Remove the line and add it again with the right details, or add the missing code under Master data.
        </Alert>
      ) : null}
      <FailureAlert title="Could not check the journal" failure={validate.failure} />
      <FailureAlert title="Could not merge the lines" failure={compress.failure} />
      <FailureAlert title="Could not remove the line" failure={removeLine.failure} />
      <FailureAlert title="Could not export the declaration" failure={exportIt.failure} />
      <FailureAlert title="Could not start the correction" failure={correct.failure} />
      {journal.isError ? (
        <LoadAlert title="Could not load the journal" error={journal.error} onRetry={() => void journal.refetch()} />
      ) : (
        <DataTable label="Intrastat lines" columns={columns} rows={j?.lines ?? []} getRowKey={(l) => String(l.line_no)} loading={journal.isPending} empty={{ title: "No lines yet", description: "Add the shipments or receipts to declare." }} />
      )}
      {open ? (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Add a line">
            {textFields.map((f) => (
              <FormField key={f.key} id={`line-${f.key}`} label={f.label} hint={f.hint} optional={f.optional} error={errors[f.key]}>
                {(c) => <Input {...c} className={f.mono ? styles.mono : undefined} value={draft[f.key]} onChange={(e) => setDraft({ ...draft, [f.key]: e.target.value })} invalid={Boolean(errors[f.key])} />}
              </FormField>
            ))}
            <div className={styles.formActions}>
              <FailureAlert title="Could not add the line" failure={add.failure} />
            </div>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={add.pending}>
                Add line
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </div>
  );
}
