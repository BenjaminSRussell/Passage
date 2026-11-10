import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, Skeleton, buttonClassName, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getPolicy,
  listPolicies,
  savePolicy,
  type CategoryAccessMode,
  type PurchasingPolicy,
  type PurchasingPolicyInput,
} from "./api/client";
import { Details, LoadError, ShortId, uuidPattern, useResource, type Failure } from "./parts";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const yesNo = (v: boolean) => (v ? "On" : "Off");

const accessModes: Record<CategoryAccessMode, { label: string; hint: string }> = {
  HARD: { label: "Hard", hint: "A requisition or order whose vendor is not assigned to a restricted category is refused." },
  SOFT: { label: "Soft", hint: "The same vendor is allowed, with a warning on the requisition or consolidation." },
  OFF: { label: "Off", hint: "Category vendor assignments are not checked." },
};

const columns: DataTableColumn<PurchasingPolicy>[] = [
  { key: "legalEntityId", header: "Legal entity", render: (p) => <ShortId id={p.legalEntityId} /> },
  { key: "changeManagementDefault", header: "Change management by default", render: (p) => yesNo(p.changeManagementDefault) },
  { key: "poFourEyes", header: "Purchase order four-eyes", render: (p) => yesNo(p.poFourEyes) },
  { key: "allowInvoiceBeforeReceipt", header: "Invoice before receipt", render: (p) => yesNo(p.allowInvoiceBeforeReceipt) },
  { key: "categoryAccessMode", header: "Category access", render: (p) => accessModes[p.categoryAccessMode]?.label ?? p.categoryAccessMode },
  { key: "version", header: "Version", align: "end" },
];

/** Saved purchasing policies, and a way to open or create the policy of any legal entity (FR-17.13). */
export function PolicyListPage() {
  const navigate = useNavigate();
  const policies = useResource((signal) => listPolicies(signal), []);
  const [entity, setEntity] = useState("");
  const [error, setError] = useState<string | undefined>();

  function open(e: FormEvent) {
    e.preventDefault();
    const id = entity.trim();
    if (!uuidPattern.test(id)) {
      setError("Enter the legal entity id (a UUID).");
      return;
    }
    navigate(`/purchasing/policies/${id}`);
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Purchasing policies"
        description="Settings per legal entity: change management by default, a second person for purchase order approval, and invoicing ahead of receipt. A legal entity with no saved policy uses the defaults."
      />
      <Card title="Open a legal entity">
        <form className={styles.form} onSubmit={open} noValidate>
          <FormField id="policy-entity" label="Legal entity id" error={error}>
            {(control) => <Input {...control} value={entity} onChange={(e) => setEntity(e.target.value)} />}
          </FormField>
          <div className={styles.actions}>
            <Button type="submit" variant="primary">
              Open policy
            </Button>
          </div>
        </form>
      </Card>
      {policies.error ? <LoadError what="purchasing policies" error={policies.error} onRetry={policies.reload} /> : null}
      <DataTable
        label="Purchasing policies"
        columns={columns}
        rows={policies.error ? [] : (policies.data ?? [])}
        getRowKey={(p) => p.legalEntityId}
        loading={policies.loading && !policies.data}
        empty={{ title: "No policies saved yet", description: "Every legal entity uses the defaults until you save a policy for it." }}
        onRowOpen={(p) => navigate(`/purchasing/policies/${p.legalEntityId}`)}
      />
    </div>
  );
}

const flags: { name: "changeManagementDefault" | "poFourEyes" | "allowInvoiceBeforeReceipt"; label: string; hint: string }[] = [
  { name: "changeManagementDefault", label: "Change management on new purchase orders", hint: "A confirmed order that changes beyond the reapproval rules goes back for approval as the next version." },
  { name: "poFourEyes", label: "Four-eyes on purchase orders", hint: "The user who created or submitted an order cannot approve or confirm it." },
  { name: "allowInvoiceBeforeReceipt", label: "Allow invoicing before receipt", hint: "A line quantity may drop below what is invoiced, never below what is received." },
];

