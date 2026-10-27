import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  DataTable,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  StatusPill,
  type DataTableColumn,
} from "../../shell/ui";
import {
  fetchLedger,
  fetchStructure,
  LedgerApiError,
  MAIN_ACCOUNT,
  putStructure,
  type Structure,
  type StructureInput,
  type Violation,
} from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import styles from "./pages.module.css";

const NAME = /^[A-Za-z0-9_.-]{1,60}$/;
const VALUE = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const MAX_SEGMENTS = 20;

/** One editable constraint row; `under` and `allowed` are typed as comma lists. */
export interface RuleDraft {
  key: number;
  segment: string;
  under: string;
  allowed: string;
}

export interface StructureDraft {
  name: string;
  version: string;
  priority: string;
  effectiveFrom: string;
  segments: string;
  accountFilter: string;
  rules: RuleDraft[];
}

type Errors = Record<string, string>;

export const emptyStructure: StructureDraft = {
  name: "",
  version: "1",
  priority: "0",
  effectiveFrom: "",
  segments: "",
  accountFilter: "",
  rules: [],
};

/** Splits a comma list, trimming blanks away. */
export function splitList(text: string): string[] {
  return text
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

/** A filter or constraint token: exact value, from..to range, `*` or `blank` (the service has the last word). */
export function validToken(token: string): boolean {
  if (token === "*" || token === "blank") return true;
  const range = token.split("..");
  if (range.length === 2) return VALUE.test(range[0]) && VALUE.test(range[1]) && range[0] <= range[1];
  return VALUE.test(token);
}

/** Parses `MAIN_ACCOUNT=6000..6999, DEPT=D100` into conditions; null when a part is not `NAME=token`. */
export function parseUnder(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const part of splitList(text)) {
    const eq = part.indexOf("=");
    if (eq <= 0) return null;
    const key = part.slice(0, eq).trim();
    const value = part.slice(eq + 1).trim();
    if (key === "" || value === "" || key in out) return null;
    out[key] = value;
  }
  return out;
}

