import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  createAssociation,
  describeProblem,
  listAssociations,
  listTestGroups,
  newKey,
  type Association,
  type CertRequirement,
  type NewAssociation,
  type Problem,
  type TestGroup,
  type Trigger,
} from "./api/client";
import { CERT_LABELS, CERT_REQUIREMENTS, describePlan, pickFields, shortId, TRIGGER_LABELS, UUID } from "./format";
import styles from "./pages.module.css";
import { emptyPlan, planFieldErrors, PlanFields, validatePlan, type PlanErrors, type PlanValues } from "./PlanFields";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import { LookupFormField } from "../../shell/lookup/LookupFormField";

type AssocTrigger = Exclude<Trigger, "MANUAL">;
const TRIGGERS = (Object.keys(TRIGGER_LABELS) as Trigger[]).filter((t): t is AssocTrigger => t !== "MANUAL");

/** What a scope's reference names, and the lookup that finds it. */
const REF_ENTITIES: Record<string, LookupEntityKey> = { ITEM: "product", ITEM_GROUP: "itemGroup", PARTY: "party", SITE: "site" };
const REF_NOUNS: Record<string, string> = { ITEM: "item", ITEM_GROUP: "item group", PARTY: "vendor or customer", SITE: "site" };

function scope(kind: string, ref?: string): string {
  return kind === "ALL" ? "All" : `${kind === "ITEM_GROUP" ? "Group" : kind.charAt(0) + kind.slice(1).toLowerCase()} ${shortId(ref)}`;
}

function columns(groups: TestGroup[]): DataTableColumn<Association>[] {
  const names = new Map(groups.map((g) => [g.id, `${g.code} v${g.version}`]));
  return [
    { key: "sequence", header: "Sequence", align: "end", render: (row) => String(row.sequence) },
    { key: "trigger", header: "Trigger", render: (row) => TRIGGER_LABELS[row.trigger] ?? row.trigger },
    { key: "item", header: "Item", mono: true, render: (row) => scope(row.itemScope, row.itemRef) },
    { key: "party", header: "Vendor or customer", mono: true, render: (row) => scope(row.partyScope, row.partyRef) },
    { key: "site", header: "Site", mono: true, render: (row) => scope(row.siteScope, row.siteRef) },
    { key: "testGroupId", header: "Test group", render: (row) => names.get(row.testGroupId) ?? shortId(row.testGroupId) },
    { key: "samplingPlan", header: "Sampling", render: (row) => describePlan(row.samplingPlan) },
    { key: "certificateRequired", header: "Vendor certificate", render: (row) => CERT_LABELS[row.certificateRequired ?? "NONE"] },
  ];
}

/** Quality associations (FR-33.3, BR-33.3): which trigger events create a quality order, with which tests and sampling. */
export function AssociationsPage() {
  const state = useLoad(async (signal) => {
    const [assocs, groups] = await Promise.all([listAssociations(signal), listTestGroups(signal)]);
    return { assocs, groups };
  }, "associations");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Quality associations"
        description="The most specific association wins: an item match scores 4, an item group 3, a vendor or customer 2 and a site 1. Ties go to the lowest sequence."
        actions={
          <Link className={buttonClassName("primary")} to="/quality/associations/new">
            New association
          </Link>
        }
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load quality associations" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable
          label="Quality associations"
          columns={columns(state.data?.groups ?? [])}
          rows={state.data?.assocs ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No quality associations yet", description: "Without an association, receipts and production create no quality orders. Add one per trigger you inspect." }}
        />
      )}
    </div>
  );
}

interface Values {
  trigger: AssocTrigger;
  itemScope: Association["itemScope"];
  itemRef: string;
  partyScope: Association["partyScope"];
  partyRef: string;
  siteScope: Association["siteScope"];
  siteRef: string;
  sequence: string;
  testGroupId: string;
  certificateRequired: CertRequirement;
}

type Errors = Partial<Record<keyof Values, string>>;

/** Checks that mirror the service's ValidateAssociation, and the request the form sends. */
export function validateAssociation(values: Values, plan: PlanValues): { errors: Errors; planErrors: PlanErrors; input?: NewAssociation } {
  const errors: Errors = {};
  const refs = [
    ["itemScope", "itemRef"],
    ["partyScope", "partyRef"],
    ["siteScope", "siteRef"],
  ] as const;
  for (const [s, r] of refs) {
    if (values[s] !== "ALL" && !UUID.test(values[r].trim())) errors[r] = "Choose what this scope applies to.";
  }
  if (!/^\d{1,6}$/.test(values.sequence.trim())) errors.sequence = "Enter a whole number, 0 or more.";
  if (!UUID.test(values.testGroupId)) errors.testGroupId = "Choose a test group.";
  const checked = validatePlan(plan);
  if (Object.keys(errors).length > 0 || !checked.plan) return { errors, planErrors: checked.errors };
  const input: NewAssociation = {
    trigger: values.trigger,
    itemScope: values.itemScope,
    partyScope: values.partyScope,
    siteScope: values.siteScope,
    sequence: Number(values.sequence),
    testGroupId: values.testGroupId,
    samplingPlan: checked.plan,
    certificateRequired: values.certificateRequired,
  };
  for (const [s, r] of refs) {
    if (values[s] !== "ALL") input[r] = values[r].trim();
  }
  return { errors, planErrors: {}, input };
}

