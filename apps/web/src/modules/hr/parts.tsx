import { useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Textarea,
  type BadgeTone,
  type ButtonVariant,
  type DataTableColumn,
  type InputProps,
} from "../../shell/ui";
import { describeProblem, newKey, type Page, type Problem } from "./api/client";
import styles from "./pages.module.css";
import { useLoad, type LoadState } from "./useLoad";

const KNOWN: Record<string, string> = {
  "HCM-QST-SCOPE":
    "Results of an anonymous questionnaire are shown only to people with access to every legal entity, so a narrower view can't be used to single out a small group.",
  "HCM-QST-NOT-CLOSED": "Results of an anonymous questionnaire are released after it closes. Come back once the schedule has closed.",
  "HCM-QST-001": "This questionnaire has closed, so answers are no longer accepted.",
  "HCM-QST-NOT-OPEN": "This questionnaire is not open for answers right now.",
  "HCM-QST-NOT-OWN": "You can only answer for yourself.",
  "HCM-QST-NOT-INVITED": "You weren't invited to this questionnaire.",
  "HCM-QST-ANSWERED": "You have already answered this questionnaire, and answers can't be changed.",
  "HCM-OFR-001": "This offer has already been converted to a hire.",
  "HCM-INC-SELF-CLOSE": "Whoever reported an incident can't close it. Ask another incident administrator.",
  "HCM-LOAN-001": "Items are still out on loan. Record them returned or lost first.",
};

export function problemText(problem: Problem): string {
  const known = KNOWN[problem.code];
  if (known) return `${known} (${problem.code})`;
  const lead =
    problem.status === 403
      ? "You don't have access to do this. "
      : problem.status === 404
        ? "Not found. "
        : problem.status === 409
          ? "This conflicts with the current state of the record; reload and try again. "
          : "";
  return `${lead}${problem.message} (${problem.code})`;
}

export function ProblemAlert({ problem, title }: { problem: Problem; title: string }) {
  return (
    <Alert tone="danger" title={title} live="alert">
      {problemText(problem)}
    </Alert>
  );
}

const isMissing = (problem: Problem) => problem.status === 404 || /NOT-FOUND$/.test(problem.code);

export function LoadFailure({ error, reload, what, backTo }: { error: Problem; reload(): void; what: string; backTo?: string }) {
  if (isMissing(error)) {
    return (
      <Card>
        <EmptyState
          headingLevel={3}
          title={`We couldn't find this ${what}`}
          description="It may belong to another tenant or legal entity, or the link is wrong."
          action={
            backTo ? (
              <Link className={buttonClassName("secondary")} to={backTo}>
                Go back
              </Link>
            ) : undefined
          }
        />
      </Card>
    );
  }
  if (error.status === 403) {
    return (
      <Alert tone="warning" title={`You can't view this ${what}`} live="alert">
        {problemText(error)}
      </Alert>
    );
  }
  return (
    <Alert
      tone="danger"
      title={`Could not load the ${what}`}
      live="alert"
      action={
        <Button size="sm" onClick={reload}>
          Try again
        </Button>
      }
    >
      {problemText(error)}
    </Alert>
  );
}

export function Loaded<T>({
  state,
  title,
  what,
  backTo,
  children,
}: {
  state: LoadState<T>;
  title: string;
  what: string;
  backTo: string;
  children(data: T): ReactNode;
}) {
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title={title} />
        <LoadFailure error={state.error} reload={state.reload} what={what} backTo={backTo} />
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title={title} />
        <Card>
          <Skeleton width="3/4" />
          <Skeleton width="1/2" />
        </Card>
      </div>
    );
  }
  return <>{children(state.data)}</>;
}

export function BackLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link className={buttonClassName("ghost")} to={to}>
      {children}
    </Link>
  );
}

export function Facts({ children }: { children: ReactNode }) {
  return <dl className={styles.details}>{children}</dl>;
}

