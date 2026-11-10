import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input } from "../../shell/ui";
import { describeProblem, newKey, PricingApiError, reviseCampaign, type Campaign, type CampaignRevision, type Problem } from "./api/client";
import { addDays, DAY, DECIMAL, today } from "./format";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

type Errors = Partial<Record<"name" | "validFrom" | "validTo" | "targetCustomerGroup" | "budget" | "form", string>>;

/** What a refused edit tells the planner, by service code. */
function failureTitle(code: string): string {
  if (code === "conflict_version") return "The campaign changed since you opened it";
  if (code === "PRC-0041") return "The campaign no longer exists";
  return "The change failed";
}

/**
 * Edit a campaign's name, validity, target customer group and budget (FR-26.10). The change
 * reaches the next price resolution; the currency cannot change. A killed campaign can be edited too.
 * An edit never changes how a day before today priced (the service refuses it): once the campaign
 * has started its start date and customer group are read-only and its end date cannot drop days
 * already priced; an ended campaign keeps its end date.
 */
export function CampaignEditCard({ campaign, onChange }: { campaign: Campaign; onChange: (next: Campaign) => void }) {
  const [name, setName] = useState(campaign.name);
  const [validFrom, setValidFrom] = useState(campaign.validFrom);
  const [validTo, setValidTo] = useState(campaign.validTo);
  const [group, setGroup] = useState(campaign.targetCustomerGroup);
  const [budget, setBudget] = useState(campaign.budget);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");
  const key = useRef(newKey());
  const now = today();
  const yesterday = addDays(now, -1);
  const started = campaign.validFrom < now;
  const ended = campaign.validTo < now;

  function change(set: (value: string) => void) {
    return (value: string) => {
      set(value);
      key.current = newKey();
    };
  }

  /** The fields that differ from the campaign as loaded; empty when nothing changed. */
  function diff(): CampaignRevision {
    const out: CampaignRevision = {};
    if (name.trim() !== campaign.name) out.name = name.trim();
    if (validFrom !== campaign.validFrom) out.validFrom = validFrom;
    if (validTo !== campaign.validTo) out.validTo = validTo;
    if (group.trim() !== campaign.targetCustomerGroup) out.targetCustomerGroup = group.trim();
    if (budget.trim() !== campaign.budget) out.budget = budget.trim();
    return out;
  }

  function check(): boolean {
    const next: Errors = {};
    if (name.trim() === "" || name.length > 120) next.name = "Enter a name of 1 to 120 characters.";
    if (!DAY.test(validFrom)) next.validFrom = "Enter the start date as YYYY-MM-DD.";
    if (!DAY.test(validTo)) next.validTo = "Enter the end date as YYYY-MM-DD.";
    else if (DAY.test(validFrom) && validTo < validFrom) next.validTo = "The end date cannot be before the start date.";
    else if (started && !ended && validTo < yesterday) next.validTo = `End it on ${yesterday} or later: the days already priced stay in the campaign.`;
    if (!started && DAY.test(validFrom) && validFrom !== campaign.validFrom && validFrom < now) next.validFrom = "The start date cannot be in the past.";
    if (group.length > 64) next.targetCustomerGroup = "Use at most 64 characters.";
    if (!DECIMAL.test(budget.trim())) next.budget = "Enter the budget as a number, 0 or more.";
    if (Object.keys(next).length === 0 && Object.keys(diff()).length === 0) next.form = "Change at least one term.";
    setErrors(next);
    return Object.keys(next).length === 0;
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    setDone("");
    if (!check()) return;
    setBusy(true);
    try {
      const changed = await reviseCampaign(campaign.id, campaign.version, diff(), key.current);
      onChange(changed);
      setName(changed.name);
      setValidFrom(changed.validFrom);
      setValidTo(changed.validTo);
      setGroup(changed.targetCustomerGroup);
      setBudget(changed.budget);
      setDone("The campaign is updated. Price resolution uses the new terms from the next request.");
      key.current = newKey();
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem);
      const fieldErrors: Errors = {};
      for (const name of ["name", "validFrom", "validTo", "targetCustomerGroup", "budget"] as const) {
        if (problem.fields[name]) fieldErrors[name] = problem.fields[name];
      }
      setErrors(fieldErrors);
      // A refused command is final; only a network or server failure may be retried with the same key.
      if (error instanceof PricingApiError && error.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Edit campaign">
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {done}
        </Alert>
        {failure ? (
          <Alert tone="danger" title={failureTitle(failure.code)} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <form className={styles.form} onSubmit={submit} noValidate>
          <FormField id="cmp-edit-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={name} maxLength={120} onChange={(e) => change(setName)(e.target.value)} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField
            id="cmp-edit-group"
            label="Customer group"
            hint={started ? "Fixed once the campaign has started." : "Leave empty for every customer."}
            optional
            error={errors.targetCustomerGroup}
          >
            {(c) => <Input {...c} value={group} maxLength={64} readOnly={started} onChange={(e) => change(setGroup)(e.target.value)} invalid={Boolean(errors.targetCustomerGroup)} />}
          </FormField>
          <FormField id="cmp-edit-from" label="Valid from" hint={started ? `Fixed: the campaign has priced since ${campaign.validFrom}.` : undefined} error={errors.validFrom}>
            {(c) => <Input {...c} type="date" value={validFrom} readOnly={started} onChange={(e) => change(setValidFrom)(e.target.value)} invalid={Boolean(errors.validFrom)} />}
          </FormField>
          <FormField id="cmp-edit-to" label="Valid to" hint={ended ? `Fixed: the campaign ended on ${campaign.validTo}.` : undefined} error={errors.validTo}>
            {(c) => <Input {...c} type="date" value={validTo} readOnly={ended} onChange={(e) => change(setValidTo)(e.target.value)} invalid={Boolean(errors.validTo)} />}
          </FormField>
          <FormField id="cmp-edit-budget" label={`Budget (${campaign.currency})`} error={errors.budget}>
            {(c) => <Input {...c} inputMode="decimal" value={budget} onChange={(e) => change(setBudget)(e.target.value)} invalid={Boolean(errors.budget)} />}
          </FormField>
          <p className={`${styles.note} ${styles.wide}`}>
            The new validity and customer group apply from the next price resolution; days already priced keep their prices. The budget is recorded; the
            currency cannot change.
          </p>
          {errors.form ? (
            <Alert tone="warning" live="alert">
              {errors.form}
            </Alert>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
          </div>
        </form>
      </div>
    </Card>
  );
}