/** Create a quality association. */
export function NewAssociationPage() {
  const navigate = useNavigate();
  const groups = useLoad((signal) => listTestGroups(signal), "test-groups");
  const [values, setValues] = useState<Values>({
    trigger: "PURCHASE_RECEIPT",
    itemScope: "ALL",
    itemRef: "",
    partyScope: "ALL",
    partyRef: "",
    siteScope: "ALL",
    siteRef: "",
    sequence: "10",
    testGroupId: "",
    certificateRequired: "NONE",
  });
  const [plan, setPlan] = useState<PlanValues>(emptyPlan);
  const [errors, setErrors] = useState<Errors>({});
  const [planErrors, setPlanErrors] = useState<PlanErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<Values>) {
    setValues((prev) => ({ ...prev, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const checked = validateAssociation(values, plan);
    setErrors(checked.errors);
    setPlanErrors(checked.planErrors);
    setFailure(null);
    if (!checked.input) return;
    setBusy(true);
    try {
      await createAssociation(checked.input, key.current);
      navigate("/quality/associations");
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = pickFields(problem.fields, ["trigger", "sequence", "testGroupId", "certificateRequired"] as const);
      if (problem.fields.itemScope) fieldErrors.itemRef = problem.fields.itemScope;
      if (problem.fields.partyScope) fieldErrors.partyRef = problem.fields.partyScope;
      if (problem.fields.siteScope) fieldErrors.siteRef = problem.fields.siteScope;
      if (problem.code === "QM-0140") fieldErrors.testGroupId = "This test group no longer exists.";
      const fromPlan = planFieldErrors(problem.fields);
      if (Object.keys(fieldErrors).length > 0 || Object.keys(fromPlan).length > 0) {
        setErrors(fieldErrors);
        setPlanErrors(fromPlan);
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  }

  const scopeField = (
    s: "itemScope" | "partyScope" | "siteScope",
    r: "itemRef" | "partyRef" | "siteRef",
    label: string,
    options: [string, string][],
  ) => (
    <>
      <FormField id={`assoc-${s}`} label={label}>
        {(control) => (
          <Select {...control} value={values[s]} onChange={(event) => update({ [s]: event.target.value, [r]: "" } as Partial<Values>)}>
            {options.map(([value, text]) => (
              <option key={value} value={value}>
                {text}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      {values[s] !== "ALL" ? (
        <LookupFormField
          key={values[s]}
          id={`assoc-${r}`}
          label={`Which ${REF_NOUNS[values[s]] ?? label.toLowerCase()}`}
          entity={REF_ENTITIES[values[s]] ?? "party"}
          value={values[r]}
          error={errors[r]}
          onChange={(v) => update({ [r]: v } as Partial<Values>)}
        />
      ) : null}
    </>
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New quality association" description="One quality order is created per source line and trigger event that this association wins." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New quality association">
          <FormField id="assoc-trigger" label="Trigger" error={errors.trigger}>
            {(control) => (
              <Select {...control} value={values.trigger} onChange={(event) => update({ trigger: event.target.value as AssocTrigger })}>
                {TRIGGERS.map((t) => (
                  <option key={t} value={t}>
                    {TRIGGER_LABELS[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {scopeField("itemScope", "itemRef", "Item", [
            ["ALL", "All items"],
            ["ITEM", "One item"],
            ["ITEM_GROUP", "Item group"],
          ])}
          {scopeField("partyScope", "partyRef", "Vendor or customer", [
            ["ALL", "All vendors and customers"],
            ["PARTY", "One vendor or customer"],
          ])}
          {scopeField("siteScope", "siteRef", "Site", [
            ["ALL", "All sites"],
            ["SITE", "One site"],
          ])}
          <FormField id="assoc-sequence" label="Sequence" hint="Lowest wins on equal specificity." error={errors.sequence}>
            {(control) => <Input {...control} inputMode="numeric" value={values.sequence} invalid={Boolean(errors.sequence)} onChange={(event) => update({ sequence: event.target.value })} />}
          </FormField>
          <FormField id="assoc-testGroupId" label="Test group" error={errors.testGroupId ?? (groups.error ? `Test groups could not be loaded: ${groups.error.message}` : undefined)}>
            {(control) => (
              <Select {...control} value={values.testGroupId} invalid={Boolean(errors.testGroupId)} onChange={(event) => update({ testGroupId: event.target.value })}>
                <option value="">{groups.loading ? "Loading test groups…" : "Choose a test group"}</option>
                {(groups.data ?? []).map((g) => (
                  <option key={g.id} value={g.id}>{`${g.code} v${g.version}`}</option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="assoc-certificateRequired" label="Vendor certificate" hint="Orders from this association cannot be accepted until it is received in specification or waived." error={errors.certificateRequired}>
            {(control) => (
              <Select {...control} value={values.certificateRequired} onChange={(event) => update({ certificateRequired: event.target.value as CertRequirement })}>
                {CERT_REQUIREMENTS.map((c) => (
                  <option key={c} value={c}>
                    {CERT_LABELS[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <PlanFields idPrefix="assoc" values={plan} errors={planErrors} onChange={(patch) => setPlan({ ...plan, ...patch })} />
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the association" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save association
            </Button>
            <Link className={buttonClassName("ghost")} to="/quality/associations">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}
