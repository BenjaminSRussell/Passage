import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Alert, Card, Button, FormField, Input, PageHeader, StatGroup, StatTile } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { getEntitySetting, listJurisdictions, listTaxCodes, listTaxGroups, listVatBoxes, putEntitySetting, type EntitySetting } from "./api/client";
import { countryLabel, FailureAlert, fieldErrors, LoadAlert, taxKey, useAction } from "./shared";

const COUNTRY = /^[A-Z]{2}$/;

/** Client check that mirrors PUT /entity-settings; the service has the last word. */
export function validateSetting(draft: EntitySetting): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!COUNTRY.test(draft.home_country.trim().toUpperCase())) errors.home_country = "Enter the 2-letter country code, for example DE or US.";
  if (draft.vat_id.trim().length > 20) errors.vat_id = "A VAT number has at most 20 characters.";
  return errors;
}

const sections = [
  { to: "/tax/jurisdictions", title: "Jurisdictions", text: "The tax authorities, from country down to city." },
  { to: "/tax/codes", title: "Tax codes", text: "Rates by date for each tax, such as Ohio sales tax or German VAT." },
  { to: "/tax/groups", title: "Tax groups", text: "Which tax codes apply to which customers, vendors and items." },
  { to: "/tax/vat-boxes", title: "Return boxes", text: "How tax amounts add up into the boxes of the VAT return." },
  { to: "/tax/vat-return", title: "VAT return", text: "The box totals for a period." },
  { to: "/tax/esl", title: "EU sales list", text: "Sales to VAT-registered businesses in other EU countries." },
  { to: "/tax/intrastat", title: "Intrastat", text: "Monthly goods declarations for trade inside the EU." },
];

export function TaxHomePage() {
  const setting = useQuery({ queryKey: taxKey("entity-setting"), queryFn: () => getEntitySetting() });
  const jurisdictions = useQuery({ queryKey: taxKey("jurisdictions"), queryFn: () => listJurisdictions() });
  const codes = useQuery({ queryKey: taxKey("codes"), queryFn: () => listTaxCodes() });
  const party = useQuery({ queryKey: taxKey("groups", "party"), queryFn: () => listTaxGroups("party") });
  const item = useQuery({ queryKey: taxKey("groups", "item"), queryFn: () => listTaxGroups("item") });
  const boxes = useQuery({ queryKey: taxKey("vat-boxes"), queryFn: () => listVatBoxes() });

  const [draft, setDraft] = useState<EntitySetting | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const current: EntitySetting = draft ?? setting.data ?? { home_country: "", vat_id: "" };
  const save = useAction(
    (s: EntitySetting) => putEntitySetting({ home_country: s.home_country.trim().toUpperCase(), vat_id: s.vat_id.trim() }),
    [taxKey("entity-setting")],
    () => setDraft(null),
  );

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateSetting(current);
    setErrors(found);
    if (Object.keys(found).length === 0) save.run(current);
  }
  const shownErrors = { ...fieldErrors(save.failure), ...errors };
  const count = (q: { data?: unknown[] }) => q.data?.length ?? 0;

  return (
    <div className={styles.stack}>
      <PageHeader title="Tax" description="Set up the taxes this company charges and pays, then file the VAT return, the EU sales list and the Intrastat declaration from what has been recorded." />
      {setting.isError ? <LoadAlert title="Could not load the company tax settings" error={setting.error} onRetry={() => void setting.refetch()} /> : null}
      {setting.isSuccess && setting.data === null ? (
        <Alert tone="warning" title="Tax settings are not set yet">
          Enter the country this company files its VAT return in. The EU sales list and the Intrastat journal need it.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Company tax settings">
          <FormField id="tax-home-country" label="Country of the VAT return" hint="The 2-letter code, such as DE. Sales to this country are left off the EU sales list." error={shownErrors.home_country}>
            {(c) => <Input {...c} className={styles.mono} value={current.home_country} onChange={(e) => setDraft({ ...current, home_country: e.target.value })} invalid={Boolean(shownErrors.home_country)} />}
          </FormField>
          <FormField id="tax-vat-id" label="Company VAT number" optional error={shownErrors.vat_id}>
            {(c) => <Input {...c} className={styles.mono} value={current.vat_id} onChange={(e) => setDraft({ ...current, vat_id: e.target.value })} invalid={Boolean(shownErrors.vat_id)} />}
          </FormField>
          <div className={styles.formActions}>
            <FailureAlert title="Could not save the tax settings" failure={Object.keys(fieldErrors(save.failure)).length > 0 ? null : save.failure} />
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={save.pending}>
              Save tax settings
            </Button>
            {setting.data ? <span className={styles.note}>Filing from {countryLabel(setting.data.home_country)}</span> : null}
          </div>
        </form>
      </Card>
      <StatGroup label="Tax setup">
        <StatTile label="Jurisdictions" value={count(jurisdictions)} />
        <StatTile label="Tax codes" value={count(codes)} />
        <StatTile label="Customer and vendor groups" value={count(party)} />
        <StatTile label="Item groups" value={count(item)} />
        <StatTile label="Return boxes" value={count(boxes)} />
      </StatGroup>
      <Card>
        <dl className={styles.details}>
          {sections.map((s) => (
            <div key={s.to} className={styles.entry}>
              <dt className={styles.term}>
                <Link to={s.to}>{s.title}</Link>
              </dt>
              <dd className={styles.value}>{s.text}</dd>
            </div>
          ))}
        </dl>
      </Card>
    </div>
  );
}