export function Entry({ term, children, mono }: { term: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

const SUCCESS = new Set(["APPLIED", "APPROVED", "ACCEPTED", "HIRED", "FILLED", "COMPLETED", "DONE", "RETURNED", "CLOSED", "SUBMITTED"]);
const DANGER = new Set(["FAILED", "REJECTED", "DECLINED", "LOST", "CANCELED"]);
const INFO = new Set(["OPEN", "SENT", "PENDING", "MANAGER_INPUT", "APPROVAL", "UNDER_REVIEW", "OUT", "DRAFT"]);

export function humanize(value: string): string {
  const text = value.replaceAll("_", " ").toLowerCase();
  return text.charAt(0).toUpperCase() + text.slice(1);
}

export function Status({ value }: { value: string }) {
  const tone: BadgeTone = SUCCESS.has(value) ? "success" : DANGER.has(value) ? "danger" : INFO.has(value) ? "info" : "neutral";
  return <Badge tone={tone}>{humanize(value)}</Badge>;
}

export function Money({ amount, currency }: { amount: string; currency: string }) {
  return <span className={styles.mono}>{`${amount} ${currency}`}</span>;
}

export interface PagedTableProps<T> {
  label: string;
  noun: string;
  load(cursor: string | undefined, signal?: AbortSignal): Promise<Page<T>>;
  loadKey: string;
  columns: DataTableColumn<T>[] | ((reload: () => void) => DataTableColumn<T>[]);
  getRowKey(row: T): string;
  empty: { title: string; description?: string };
}

export function PagedTable<T>(props: PagedTableProps<T>) {
  const state = useLoad((signal) => props.load(undefined, signal), props.loadKey);
  const [more, setMore] = useState<{ key: string; items: T[]; cursor?: string } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<Problem | null>(null);

  const extra = more && more.key === props.loadKey ? more : null;
  const rows = [...(state.data?.items ?? []), ...(extra?.items ?? [])];
  const cursor = extra ? extra.cursor : state.data?.nextCursor;
  const reload = () => {
    setMore(null);
    state.reload();
  };

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const next = await props.load(cursor);
      setMore({ key: props.loadKey, items: [...(extra?.items ?? []), ...next.items], cursor: next.nextCursor });
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <>
      {state.error ? (
        <LoadFailure error={state.error} reload={state.reload} what={props.noun} />
      ) : (
        <DataTable
          label={props.label}
          columns={typeof props.columns === "function" ? props.columns(reload) : props.columns}
          rows={rows}
          getRowKey={(row) => props.getRowKey(row)}
          loading={state.loading && !state.data}
          empty={props.empty}
        />
      )}
      {moreError ? <ProblemAlert problem={moreError} title={`Could not load more ${props.noun}`} /> : null}
      {!state.error && !state.loading && cursor ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            {`Load more ${props.noun}`}
          </Button>
        </div>
      ) : null}
    </>
  );
}

export interface ListPageProps<T> extends PagedTableProps<T> {
  title: string;
  description?: string;
  actions?: ReactNode;
  toolbar?: ReactNode;
  banner?: ReactNode;
}

export function ListPage<T>({ title, description, actions, toolbar, banner, ...table }: ListPageProps<T>) {
  return (
    <div className={styles.stack}>
      <PageHeader title={title} description={description} actions={actions} />
      {toolbar ? <div className={styles.toolbar}>{toolbar}</div> : null}
      {banner}
      <PagedTable {...table} />
    </div>
  );
}

