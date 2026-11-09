import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import {
  previewDivestiture,
  previewEquityMethod,
  type DivestitureRequest,
  type EquityMethodPreview,
  type EquityMethodRequest,
} from "./api/client";
import { entryColumns } from "./entries";
import { Fields, type FieldSpec } from "./Fields";
import { amount, isCurrency, isDecimal, isNonZero, isPositive, isSignedDecimal } from "./format";
import { usePreview, type Checked } from "./usePreview";
import styles from "./pages.module.css";

type EquityKey = "currency" | "ownershipPct" | "openingCarrying" | "netIncome" | "dividendsReceived";
type DivestKey = "currency" | "proceeds" | "netAssetsCarrying" | "goodwill" | "nciCarrying" | "ctaRecycled";

const equityFields: FieldSpec<EquityKey>[] = [
  { name: "currency", label: "Group currency", mono: true },
  { name: "ownershipPct", label: "Ownership (%)", decimal: true, hint: "Above 0 and at most 50; control is consolidated line by line." },
  { name: "openingCarrying", label: "Opening carrying amount", decimal: true },
  { name: "netIncome", label: "Associate net income", decimal: true, hint: "Negative for a loss." },
  { name: "dividendsReceived", label: "Dividends received", decimal: true, optional: true },
];

const divestFields: FieldSpec<DivestKey>[] = [
  { name: "currency", label: "Group currency", mono: true },
  { name: "proceeds", label: "Proceeds", decimal: true },
  { name: "netAssetsCarrying", label: "Net assets at group carrying amount", decimal: true },
  { name: "goodwill", label: "Goodwill", decimal: true, optional: true },
  { name: "nciCarrying", label: "Non-controlling interest", decimal: true, optional: true },
  { name: "ctaRecycled", label: "CTA recycled from OCI", decimal: true, optional: true, hint: "Positive when the translation reserve is a gain." },
];

/** AC-40.14: a 30% associate, investment 300,000.00, net income 200,000.00, dividends 20,000.00. */
export const ac4014: Record<EquityKey, string> = {
  currency: "USD",
  ownershipPct: "30",
  openingCarrying: "300000.00",
  netIncome: "200000.00",
  dividendsReceived: "20000.00",
};

export const sampleDivestiture: Record<DivestKey, string> = {
  currency: "USD",
  proceeds: "500000.00",
  netAssetsCarrying: "300000.00",
  goodwill: "50000.00",
  nciCarrying: "20000.00",
  ctaRecycled: "10000.00",
};

/** Above 0 and at most 50: an associate or joint venture, not a controlled subsidiary. */
const percentText = /^0*([1-4]?\d(\.\d+)?|50(\.0+)?)$/;

/** Checks that mirror the service, and the request the equity-method form sends. */
export function validateEquity(values: Record<EquityKey, string>): Checked<EquityMethodRequest> {
  const errors: Record<string, string> = {};
  const v = (k: EquityKey) => values[k].trim();
  if (!isCurrency(v("currency").toUpperCase())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!(isPositive(v("ownershipPct")) && percentText.test(v("ownershipPct")))) errors.ownershipPct = "Enter an ownership above 0 and at most 50.";
  if (!isDecimal(v("openingCarrying"))) errors.openingCarrying = "Enter the carrying amount, 0 or more.";
  if (!isSignedDecimal(v("netIncome"))) errors.netIncome = "Enter the associate's net income.";
  if (v("dividendsReceived") !== "" && !isDecimal(v("dividendsReceived"))) errors.dividendsReceived = "Enter the dividends, 0 or more.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      currency: v("currency").toUpperCase(),
      ownershipPct: v("ownershipPct"),
      openingCarrying: v("openingCarrying"),
      netIncome: v("netIncome"),
      dividendsReceived: v("dividendsReceived") || undefined,
    },
  };
}

