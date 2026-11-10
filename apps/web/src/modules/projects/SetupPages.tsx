import { useState, type FormEvent } from "react";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  LINE_PROPERTIES,
  listGroups,
  listProfiles,
  PROJECT_TYPES,
  resolveProfile,
  saveGroup,
  saveProfile,
  WILDCARD,
  type LineProperty,
  type PostingProfile,
  type PostingProfileInput,
  type Problem,
  type ProjectGroup,
  type ProjectType,
} from "./api/client";
import { dash, linePropertyLabel, typeLabel } from "./labels";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const GROUP_ID = /^[A-Z0-9][A-Z0-9-]{0,9}$/;

function LoadFailure({ title, problem, retry }: { title: string; problem: Problem; retry(): void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={retry}>Try again</Button>}>
      <ProblemMessage problem={problem} />
    </Alert>
  );
}

/** Explains a failed save; a stale version asks the user to reload first. */
function SaveFailure({ problem, what }: { problem: Problem; what: string }) {
  const stale = problem.code === "conflict_version";
  return (
    <Alert tone="danger" title={stale ? `Someone else changed this ${what}` : `Could not save the ${what}`} live="alert">
      {stale ? "The list now shows the latest version. Open the row again and repeat your change." : <ProblemMessage problem={problem} />}
    </Alert>
  );
}

interface GroupValues {
  id: string;
  name: string;
  defaultProjectType: ProjectType | "";
}

const emptyGroup: GroupValues = { id: "", name: "", defaultProjectType: "" };

const groupColumns: DataTableColumn<ProjectGroup>[] = [
  { key: "id", header: "Group", mono: true },
  { key: "name", header: "Name" },
  { key: "defaultProjectType", header: "Default project type", render: (g) => (g.defaultProjectType ? typeLabel[g.defaultProjectType] : dash) },
  { key: "version", header: "Version", align: "end" },
];