/** One legal entity's policy: read it, change the three settings and save under its version. */
export function PolicyDetailPage() {
  const { legalEntityId = "" } = useParams();
  const policy = useResource(async (signal) => (await getPolicy(legalEntityId, signal)).body, [legalEntityId]);
  const [draft, setDraft] = useState<PurchasingPolicyInput | null>(null);
  const [saved, setSaved] = useState<PurchasingPolicy | null>(null);
  const [failure, setFailure] = useState<Failure | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (policy.data) {
      setSaved(policy.data);
      setDraft({
        changeManagementDefault: policy.data.changeManagementDefault,
        poFourEyes: policy.data.poFourEyes,
        allowInvoiceBeforeReceipt: policy.data.allowInvoiceBeforeReceipt,
        categoryAccessMode: policy.data.categoryAccessMode ?? "SOFT",
      });
    }
  }, [policy.data]);

  function reload() {
    setFailure(null);
    setNotice(null);
    policy.reload();
  }

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    if (!draft || !saved) return;
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      const { body } = await savePolicy(legalEntityId, draft, saved.version);
      setSaved(body);
      setNotice(`Policy saved as version ${body.version}.`);
    } catch (err) {
      setFailure(describeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Purchasing policy"
        description="Applies to every purchase order of this legal entity."
        actions={
          <Link className={buttonClassName("secondary")} to="/purchasing/policies">
            All policies
          </Link>
        }
      />
      {policy.error ? <LoadError what="the purchasing policy" error={policy.error} onRetry={policy.reload} /> : null}
      {policy.loading && !saved ? <Skeleton /> : null}
      {saved && draft ? (
        <form className={styles.form} onSubmit={onSubmit} noValidate>
          <Card>
            <Details
              items={[
                { label: "Legal entity", value: legalEntityId, mono: true },
                { label: "Saved version", value: saved.version === 0 ? "Not saved yet (defaults)" : String(saved.version) },
              ]}
            />
          </Card>
          <Card title="Settings">
            <div className={styles.stack}>
              {flags.map((f) => (
                <div key={f.name}>
                  <div className={styles.checkboxRow}>
                    <Input
                      id={`policy-${f.name}`}
                      type="checkbox"
                      checked={draft[f.name]}
                      aria-describedby={`policy-${f.name}-hint`}
                      onChange={(e) => setDraft({ ...draft, [f.name]: e.target.checked })}
                    />
                    <label htmlFor={`policy-${f.name}`}>{f.label}</label>
                  </div>
                  <p id={`policy-${f.name}-hint`} className={styles.muted}>
                    {f.hint}
                  </p>
                </div>
              ))}
            </div>
            <div className={styles.grid}>
              <FormField id="policy-categoryAccessMode" label="Category access" hint={accessModes[draft.categoryAccessMode].hint}>
                {(control) => (
                  <Select
                    {...control}
                    value={draft.categoryAccessMode}
                    onChange={(e) => setDraft({ ...draft, categoryAccessMode: e.target.value as CategoryAccessMode })}
                  >
                    {(Object.keys(accessModes) as CategoryAccessMode[]).map((m) => (
                      <option key={m} value={m}>
                        {accessModes[m].label}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </div>
          </Card>
          <Alert
            tone="danger"
            live="alert"
            action={
              failure?.code === "PRC-POL-003" ? (
                <Button type="button" variant="secondary" onClick={reload}>
                  Reload policy
                </Button>
              ) : undefined
            }
          >
            {failure
              ? failure.code === "PRC-POL-003"
                ? <ProblemMessage problem={failure} message="Someone else saved this policy since you opened it. Reload to see their settings, then save again." />
                : <ProblemMessage problem={failure} />
              : null}
          </Alert>
          <Alert tone="success" live="status">
            {notice}
          </Alert>
          <div className={styles.actions}>
            <Button type="submit" variant="primary" loading={busy}>
              {saved.version === 0 ? "Save policy" : "Update policy"}
            </Button>
          </div>
        </form>
      ) : null}
    </div>
  );
}