export function formatUnder(under: Record<string, string> | undefined): string {
  return Object.entries(under ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join(", ");
}

/** Client checks that mirror PUT .../account-structures/{id} (FR-11.3). `attributes` are the ledger's, when known. */
export function validateStructure(d: StructureDraft, attributes?: string[]): Errors {
  const errors: Errors = {};
  if (!NAME.test(d.name.trim())) errors.name = "Use 1 to 60 letters, digits, dots, dashes or underscores.";
  if (!/^\d+$/.test(d.version) || Number(d.version) < 1) errors.version = "Enter a whole number from 1.";
  if (!/^\d+$/.test(d.priority)) errors.priority = "Enter a whole number from 0.";
  if (!DATE.test(d.effectiveFrom)) errors.effectiveFrom = "Enter the date the version applies from.";
  const segments = splitList(d.segments);
  if (segments.length > MAX_SEGMENTS) errors.segments = `Use at most ${MAX_SEGMENTS} segments.`;
  else if (new Set(segments).size !== segments.length) errors.segments = "List each dimension once.";
  else if (segments.includes(MAIN_ACCOUNT)) errors.segments = "The main account is always the first segment; don't list it.";
  else if (attributes) {
    const unknown = segments.filter((s) => !attributes.includes(s));
    if (unknown.length > 0) errors.segments = `Not a dimension of this ledger: ${unknown.join(", ")}.`;
  }
  const filter = splitList(d.accountFilter);
  if (filter.some((t) => t === "blank" || !validToken(t))) errors.accountFilter = "Use account numbers, ranges like 6000..6999, or *.";
  d.rules.forEach((rule, i) => {
    const at = segments.indexOf(rule.segment);
    if (at < 0) {
      errors[`rules.${i}`] = `Rule ${i + 1}: pick one of the structure's segments.`;
      return;
    }
    const under = parseUnder(rule.under);
    if (under === null) {
      errors[`rules.${i}`] = `Rule ${i + 1}: write conditions as NAME=value, separated by commas.`;
      return;
    }
    for (const [key, token] of Object.entries(under)) {
      const earlier = key === MAIN_ACCOUNT || (segments.indexOf(key) >= 0 && segments.indexOf(key) < at);
      if (!earlier || !validToken(token)) {
        errors[`rules.${i}`] = `Rule ${i + 1}: conditions may only use ${MAIN_ACCOUNT} or an earlier segment, with a value, range, blank or *.`;
        return;
      }
    }
    const allowed = splitList(rule.allowed);
    if (allowed.length === 0 || allowed.some((t) => !validToken(t))) {
      errors[`rules.${i}`] = `Rule ${i + 1}: list the allowed values, ranges, * (any value) or blank.`;
    }
  });
  return errors;
}

export function toStructureInput(d: StructureDraft, status: Structure["status"]): StructureInput {
  return {
    name: d.name.trim(),
    version: Number(d.version),
    priority: Number(d.priority),
    effectiveFrom: d.effectiveFrom,
    status,
    segments: splitList(d.segments),
    accountFilter: splitList(d.accountFilter),
    constraints: d.rules.map((r) => {
      const under = parseUnder(r.under) ?? {};
      return Object.keys(under).length > 0
        ? { segment: r.segment, under, allowed: splitList(r.allowed) }
        : { segment: r.segment, allowed: splitList(r.allowed) };
    }),
  };
}

export function toStructureDraft(s: Structure): StructureDraft {
  return {
    name: s.name,
    version: String(s.version),
    priority: String(s.priority),
    effectiveFrom: s.effectiveFrom,
    segments: s.segments.join(", "),
    accountFilter: s.accountFilter.join(", "),
    rules: s.constraints.map((c, i) => ({ key: i, segment: c.segment, under: formatUnder(c.under), allowed: c.allowed.join(", ") })),
  };
}

function failureText(error: unknown): { message: string; violations: Violation[] } {
  if (error instanceof LedgerApiError) return { message: `${error.message} (${error.code})`, violations: error.violations };
  return { message: (error as Error).message, violations: [] };
}

interface FormProps {
  ledgerId: string;
  structureId: string;
  initial: StructureDraft;
  status: Structure["status"] | "new";
}

function StructureForm({ ledgerId, structureId, initial, status }: FormProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const ledger = useQuery({ queryKey: ["ledger", "ledger", ledgerId], queryFn: () => fetchLedger(ledgerId) });
  const [values, setValues] = useState<StructureDraft>(initial);
  const [nextKey, setNextKey] = useState(initial.rules.length);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<{ message: string; violations: Violation[] } | null>(null);
  const [busy, setBusy] = useState<Structure["status"] | null>(null);
  const locked = status === "active" || status === "retired";
  const query = ledgerQuery(ledgerId);
  const segments = splitList(values.segments);

  const update = (patch: Partial<StructureDraft>) => setValues((prev) => ({ ...prev, ...patch }));
  const updateRule = (key: number, patch: Partial<RuleDraft>) =>
    setValues((prev) => ({ ...prev, rules: prev.rules.map((r) => (r.key === key ? { ...r, ...patch } : r)) }));
  const addRule = () => {
    setValues((prev) => ({ ...prev, rules: [...prev.rules, { key: nextKey, segment: segments[0] ?? "", under: "", allowed: "*" }] }));
    setNextKey((k) => k + 1);
  };
  const removeRule = (key: number) => setValues((prev) => ({ ...prev, rules: prev.rules.filter((r) => r.key !== key) }));

  async function save(target: Structure["status"]) {
    const found = locked ? {} : validateStructure(values, ledger.data?.dimensionAttributes);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(target);
    try {
      await putStructure(ledgerId, structureId, toStructureInput(values, target));
      await queryClient.invalidateQueries({ queryKey: ["ledger", "structures"] });
      navigate(`/ledger/structures${query}`);
    } catch (error) {
      setFailure(failureText(error));
    } finally {
      setBusy(null);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void save("draft");
  }

  const ruleColumns: DataTableColumn<RuleDraft>[] = [
    {
      key: "segment",
      header: "Segment",
      render: (r) => (
        <Select aria-label={`Rule ${values.rules.indexOf(r) + 1} segment`} value={r.segment} disabled={locked} onChange={(e) => updateRule(r.key, { segment: e.target.value })}>
          {r.segment !== "" && !segments.includes(r.segment) ? <option value={r.segment}>{r.segment}</option> : null}
          {segments.length === 0 ? <option value="">No segments</option> : null}
          {segments.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </Select>
      ),
    },
    {
      key: "under",
      header: "When",
      render: (r) => (
        <Input
          aria-label={`Rule ${values.rules.indexOf(r) + 1} conditions`}
          className={styles.mono}
          placeholder="Always"
          value={r.under}
          readOnly={locked}
          onChange={(e) => updateRule(r.key, { under: e.target.value })}
        />
      ),
    },
    {
      key: "allowed",
      header: "Allowed",
      render: (r) => (
        <Input
          aria-label={`Rule ${values.rules.indexOf(r) + 1} allowed values`}
          className={styles.mono}
          value={r.allowed}
          readOnly={locked}
          onChange={(e) => updateRule(r.key, { allowed: e.target.value })}
        />
      ),
    },
  ];
  if (!locked) {
    ruleColumns.push({
      key: "remove",
      header: "Remove",
      render: (r) => (
        <Button size="sm" variant="ghost" onClick={() => removeRule(r.key)}>
          Remove rule {values.rules.indexOf(r) + 1}
        </Button>
      ),
    });
  }
  const ruleErrors = Object.entries(errors).filter(([k]) => k.startsWith("rules."));
  const attributes = ledger.data?.dimensionAttributes;

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={status === "new" ? "New account structure" : `Account structure ${initial.name}`}>
        {status !== "new" ? (
          <p className={`${styles.note} ${styles.wide}`}>
            Status: <StatusPill status={status} />
            {locked ? " This version is locked. Save a new version to change it." : null}
          </p>
        ) : null}
        <FormField id="st-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} className={styles.mono} value={values.name} readOnly={locked} maxLength={60} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="st-version" label="Version" error={errors.version}>
          {(c) => <Input {...c} inputMode="numeric" value={values.version} readOnly={locked} onChange={(e) => update({ version: e.target.value })} invalid={Boolean(errors.version)} />}
        </FormField>
        <FormField id="st-from" label="Effective from" error={errors.effectiveFrom}>
          {(c) => <Input {...c} type="date" value={values.effectiveFrom} readOnly={locked} onChange={(e) => update({ effectiveFrom: e.target.value })} invalid={Boolean(errors.effectiveFrom)} />}
        </FormField>
        <FormField id="st-priority" label="Priority" hint="Lower numbers are tried first." error={errors.priority}>
          {(c) => <Input {...c} inputMode="numeric" value={values.priority} readOnly={locked} onChange={(e) => update({ priority: e.target.value })} invalid={Boolean(errors.priority)} />}
        </FormField>
        <FormField
          id="st-segments"
          label="Segments"
          optional
          hint={attributes ? `In order, after the main account. This ledger's dimensions: ${attributes.join(", ") || "none"}.` : "In order, after the main account, separated by commas."}
          error={errors.segments}
        >
          {(c) => <Input {...c} className={styles.mono} placeholder="DEPT, CC" value={values.segments} readOnly={locked} onChange={(e) => update({ segments: e.target.value })} invalid={Boolean(errors.segments)} />}
        </FormField>
        <FormField id="st-filter" label="Main accounts" optional hint="Numbers, ranges like 6000..6999, or *. Empty covers every account." error={errors.accountFilter}>
          {(c) => <Input {...c} className={styles.mono} placeholder="6000..6999" value={values.accountFilter} readOnly={locked} onChange={(e) => update({ accountFilter: e.target.value })} invalid={Boolean(errors.accountFilter)} />}
        </FormField>
        <div className={`${styles.stack} ${styles.wide}`}>
          <DataTable
            label="Constraint rules"
            columns={ruleColumns}
            rows={values.rules}
            getRowKey={(r) => String(r.key)}
            empty={{ title: "No rules", description: "Every segment is optional and takes any value." }}
          />
          <p className={styles.note}>
            Conditions use {MAIN_ACCOUNT} or an earlier segment, such as {MAIN_ACCOUNT}=6010 or DEPT=D100; leave empty to always apply. Allowed takes values, ranges, *
            (any value) and blank (may stay empty). The most specific matching rule decides.
          </p>
          {ruleErrors.length > 0 ? (
            <ul className={styles.problems} role="alert">
              {ruleErrors.map(([k, message]) => (
                <li key={k}>{message}</li>
              ))}
            </ul>
          ) : null}
          {!locked ? (
            <div className={styles.formActions}>
              <Button size="sm" onClick={addRule} disabled={segments.length === 0}>
                Add rule
              </Button>
            </div>
          ) : null}
        </div>
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the account structure" live="alert">
              {failure.message}
              {failure.violations.length > 0 ? (
                <ul className={styles.problems}>
                  {failure.violations.map((v, i) => (
                    <li key={i}>
                      {v.field ? <span className={styles.mono}>{v.field}</span> : null} {v.message}
                    </li>
                  ))}
                </ul>
              ) : null}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          {!locked ? (
            <>
              <Button type="submit" variant="primary" loading={busy === "draft"}>
                Save draft
              </Button>
              <Button onClick={() => void save("active")} loading={busy === "active"}>
                Save and activate
              </Button>
            </>
          ) : null}
          {status === "active" ? (
            <Button variant="danger" onClick={() => void save("retired")} loading={busy === "retired"}>
              Retire version
            </Button>
          ) : null}
          {locked ? (
            <Link className={buttonClassName("primary")} to={`/ledger/structures/new${query}${query ? "&" : "?"}from=${encodeURIComponent(structureId)}`}>
              New version
            </Link>
          ) : null}
          <Link className={buttonClassName("ghost")} to={`/ledger/structures${query}`}>
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

function Loading({ label }: { label: string }) {
  return (
    <Card>
      <div aria-busy="true">
        <span className="visually-hidden">{label}</span>
        <Skeleton height="control" />
        <Skeleton height="control" width="1/2" />
      </div>
    </Card>
  );
}

function NeedsLedger() {
  return (
    <Alert tone="warning" title="No ledger chosen">
      Open this page from <Link to="/ledger/structures">Account structures</Link> after choosing a ledger.
    </Alert>
  );
}

/** Create a structure version; `?from=<id>` starts from that version with the next version number. */
export function NewStructurePage() {
  const [ledgerId] = useLedgerParam();
  const [params] = useSearchParams();
  const from = params.get("from") ?? "";
  const [id] = useState(() => crypto.randomUUID());
  const source = useQuery({
    queryKey: ["ledger", "structures", ledgerId, from],
    queryFn: () => fetchStructure(ledgerId, from),
    enabled: ledgerId !== "" && from !== "",
  });

  let body;
  if (ledgerId === "") body = <NeedsLedger />;
  else if (from === "") body = <StructureForm ledgerId={ledgerId} structureId={id} initial={emptyStructure} status="new" />;
  else if (source.isError)
    body = (
      <Alert tone="danger" title="Could not load the version to copy" live="alert" action={<Button size="sm" onClick={() => void source.refetch()}>Try again</Button>}>
        {(source.error as Error).message}
      </Alert>
    );
  else if (source.isPending) body = <Loading label="Loading the version to copy…" />;
  else
    body = (
      <StructureForm
        ledgerId={ledgerId}
        structureId={id}
        initial={{ ...toStructureDraft(source.data), version: String(source.data.version + 1), effectiveFrom: "" }}
        status="new"
      />
    );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="New account structure"
        description="Save it as a draft to keep working on it, or activate it so postings from its effective date are checked against it."
      />
      {body}
    </div>
  );
}

/** View or edit one structure version: drafts are editable, active versions can be retired. */
export function EditStructurePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const result = useQuery({
    queryKey: ["ledger", "structures", ledgerId, id],
    queryFn: () => fetchStructure(ledgerId, id),
    enabled: ledgerId !== "",
  });

  return (
    <div className={styles.stack}>
      <PageHeader
        title={result.data ? `Account structure ${result.data.name}, version ${result.data.version}` : "Account structure"}
        description="Segments, the accounts the structure covers, and the rules for each segment."
      />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : result.isError ? (
        <Alert tone="danger" title="Could not load the account structure" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Loading label="Loading the account structure…" />
      ) : (
        <StructureForm key={`${result.data.id}-${result.data.status}`} ledgerId={ledgerId} structureId={id} initial={toStructureDraft(result.data)} status={result.data.status} />
      )}
    </div>
  );
}