export function useAction(onDone: () => void) {
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function run<T>(name: string, call: () => Promise<T>, done: string | ((result: T) => string)) {
    setBusy(name);
    setFailure(null);
    setNotice(null);
    try {
      const result = await call();
      setNotice(typeof done === "string" ? done : done(result));
      onDone();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const feedback = (
    <>
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? <ProblemAlert problem={failure} title="That didn't work" /> : null}
    </>
  );
  return { busy, run, feedback };
}

export function ConfirmAction({
  label,
  title,
  description,
  confirmLabel,
  variant = "danger",
  busy,
  disabled,
  size = "md",
  onConfirm,
}: {
  label: string;
  title: string;
  description: string;
  confirmLabel: string;
  variant?: ButtonVariant;
  busy: boolean;
  disabled?: boolean;
  size?: "sm" | "md";
  onConfirm(): void;
}) {
  const [asking, setAsking] = useState(false);
  return (
    <>
      <Button variant={variant} size={size} loading={busy} disabled={disabled} onClick={() => setAsking(true)}>
        {label}
      </Button>
      {asking ? (
        <ConfirmDialog
          title={title}
          description={description}
          confirmLabel={confirmLabel}
          cancelLabel="Keep as is"
          onCancel={() => setAsking(false)}
          onConfirm={() => {
            setAsking(false);
            onConfirm();
          }}
        />
      ) : null}
    </>
  );
}

type Errors = Record<string, string>;

export function useSubmit(codeField: Record<string, string> = {}) {
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const key = useRef(newKey());

  const touch = () => {
    key.current = newKey();
  };

  async function run<T>(call: (key: string) => Promise<T>, onDone: (result: T) => void) {
    setBusy(true);
    setFailure(null);
    setErrors({});
    try {
      onDone(await call(key.current));
    } catch (error) {
      const problem = describeProblem(error);
      const field = codeField[problem.code];
      const found: Errors = field ? { [field]: problemText(problem) } : { ...problem.fields };
      if (Object.keys(found).length > 0) setErrors(found);
      else setFailure(problem);
      if (problem.code === "idempotency_key_reuse") touch();
    } finally {
      setBusy(false);
    }
  }

  return { busy, failure, errors, setErrors, setFailure, touch, run };
}

export function FormActions({ busy, submit, cancelTo, failure, failureTitle }: { busy: boolean; submit: string; cancelTo?: string; failure: Problem | null; failureTitle: string }) {
  return (
    <>
      {failure ? (
        <div className={styles.formActions}>
          <ProblemAlert problem={failure} title={failureTitle} />
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {submit}
        </Button>
        {cancelTo ? <BackLink to={cancelTo}>Cancel</BackLink> : null}
      </div>
    </>
  );
}

interface FieldBase {
  id: string;
  label: string;
  error?: string;
  hint?: string;
  optional?: boolean;
}

export function TextField({
  id,
  label,
  error,
  hint,
  optional,
  value,
  onChange,
  ...input
}: FieldBase & { value: string; onChange(value: string): void } & Omit<InputProps, "id" | "value" | "onChange">) {
  return (
    <FormField id={id} label={label} hint={hint} error={error} optional={optional}>
      {(control) => <Input {...control} {...input} value={value} invalid={Boolean(error)} onChange={(event) => onChange(event.target.value)} />}
    </FormField>
  );
}

export function TextAreaField({ id, label, error, hint, optional, value, onChange, rows = 3, maxLength }: FieldBase & { value: string; onChange(value: string): void; rows?: number; maxLength?: number }) {
  return (
    <FormField id={id} label={label} hint={hint} error={error} optional={optional}>
      {(control) => <Textarea {...control} rows={rows} maxLength={maxLength} value={value} invalid={Boolean(error)} onChange={(event) => onChange(event.target.value)} />}
    </FormField>
  );
}

export function SelectField({
  id,
  label,
  error,
  hint,
  optional,
  value,
  onChange,
  options,
  placeholder,
}: FieldBase & { value: string; onChange(value: string): void; options: ReadonlyArray<readonly [string, string]>; placeholder?: string }) {
  return (
    <FormField id={id} label={label} hint={hint} error={error} optional={optional}>
      {(control) => (
        <Select {...control} value={value} invalid={Boolean(error)} onChange={(event) => onChange(event.target.value)}>
          {placeholder !== undefined ? <option value="">{placeholder}</option> : null}
          {options.map(([optionValue, text]) => (
            <option key={optionValue} value={optionValue}>
              {text}
            </option>
          ))}
        </Select>
      )}
    </FormField>
  );
}

export function CheckField({ id, label, checked, onChange }: { id: string; label: string; checked: boolean; onChange(checked: boolean): void }) {
  return (
    <div className={styles.check}>
      <Input id={id} type="checkbox" checked={checked} onChange={(event) => onChange(event.target.checked)} />
      <label htmlFor={id}>{label}</label>
    </div>
  );
}

export const labelled = (values: readonly string[]): Array<readonly [string, string]> => values.map((v) => [v, humanize(v)] as const);

export const opt = (value: string): string | undefined => {
  const trimmed = value.trim();
  return trimmed === "" ? undefined : trimmed;
};
