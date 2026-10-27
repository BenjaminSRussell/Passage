import { useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, Textarea, type DataTableColumn } from "../../shell/ui";
import { checkPunchout, describeProblem, type CartLine, type CartResult, type Problem, type PunchoutFormat } from "./api/client";
import { dash } from "./format";
import { IssueList, ProblemAlert } from "./parts";
import styles from "./pages.module.css";

/** Splits "KEY=VALUE" lines into a record; blank lines are skipped. Returns the 1-based numbers of lines without "=". */
export function parsePairs(text: string): { pairs: Record<string, string>; bad: number[] } {
  const pairs: Record<string, string> = {};
  const bad: number[] = [];
  text.split("\n").forEach((raw, i) => {
    const line = raw.trim();
    if (line === "") return;
    const eq = line.indexOf("=");
    if (eq <= 0) {
      bad.push(i + 1);
      return;
    }
    pairs[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  });
  return { pairs, bad };
}

const OCI_EXAMPLE = `NEW_ITEM-VENDORMAT[1]=OCI-100
NEW_ITEM-DESCRIPTION[1]=Hex nut M8
NEW_ITEM-QUANTITY[1]=40
NEW_ITEM-UNIT[1]=EA
NEW_ITEM-PRICE[1]=12.50
NEW_ITEM-PRICEUNIT[1]=100
NEW_ITEM-CURRENCY[1]=USD
NEW_ITEM-EXT_SCHEMA_TYPE[1]=UNSPSC
NEW_ITEM-EXT_CATEGORY_ID[1]=31161600
NEW_ITEM-VENDORMAT[2]=OCI-200
NEW_ITEM-DESCRIPTION[2]=Flange DN50
NEW_ITEM-QUANTITY[2]=2
NEW_ITEM-UNIT[2]=EA
NEW_ITEM-PRICE[2]=18.50
NEW_ITEM-CURRENCY[2]=USD
NEW_ITEM-LEADTIME[2]=5`;

const CXML_EXAMPLE = `<cXML payloadID="example@supplier" timestamp="2026-10-27T10:00:00Z">
 <Message><PunchOutOrderMessage>
  <BuyerCookie>session-1</BuyerCookie>
  <PunchOutOrderMessageHeader operationAllowed="create">
   <Total><Money currency="USD">40.00</Money></Total>
  </PunchOutOrderMessageHeader>
  <ItemIn quantity="2">
   <ItemID><SupplierPartID>SUP-100</SupplierPartID></ItemID>
   <ItemDetail><UnitPrice><Money currency="USD">10.00</Money></UnitPrice>
    <Description xml:lang="en">Bolt M8</Description><UnitOfMeasure>EA</UnitOfMeasure>
    <Classification domain="UNSPSC">31161501</Classification></ItemDetail>
  </ItemIn>
  <ItemIn quantity="5">
   <ItemID><SupplierPartID>SUP-200</SupplierPartID></ItemID>
   <ItemDetail><UnitPrice><Money currency="USD">4.00</Money></UnitPrice>
    <Description xml:lang="en">Washer</Description><UnitOfMeasure>KG</UnitOfMeasure></ItemDetail>
  </ItemIn>
 </PunchOutOrderMessage></Message>
</cXML>`;

const columns: DataTableColumn<CartLine>[] = [
  { key: "line", header: "Line", align: "end" },
  { key: "supplierPartId", header: "Supplier part", mono: true },
  { key: "description", header: "Description", render: (row) => row.description || dash },
  { key: "quantity", header: "Quantity", align: "end", render: (row) => `${row.quantity} ${row.unitCode}` },
  { key: "unitPrice", header: "Unit price", align: "end", render: (row) => `${row.unitPrice} ${row.currency}` },
  { key: "unspsc", header: "UNSPSC", mono: true, render: (row) => row.unspsc || dash },
  { key: "leadTimeDays", header: "Lead time", align: "end", render: (row) => (row.leadTimeDays > 0 ? `${row.leadTimeDays} d` : dash) },
  {
    key: "needsApproval",
    header: "Approval",
    render: (row) => (row.needsApproval ? <Badge tone="warning">Above catalog price</Badge> : <Badge tone="success">Catalog price</Badge>),
  },
];

interface Values {
  format: PunchoutFormat;
  document: string;
  fields: string;
  currencies: string;
  tolerancePct: string;
  uomMap: string;
  referencePrices: string;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Client checks before the request; the service applies BR-35.13 itself. */
export function validatePunchout(values: Values): Errors {
  const errors: Errors = {};
  if (values.format === "CXML" && values.document.trim() === "") errors.document = "Paste the PunchOutOrderMessage.";
  if (values.format === "OCI") {
    const { pairs, bad } = parsePairs(values.fields);
    if (bad.length > 0) errors.fields = `Lines ${bad.join(", ")} are not FIELD=value pairs.`;
    else if (Object.keys(pairs).length === 0) errors.fields = "Paste the NEW_ITEM fields of the form post.";
  }
  if (values.currencies.split(",").every((c) => c.trim() === "")) errors.currencies = "Name at least one currency.";
  const tol = Number(values.tolerancePct);
  if (values.tolerancePct.trim() !== "" && !(tol >= 0 && tol <= 20)) errors.tolerancePct = "Enter a percentage from 0 to 20.";
  for (const key of ["uomMap", "referencePrices"] as const) {
    const { bad } = parsePairs(values[key]);
    if (bad.length > 0) errors[key] = `Lines ${bad.join(", ")} are not KEY=value pairs.`;
  }
  return errors;
}

/**
 * Check a supplier's returned punchout cart (FR-35.13): which requisition lines
 * it would create, which lines are dropped (EDI-0133), whether the total holds
 * (EDI-0134) and which prices need approval. Nothing is created.
 */
export function PunchoutCheckPage() {
  const [values, setValues] = useState<Values>({
    format: "OCI",
    document: CXML_EXAMPLE,
    fields: OCI_EXAMPLE,
    currencies: "USD",
    tolerancePct: "0",
    uomMap: "",
    referencePrices: "OCI-200=18.00",
  });
  const [errors, setErrors] = useState<Errors>({});
  const [result, setResult] = useState<CartResult | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<Values>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validatePunchout(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const supplier = {
        currencies: values.currencies.split(",").map((c) => c.trim()).filter(Boolean),
        tolerancePct: values.tolerancePct.trim() || undefined,
        uomMap: parsePairs(values.uomMap).pairs,
        referencePrices: parsePairs(values.referencePrices).pairs,
      };
      setResult(
        await checkPunchout(
          values.format === "CXML"
            ? { format: "CXML", document: values.document, supplier }
            : { format: "OCI", fields: parsePairs(values.fields).pairs, supplier },
        ),
      );
    } catch (error) {
      setResult(null);
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Punchout cart check"
        description="Paste the cart a supplier catalog returned, as a cXML PunchOutOrderMessage or an SAP OCI form post, to see the requisition lines it would create. Nothing is stored and no requisition is created."
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="punchout-format" label="Cart format">
            {(control) => (
              <Select {...control} value={values.format} onChange={(event) => update({ format: event.target.value as PunchoutFormat })}>
                <option value="OCI">SAP OCI form post</option>
                <option value="CXML">cXML PunchOutOrderMessage</option>
              </Select>
            )}
          </FormField>
          <FormField id="punchout-currencies" label="Allowed currencies" hint="Comma-separated ISO 4217 codes." error={errors.currencies}>
            {(control) => <Input {...control} value={values.currencies} onChange={(event) => update({ currencies: event.target.value })} invalid={Boolean(errors.currencies)} />}
          </FormField>
          <FormField id="punchout-tolerance" label="Price tolerance (%)" hint="0 to 20. A price above the reference price plus this needs approval." error={errors.tolerancePct}>
            {(control) => (
              <Input {...control} inputMode="decimal" value={values.tolerancePct} onChange={(event) => update({ tolerancePct: event.target.value })} invalid={Boolean(errors.tolerancePct)} />
            )}
          </FormField>
          <div className={styles.wide}>
            {values.format === "CXML" ? (
              <FormField id="punchout-document" label="PunchOutOrderMessage" error={errors.document}>
                {(control) => <Textarea {...control} mono rows={12} value={values.document} onChange={(event) => update({ document: event.target.value })} invalid={Boolean(errors.document)} />}
              </FormField>
            ) : (
              <FormField id="punchout-fields" label="OCI fields" hint="One NEW_ITEM-FIELD[n]=value per line, as posted to the HOOK_URL." error={errors.fields}>
                {(control) => <Textarea {...control} mono rows={12} value={values.fields} onChange={(event) => update({ fields: event.target.value })} invalid={Boolean(errors.fields)} />}
              </FormField>
            )}
          </div>
          <FormField id="punchout-uom" label="Supplier units" optional hint="SUPPLIER_UNIT=UN/ECE code per line; overrides the default table." error={errors.uomMap}>
            {(control) => <Textarea {...control} mono rows={3} value={values.uomMap} onChange={(event) => update({ uomMap: event.target.value })} invalid={Boolean(errors.uomMap)} />}
          </FormField>
          <FormField id="punchout-prices" label="Reference prices" optional hint="SUPPLIER_PART=price per line." error={errors.referencePrices}>
            {(control) => (
              <Textarea {...control} mono rows={3} value={values.referencePrices} onChange={(event) => update({ referencePrices: event.target.value })} invalid={Boolean(errors.referencePrices)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Check cart
            </Button>
          </div>
        </form>
      </Card>
      <ProblemAlert problem={failure} title="The cart could not be checked" />
      {result ? (
        <Card title="Result" headingLevel={2}>
          <div className={styles.stack}>
            {result.cartRejected ? (
              <Alert tone="danger" title="Cart rejected" live="status">
                {`The stated total ${result.statedTotal ?? dash} differs from the line sum ${result.computedTotal}. No line would be created; repeat the punchout.`}
              </Alert>
            ) : (
              <Alert tone="success" title={`${result.lines.length} requisition line${result.lines.length === 1 ? "" : "s"} would be created`} live="status">
                {`Line sum ${result.computedTotal}${result.statedTotal ? `, stated total ${result.statedTotal}` : " (OCI carries no total)"}. Lines are flagged as catalog-sourced.`}
              </Alert>
            )}
            <IssueList issues={result.issues} label="Cart findings" />
            <DataTable
              label="Requisition lines"
              columns={columns}
              rows={result.lines}
              getRowKey={(row) => String(row.line)}
              empty={{ title: "No lines would be created", description: result.cartRejected ? "The whole cart is rejected." : "Every returned line was dropped." }}
            />
          </div>
        </Card>
      ) : null}
    </div>
  );
}
