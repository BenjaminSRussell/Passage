import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  listPostingProfiles,
  savePostingProfile,
  type PostingProfile,
  type PostingProfileAccounts,
  type PostingProfileInput,
  type Problem,
} from "./api/client";
import styles from "./pages.module.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The accounts of a profile in form order, with their labels (FR-21.13 posting types). */
export const accountFields: { key: keyof PostingProfileAccounts; label: string }[] = [
  { key: "wipAccount", label: "WIP account" },
  { key: "inventoryAccount", label: "Inventory (picking-list offset)" },
  { key: "finishedGoodsAccount", label: "Finished goods (RAF receipt)" },
  { key: "laborAbsorptionAccount", label: "Labor absorption" },
  { key: "overheadAbsorptionAccount", label: "Overhead absorption" },
  { key: "lotSizeVarianceAccount", label: "Lot-size variance" },
  { key: "quantityVarianceAccount", label: "Quantity variance" },
  { key: "priceVarianceAccount", label: "Price variance" },
  { key: "substitutionVarianceAccount", label: "Substitution variance" },
];

export const blankProfile = (): PostingProfileInput => ({
  itemGroup: "All",
  siteId: "All",
  wipAccount: "",
  inventoryAccount: "",
  finishedGoodsAccount: "",
  laborAbsorptionAccount: "",
  overheadAbsorptionAccount: "",
  lotSizeVarianceAccount: "",
  quantityVarianceAccount: "",
  priceVarianceAccount: "",
  substitutionVarianceAccount: "",
});

/** Validates a profile like the service: item group, site (UUID or All) and every account. */
export function validateProfile(p: PostingProfileInput): Record<string, string> {
  const errors: Record<string, string> = {};
  if (p.itemGroup.trim() === "" || p.itemGroup.length > 40) {
    errors.itemGroup = "Enter an item group (up to 40 characters) or All.";
  }
  if (p.siteId !== "All" && !uuidPattern.test(p.siteId)) {
    errors.siteId = "Enter the site id (a UUID) or All.";
  }
  for (const { key } of accountFields) {
    if (p[key].trim() === "" || p[key].length > 64) {
      errors[key] = "Enter the ledger account (up to 64 characters).";
    }
  }
  return errors;
}

const columns: DataTableColumn<PostingProfile>[] = [
  { key: "itemGroup", header: "Item group" },
  { key: "siteId", header: "Site", mono: true },
  { key: "wipAccount", header: "WIP", mono: true },
  { key: "inventoryAccount", header: "Inventory", mono: true },
  { key: "finishedGoodsAccount", header: "Finished goods", mono: true },
  { key: "laborAbsorptionAccount", header: "Labor absorption", mono: true },
  { key: "overheadAbsorptionAccount", header: "Overhead absorption", mono: true },
  { key: "version", header: "Version", align: "end" },
];

/**
 * Posting profile setup (PRD 4.21.6, FR-21.13): ledger accounts per item group × site, with All as
 * the fallback. Postings resolve item group × site, item group × All, All × site, then All × All.
 */
export function PostingProfilesPage() {
  const [state, setState] = useState<{ rows: PostingProfile[]; loading: boolean; error: Problem | null }>({ rows: [], loading: true, error: null });
  const [form, setForm] = useState<PostingProfileInput>(blankProfile);
  const [editing, setEditing] = useState<PostingProfile | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback((signal?: AbortSignal) => {
    return listPostingProfiles(signal).then(
      (rows) => setState({ rows, loading: false, error: null }),
      (err) => !signal?.aborted && setState({ rows: [], loading: false, error: describeProblem(err) }),
    );
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const edit = (profile: PostingProfile) => {
    setEditing(profile);
    const { itemGroup, siteId } = profile;
    const accounts = Object.fromEntries(accountFields.map(({ key }) => [key, profile[key]])) as unknown as PostingProfileAccounts;
    setForm({ itemGroup, siteId, ...accounts });
    setErrors({});
    setFailure(null);
    setNotice("");
  };

  const startNew = () => {
    setEditing(null);
    setForm(blankProfile());
    setErrors({});
    setFailure(null);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateProfile(form);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      return;
    }
    setSaving(true);
    setFailure(null);
    setNotice("");
    try {
      const saved = await savePostingProfile(form, editing?.version);
      setNotice(`Posting profile ${saved.itemGroup} × ${saved.siteId} saved (version ${saved.version}).`);
      setEditing(saved);
      await load();
    } catch (err) {
      const problem = describeProblem(err);
      setFailure(problem);
      setErrors(problem.errors ?? {});
    } finally {
      setSaving(false);
    }
  };

  const field = (key: keyof PostingProfileInput, label: string, hint?: string) => (
    <FormField key={key} id={`profile-${key}`} label={label} hint={hint} error={errors[key]}>
      {(control) => (
        <Input
          {...control}
          value={form[key] ?? ""}
          readOnly={editing !== null && (key === "itemGroup" || key === "siteId")}
          onChange={(e) => setForm({ ...form, [key]: e.target.value })}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Posting profiles"
        description="Ledger accounts for production postings by item group and site; All matches any."
        actions={
          <Button variant="secondary" onClick={startNew}>
            New posting profile
          </Button>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {state.error ? (
        <Alert tone="danger" title="Could not load posting profiles" live="alert">
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Posting profiles"
          columns={columns}
          rows={state.rows}
          loading={state.loading}
          getRowKey={(row) => `${row.itemGroup}/${row.siteId}`}
          onRowOpen={edit}
          empty={{ title: "No posting profiles yet", description: "Add an All × All profile so every production posting finds its accounts." }}
        />
      )}
      <Card title={editing ? `Edit ${editing.itemGroup} × ${editing.siteId}` : "New posting profile"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Posting profile">
          {failure ? (
            <Alert tone="danger" live="alert" title="The profile was not saved">
              {failure.message} ({failure.code})
            </Alert>
          ) : null}
          <div className={styles.fields}>
            {field("itemGroup", "Item group", "All matches every item group.")}
            {field("siteId", "Site", "A site id, or All.")}
          </div>
          <div className={styles.fields}>{accountFields.map(({ key, label }) => field(key, label))}</div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving}>
              Save posting profile
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
