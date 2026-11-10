import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import { OCM_MODES, describeProblem, listOCMProfiles, saveOCMProfile, type OCMMode, type OCMProfile, type OCMProfileInput, type Problem } from "./api/client";
import { ocmModeLabel } from "./labels";
import styles from "./pages.module.css";
import { ProblemMessage } from "../../shell/errors";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const decimalPattern = /^\d{1,12}(\.\d{1,4})?$/;

export const blankOCMProfile = (): OCMProfileInput => ({ itemGroup: "All", siteId: "All", mode: "manual", surchargeFixed: "0", surchargePct: "0" });

/** Validates an OCM profile like the service: item group, site (UUID or All), mode and the surcharge. */
export function validateOCMProfile(p: OCMProfileInput): Record<string, string> {
  const errors: Record<string, string> = {};
  if (p.itemGroup.trim() === "" || p.itemGroup.length > 40) {
    errors.itemGroup = "Enter an item group (up to 40 characters) or All.";
  }
  if (p.siteId !== "All" && !uuidPattern.test(p.siteId)) {
    errors.siteId = "Enter the site id (a UUID) or All.";
  }
  if (!OCM_MODES.includes(p.mode)) {
    errors.mode = "Choose automatic, manual or none.";
  }
  if (!decimalPattern.test(p.surchargeFixed)) {
    errors.surchargeFixed = "Enter an amount of 0 or more, with up to 4 decimals.";
  }
  if (!decimalPattern.test(p.surchargePct) || Number(p.surchargePct) > 1000) {
    errors.surchargePct = "Enter a percentage from 0 to 1000, with up to 4 decimals.";
  }
  return errors;
}

const columns: DataTableColumn<OCMProfile>[] = [
  { key: "itemGroup", header: "Item group" },
  { key: "siteId", header: "Site", mono: true },
  { key: "mode", header: "Mode", render: (row) => ocmModeLabel[row.mode] },
  { key: "surchargeFixed", header: "Fixed surcharge", align: "end" },
  { key: "surchargePct", header: "Surcharge %", align: "end" },
  { key: "version", header: "Version", align: "end" },
];

/**
 * Order change profiles (PRD 4.21.13, FR-21.16): per item group × site, whether a superseded BOM or route is
 * applied automatically, listed for the planner or ignored, and the change-cost surcharge (BR-21.12). With no profile the default is manual.
 */
export function OcmProfilesPage() {
  const [state, setState] = useState<{ rows: OCMProfile[]; loading: boolean; error: Problem | null }>({ rows: [], loading: true, error: null });
  const [form, setForm] = useState<OCMProfileInput>(blankOCMProfile);
  const [editing, setEditing] = useState<OCMProfile | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback((signal?: AbortSignal) => {
    return listOCMProfiles(signal).then(
      (rows) => setState({ rows, loading: false, error: null }),
      (err) => !signal?.aborted && setState({ rows: [], loading: false, error: describeProblem(err) }),
    );
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const edit = (profile: OCMProfile) => {
    setEditing(profile);
    setForm({ itemGroup: profile.itemGroup, siteId: profile.siteId, mode: profile.mode, surchargeFixed: profile.surchargeFixed, surchargePct: profile.surchargePct });
    setErrors({});
    setFailure(null);
    setNotice("");
  };

  const startNew = () => {
    setEditing(null);
    setForm(blankOCMProfile());
    setErrors({});
    setFailure(null);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateOCMProfile(form);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      return;
    }
    setSaving(true);
    setFailure(null);
    setNotice("");
    try {
      const saved = await saveOCMProfile(form, editing?.version);
      setNotice(`Order change profile ${saved.itemGroup} × ${saved.siteId} saved (version ${saved.version}).`);
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

  const text = (key: "itemGroup" | "siteId" | "surchargeFixed" | "surchargePct", label: string, hint?: string) => (
    <FormField key={key} id={`ocm-${key}`} label={label} hint={hint} error={errors[key]}>
      {(control) => (
        <Input
          {...control}
          value={form[key]}
          readOnly={editing !== null && (key === "itemGroup" || key === "siteId")}
          onChange={(e) => setForm({ ...form, [key]: e.target.value })}
        />
      )}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Order change profiles"
        description="Whether a newer BOM or route is applied at the order's next status command, offered to the planner or ignored, by item group and site, and the change-cost surcharge."
        actions={
          <Button variant="secondary" onClick={startNew}>
            New profile
          </Button>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {state.error ? (
        <Alert tone={state.error.status === 403 ? "info" : "danger"} title="Could not load the profiles" live="alert">
          {state.error.status === 403 ? "You need the View production setup duty to see the profiles." : <ProblemMessage problem={state.error} />}
        </Alert>
      ) : (
        <DataTable
          label="Order change profiles"
          columns={columns}
          rows={state.rows}
          loading={state.loading}
          getRowKey={(row) => `${row.itemGroup}/${row.siteId}`}
          onRowOpen={edit}
          empty={{ title: "No profiles yet", description: "Without a profile every superseded version is offered to the planner with no surcharge." }}
        />
      )}
      <Card title={editing ? `Edit ${editing.itemGroup} × ${editing.siteId}` : "New profile"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Order change profile">
          {failure ? (
            <Alert tone="danger" live="alert" title="The profile was not saved">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          <div className={styles.fields}>
            {text("itemGroup", "Item group", "All matches every item group.")}
            {text("siteId", "Site", "A site id, or All.")}
            <FormField id="ocm-mode" label="Mode" error={errors.mode}>
              {(control) => (
                <Select {...control} value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value as OCMMode })}>
                  {OCM_MODES.map((m) => (
                    <option key={m} value={m}>
                      {ocmModeLabel[m]}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {text("surchargeFixed", "Fixed surcharge", "Added to every change cost, even a saving.")}
            {text("surchargePct", "Surcharge %", "Of a positive planned-cost delta.")}
          </div>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={saving}>
              Save profile
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}