/** Checks that mirror the service, and the request the divestiture form sends. */
export function validateDivestiture(values: Record<DivestKey, string>): Checked<DivestitureRequest> {
  const errors: Record<string, string> = {};
  const v = (k: DivestKey) => values[k].trim();
  if (!isCurrency(v("currency").toUpperCase())) errors.currency = "Enter a three-letter currency code such as USD.";
  if (!isDecimal(v("proceeds"))) errors.proceeds = "Enter the proceeds, 0 or more.";
  if (!isSignedDecimal(v("netAssetsCarrying"))) errors.netAssetsCarrying = "Enter the net assets disposed.";
  for (const k of ["goodwill", "nciCarrying"] as const) {
    if (v(k) !== "" && !isDecimal(v(k))) errors[k] = "Enter an amount, 0 or more, or leave it empty.";
  }
  if (v("ctaRecycled") !== "" && !isSignedDecimal(v("ctaRecycled"))) errors.ctaRecycled = "Enter an amount, or leave it empty.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    request: {
      currency: v("currency").toUpperCase(),
      proceeds: v("proceeds"),
      netAssetsCarrying: v("netAssetsCarrying"),
      goodwill: v("goodwill") || undefined,
      nciCarrying: v("nciCarrying") || undefined,
      ctaRecycled: v("ctaRecycled") || undefined,
    },
  };
}

const equityEntries = entryColumns<EquityMethodPreview["entries"][number]>();

/** Equity method for associates and the gain on a full divestiture (FR-40.18, AC-40.14). */
export function InvestmentsPage() {
  const [equity, setEquity] = useState(ac4014);
  const [divest, setDivest] = useState(sampleDivestiture);
  const eq = usePreview(previewEquityMethod);
  const dv = usePreview(previewDivestiture);

  function submitEquity(event: FormEvent) {
    event.preventDefault();
    void eq.run(validateEquity(equity));
  }

  function submitDivest(event: FormEvent) {
    event.preventDefault();
    void dv.run(validateDivestiture(divest));
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Investments"
        description="An associate is not consolidated line by line: the investor takes its share of the associate's profit into the investment, and dividends received reduce it. On a full divestiture the gain is the proceeds less the group carrying amount, plus the translation reserve recycled from OCI. Recording investments, ownership changes and step acquisitions arrive with the group structure slice."
      />
      <Card title="Equity method" headingLevel={3}>
        <form className={styles.form} onSubmit={submitEquity} noValidate>
          <Fields idPrefix="eq" fields={equityFields} values={equity} errors={eq.errors} onChange={setEquity} />
          {eq.failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The equity method was not applied" live="alert">
                {eq.failure.message} ({eq.failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={eq.busy}>
              Apply equity method
            </Button>
          </div>
        </form>
      </Card>
      {eq.busy ? (
        <DataTable label="Equity-method entries" columns={equityEntries} rows={[]} getRowKey={(_, i) => String(i)} loading />
      ) : eq.result ? (
        <>
          <StatGroup label="Equity method">
            <StatTile label="Share of profit" value={`${amount(eq.result.shareOfProfit)} ${eq.result.currency}`} />
            <StatTile label="Closing carrying amount" value={amount(eq.result.closingCarrying)} />
            {isNonZero(eq.result.unrecognizedLoss) ? (
              <StatTile label="Unrecognized loss" value={amount(eq.result.unrecognizedLoss)} tone="warning" />
            ) : null}
          </StatGroup>
          <DataTable label="Equity-method entries" columns={equityEntries} rows={eq.result.entries} getRowKey={(e, i) => `${e.account}-${i}`} />
        </>
      ) : (
        <Card>
          <EmptyState
            icon="percent"
            headingLevel={3}
            title="No equity-method result yet"
            description="The form is filled in with an example. Apply it, or enter your own associate."
          />
        </Card>
      )}
      <Card title="Divestiture" headingLevel={3}>
        <form className={styles.form} onSubmit={submitDivest} noValidate>
          <Fields idPrefix="dv" fields={divestFields} values={divest} errors={dv.errors} onChange={setDivest} />
          {dv.failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="The divestiture gain was not computed" live="alert">
                {dv.failure.message} ({dv.failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={dv.busy}>
              Compute gain
            </Button>
          </div>
        </form>
      </Card>
      {dv.result ? (
        <StatGroup label="Divestiture">
          <StatTile label="Carrying amount disposed" value={`${amount(dv.result.carryingAmount)} ${dv.result.currency}`} />
          <StatTile label={dv.result.gain.startsWith("-") ? "Loss on disposal" : "Gain on disposal"} value={amount(dv.result.gain)} />
        </StatGroup>
      ) : (
        <Card>
          <EmptyState
            icon="receipt"
            headingLevel={3}
            title="No divestiture result yet"
            description="Enter the proceeds and what the group carried for the subsidiary to see the gain or loss on disposal."
          />
        </Card>
      )}
    </div>
  );
}