/** Project groups (FR-41.1): every project belongs to one; posting profiles key on it. Saving needs projects.setup.admin. */
export function ProjectGroupsPage() {
  const state = useLoad((signal) => listGroups(signal), "groups");
  const [editing, setEditing] = useState<ProjectGroup | null>(null);
  const [values, setValues] = useState<GroupValues>(emptyGroup);
  const [errors, setErrors] = useState<Partial<Record<keyof GroupValues, string>>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function open(group: ProjectGroup) {
    setEditing(group);
    setValues({ id: group.id, name: group.name, defaultProjectType: group.defaultProjectType ?? "" });
    setErrors({});
    setFailure(null);
    setSaved(null);
  }

  function reset() {
    setEditing(null);
    setValues(emptyGroup);
    setErrors({});
    setFailure(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: typeof errors = {};
    const id = values.id.trim();
    const name = values.name.trim();
    if (!GROUP_ID.test(id)) found.id = "Use 1 to 10 capital letters, digits or hyphens, for example ENG.";
    if (name === "" || name.length > 120) found.name = "Enter a name of up to 120 characters.";
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const out = await saveGroup(id, { name, ...(values.defaultProjectType ? { defaultProjectType: values.defaultProjectType } : {}) }, editing?.version);
      setSaved(`Project group ${out.id} is saved.`);
      reset();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as typeof errors);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Project groups"
        description="Every project belongs to a group. Posting profiles use the group to find the ledger accounts for project costs and revenue."
      />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      <Card title={editing ? `Edit group ${editing.id}` : "New project group"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit project group" : "New project group"}>
          <FormField id="group-id" label="Group id" hint="Up to 10 capital letters, digits or hyphens." error={errors.id}>
            {(control) => (
              <Input
                {...control}
                className={styles.mono}
                value={values.id}
                readOnly={editing !== null}
                onChange={(e) => setValues((v) => ({ ...v, id: e.target.value.toUpperCase() }))}
                invalid={Boolean(errors.id)}
              />
            )}
          </FormField>
          <FormField id="group-name" label="Name" error={errors.name}>
            {(control) => (
              <Input {...control} value={values.name} onChange={(e) => setValues((v) => ({ ...v, name: e.target.value }))} invalid={Boolean(errors.name)} />
            )}
          </FormField>
          <FormField id="group-type" label="Default project type" optional error={errors.defaultProjectType}>
            {(control) => (
              <Select {...control} value={values.defaultProjectType} onChange={(e) => setValues((v) => ({ ...v, defaultProjectType: e.target.value as ProjectType | "" }))}>
                <option value="">None</option>
                {PROJECT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {typeLabel[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <SaveFailure problem={failure} what="project group" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {editing ? "Save changes" : "Add group"}
            </Button>
            {editing ? (
              <Button type="button" variant="ghost" onClick={reset}>
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <LoadFailure title="Could not load project groups" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Project groups"
          columns={groupColumns}
          rows={state.data?.items ?? []}
          getRowKey={(g) => g.id}
          loading={state.loading}
          empty={{ title: "No project groups yet", description: "Add a group before you create projects; every project names one." }}
          onRowOpen={open}
        />
      )}
    </div>
  );
}

type ProfileLevel = "exact" | "group" | "all";

interface ProfileValues {
  level: ProfileLevel;
  projectGroup: string;
  category: string;
  lineProperty: LineProperty;
  costAccount: string;
  wipAccount: string;
  accruedRevenueAccount: string;
  invoicedRevenueAccount: string;
  onAccountAccount: string;
}

type ProfileErrors = Partial<Record<keyof ProfileValues, string>>;

const emptyProfile: ProfileValues = {
  level: "exact",
  projectGroup: "",
  category: "",
  lineProperty: "CHARGEABLE",
  costAccount: "",
  wipAccount: "",
  accruedRevenueAccount: "",
  invoicedRevenueAccount: "",
  onAccountAccount: "",
};

const levelLabel: Record<ProfileLevel, string> = {
  exact: "One group, category and line property",
  group: "Fallback for a whole group",
  all: "Catch-all for every group",
};

const accounts: { name: keyof PostingProfileInput & keyof ProfileValues; label: string }[] = [
  { name: "costAccount", label: "Cost account" },
  { name: "wipAccount", label: "WIP account" },
  { name: "accruedRevenueAccount", label: "Accrued revenue account" },
  { name: "invoicedRevenueAccount", label: "Invoiced revenue account" },
  { name: "onAccountAccount", label: "On-account account" },
];

function levelOf(p: PostingProfile): ProfileLevel {
  if (p.projectGroup === WILDCARD) return "all";
  return p.category === WILDCARD ? "group" : "exact";
}

/**
 * Builds the profile the service stores: only the three keys its resolver reads
 * (FR-41.5), so the level decides which key fields are wildcards.
 */
export function profileInput(values: ProfileValues): { errors: ProfileErrors; input?: PostingProfileInput } {
  const errors: ProfileErrors = {};
  const group = values.projectGroup.trim();
  const category = values.category.trim();
  if (values.level !== "all" && !GROUP_ID.test(group)) errors.projectGroup = "Enter a project group id, for example ENG.";
  if (values.level === "exact" && (category === "" || category === WILDCARD || category.length > 20)) {
    errors.category = "Enter a category of up to 20 characters.";
  }
  for (const a of accounts) {
    const v = values[a.name].trim();
    if (v === "" || v.length > 40) errors[a.name] = "Enter an account of up to 40 characters.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  const exact = values.level === "exact";
  return {
    errors,
    input: {
      projectGroup: values.level === "all" ? WILDCARD : group,
      category: exact ? category : WILDCARD,
      lineProperty: exact ? values.lineProperty : WILDCARD,
      costAccount: values.costAccount.trim(),
      wipAccount: values.wipAccount.trim(),
      accruedRevenueAccount: values.accruedRevenueAccount.trim(),
      invoicedRevenueAccount: values.invoicedRevenueAccount.trim(),
      onAccountAccount: values.onAccountAccount.trim(),
    },
  };
}

const profileColumns: DataTableColumn<PostingProfile>[] = [
  { key: "projectGroup", header: "Group", mono: true, render: (p) => (p.projectGroup === WILDCARD ? "Any" : p.projectGroup) },
  { key: "category", header: "Category", mono: true, render: (p) => (p.category === WILDCARD ? "Any" : p.category) },
  { key: "lineProperty", header: "Line property", render: (p) => linePropertyLabel[p.lineProperty] ?? p.lineProperty },
  { key: "costAccount", header: "Cost", mono: true },
  { key: "wipAccount", header: "WIP", mono: true },
  { key: "accruedRevenueAccount", header: "Accrued revenue", mono: true },
  { key: "invoicedRevenueAccount", header: "Invoiced revenue", mono: true },
  { key: "onAccountAccount", header: "On account", mono: true },
];

const profileKey = (p: { projectGroup: string; category: string; lineProperty: string }) => `${p.projectGroup}|${p.category}|${p.lineProperty}`;

/**
 * Posting profiles (FR-41.5): ledger accounts per (group, category, line
 * property), with a group fallback and a catch-all. Includes a lookup that
 * shows which row a posting would use.
 */
export function PostingProfilesPage() {
  const state = useLoad((signal) => listProfiles(signal), "profiles");
  const [editing, setEditing] = useState<PostingProfile | null>(null);
  const [values, setValues] = useState<ProfileValues>(emptyProfile);
  const [errors, setErrors] = useState<ProfileErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function open(p: PostingProfile) {
    setEditing(p);
    setValues({
      level: levelOf(p),
      projectGroup: p.projectGroup === WILDCARD ? "" : p.projectGroup,
      category: p.category === WILDCARD ? "" : p.category,
      lineProperty: p.lineProperty === WILDCARD ? "CHARGEABLE" : p.lineProperty,
      costAccount: p.costAccount,
      wipAccount: p.wipAccount,
      accruedRevenueAccount: p.accruedRevenueAccount,
      invoicedRevenueAccount: p.invoicedRevenueAccount,
      onAccountAccount: p.onAccountAccount,
    });
    setErrors({});
    setFailure(null);
    setSaved(null);
  }

  function reset() {
    setEditing(null);
    setValues(emptyProfile);
    setErrors({});
    setFailure(null);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = profileInput(values);
    setErrors(found);
    setFailure(null);
    setSaved(null);
    if (!input) return;
    // Replacing needs the version of the row with this key, also when the user retyped an existing key.
    const existing = state.data?.items.find((p) => profileKey(p) === profileKey(input));
    setBusy(true);
    try {
      await saveProfile(input, existing?.version);
      setSaved("The posting profile is saved.");
      reset();
      state.reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as ProfileErrors);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(false);
    }
  }

  const set = (patch: Partial<ProfileValues>) => setValues((v) => ({ ...v, ...patch }));
  const keyLocked = editing !== null;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Posting profiles"
        description="Ledger accounts for project postings. A posting uses the row for its group, category and line property, then the group fallback, then the catch-all."
      />
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      <Card title={editing ? "Edit posting profile" : "New posting profile"}>
        <form className={styles.form} onSubmit={submit} noValidate aria-label={editing ? "Edit posting profile" : "New posting profile"}>
          <FormField id="profile-level" label="Applies to">
            {(control) => (
              <Select {...control} value={values.level} disabled={keyLocked} onChange={(e) => set({ level: e.target.value as ProfileLevel })}>
                {(Object.keys(levelLabel) as ProfileLevel[]).map((l) => (
                  <option key={l} value={l}>
                    {levelLabel[l]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {values.level !== "all" ? (
            <FormField id="profile-group" label="Project group" error={errors.projectGroup}>
              {(control) => (
                <Input
                  {...control}
                  className={styles.mono}
                  value={values.projectGroup}
                  readOnly={keyLocked}
                  onChange={(e) => set({ projectGroup: e.target.value.toUpperCase() })}
                  invalid={Boolean(errors.projectGroup)}
                />
              )}
            </FormField>
          ) : null}
          {values.level === "exact" ? (
            <>
              <FormField id="profile-category" label="Category" hint="Project category code, for example LABOR." error={errors.category}>
                {(control) => (
                  <Input
                    {...control}
                    className={styles.mono}
                    value={values.category}
                    readOnly={keyLocked}
                    onChange={(e) => set({ category: e.target.value.toUpperCase() })}
                    invalid={Boolean(errors.category)}
                  />
                )}
              </FormField>
              <FormField id="profile-property" label="Line property" error={errors.lineProperty}>
                {(control) => (
                  <Select {...control} value={values.lineProperty} disabled={keyLocked} onChange={(e) => set({ lineProperty: e.target.value as LineProperty })}>
                    {LINE_PROPERTIES.map((p) => (
                      <option key={p} value={p}>
                        {linePropertyLabel[p]}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </>
          ) : null}
          {accounts.map((a) => (
            <FormField key={a.name} id={`profile-${a.name}`} label={a.label} error={errors[a.name]}>
              {(control) => (
                <Input {...control} className={styles.mono} value={values[a.name]} onChange={(e) => set({ [a.name]: e.target.value })} invalid={Boolean(errors[a.name])} />
              )}
            </FormField>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <SaveFailure problem={failure} what="posting profile" />
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {editing ? "Save changes" : "Add profile"}
            </Button>
            {editing ? (
              <Button type="button" variant="ghost" onClick={reset}>
                Cancel
              </Button>
            ) : null}
          </div>
        </form>
      </Card>
      {state.error ? (
        <LoadFailure title="Could not load posting profiles" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Posting profiles"
          columns={profileColumns}
          rows={state.data?.items ?? []}
          getRowKey={profileKey}
          loading={state.loading}
          empty={{ title: "No posting profiles yet", description: "Add a catch-all row first so that every project posting finds its accounts." }}
          onRowOpen={open}
        />
      )}
      <ProfileLookup />
    </div>
  );
}

/** "Which accounts would this posting use?" over POST /posting-profiles/resolve. */
function ProfileLookup() {
  const [group, setGroup] = useState("");
  const [category, setCategory] = useState("");
  const [property, setProperty] = useState<LineProperty>("CHARGEABLE");
  const [result, setResult] = useState<PostingProfile | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setResult(null);
    setFailure(null);
    setBusy(true);
    try {
      setResult(await resolveProfile({ projectGroup: group.trim(), category: category.trim(), lineProperty: property }));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  const matched = result
    ? result.projectGroup === WILDCARD
      ? "the catch-all row"
      : result.category === WILDCARD
        ? `the fallback for group ${result.projectGroup}`
        : "the exact row"
    : "";
  return (
    <Card title="Look up accounts">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Look up accounts">
        <FormField id="lookup-group" label="Project group" error={failure?.fields.projectGroup}>
          {(control) => <Input {...control} className={styles.mono} value={group} onChange={(e) => setGroup(e.target.value.toUpperCase())} />}
        </FormField>
        <FormField id="lookup-category" label="Category" error={failure?.fields.category}>
          {(control) => <Input {...control} className={styles.mono} value={category} onChange={(e) => setCategory(e.target.value.toUpperCase())} />}
        </FormField>
        <FormField id="lookup-property" label="Line property">
          {(control) => (
            <Select {...control} value={property} onChange={(e) => setProperty(e.target.value as LineProperty)}>
              {LINE_PROPERTIES.map((p) => (
                <option key={p} value={p}>
                  {linePropertyLabel[p]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Look up
          </Button>
        </div>
        <div className={styles.formActions}>
          <Alert tone="info" live="status">
            {result ? `Uses ${matched}: cost ${result.costAccount}, WIP ${result.wipAccount}, accrued revenue ${result.accruedRevenueAccount}, invoiced revenue ${result.invoicedRevenueAccount}, on account ${result.onAccountAccount}.` : null}
          </Alert>
          {failure ? (
            <Alert tone="danger" title={failure.code === "PROJ-POST-001" ? "No posting profile applies" : "Could not look up the accounts"} live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
        </div>
      </form>
    </Card>
  );
}
