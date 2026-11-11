import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Alert, Badge, Button, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Textarea, type DataTableColumn } from "../../../shell/ui";
import {
  assignRole,
  endAssignment,
  listAssignments,
  listLegalEntities,
  UUID,
  type AssignmentQuery,
  type RoleAssignment,
} from "./api";
import { EntityLookup } from "../../../shell/lookup/EntityLookup";
import styles from "./security.module.css";
import {
  ApiFailure,
  canRetry,
  fieldsOwnFailure,
  mapServerFields,
  toIsoFromLocal,
  useFocusFirstInvalid,
  withoutFields,
  type Errors,
} from "./support";

const DUTY = "identity.security-role.maintain";
const ASSIGN_FIELDS = ["userId", "role", "legalEntityId", "validFrom", "validTo", "reason"] as const;
const REASON_MAX = 500;

interface FindValues {
  userId: string;
  role: string;
  activeOnly: boolean;
}

interface AssignValues {
  userId: string;
  role: string;
  legalEntityId: string;
  validFrom: string;
  validTo: string;
  reason: string;
}

const emptyAssign: AssignValues = { userId: "", role: "", legalEntityId: "", validFrom: "", validTo: "", reason: "" };

type Standing = "scheduled" | "active" | "ended";

function standing(a: RoleAssignment, now: number): Standing {
  if (a.validTo !== undefined && Date.parse(a.validTo) <= Date.parse(a.validFrom)) return "ended";
  if (Date.parse(a.validFrom) > now) return "scheduled";
  if (a.validTo !== undefined && Date.parse(a.validTo) <= now) return "ended";
  return "active";
}

function validateFind(values: FindValues): Errors {
  const errors: Errors = {};
  const userId = values.userId.trim();
  if (userId === "" && values.role.trim() === "") errors.userId = "Choose a user, a role, or both.";
  else if (userId !== "" && !UUID.test(userId)) errors.userId = "Choose a user, or clear it.";
  return errors;
}

function validateAssign(values: AssignValues): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.userId.trim())) errors.userId = "Choose the user to assign.";
  if (values.role.trim() === "") errors.role = "Enter the role to assign.";
  if (values.legalEntityId.trim() !== "" && !UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id as a UUID.";
  const from = toIsoFromLocal(values.validFrom);
  const to = toIsoFromLocal(values.validTo);
  if (from && to && Date.parse(to) <= Date.parse(from)) errors.validTo = "The end must be after the start.";
  if (new TextEncoder().encode(values.reason.trim()).length > REASON_MAX) errors.reason = `Keep the reason to ${REASON_MAX} bytes or fewer; accented and non-Latin characters take more than one.`;
  return errors;
}

function when(value?: string) {
  return value ? <time dateTime={value}>{value}</time> : "Open-ended";
}

export function RoleAssignmentsPage() {
  const queryClient = useQueryClient();
  const findForm = useRef<HTMLFormElement>(null);
  const resultsHeading = useRef<HTMLHeadingElement>(null);
  const assignForm = useRef<HTMLFormElement>(null);
  const [find, setFind] = useState<FindValues>({ userId: "", role: "", activeOnly: true });
  const [findErrors, setFindErrors] = useState<Errors>({});
  const [applied, setApplied] = useState<AssignmentQuery | null>(null);
  const [values, setValues] = useState<AssignValues>(emptyAssign);
  const [errors, setErrors] = useState<Errors>({});
  const [saved, setSaved] = useState<string | null>(null);
  const [ending, setEnding] = useState<RoleAssignment | null>(null);
  const [ended, setEnded] = useState<string | null>(null);

  const assignments = useInfiniteQuery({
    queryKey: ["admin", "assignments", applied],
    queryFn: ({ pageParam }) => listAssignments({ ...applied!, after: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
    enabled: applied !== null,
    retry: false,
  });
  const entities = useQuery({ queryKey: ["admin", "legal-entities", "options"], queryFn: () => listLegalEntities(), retry: false });

  const assign = useMutation({
    mutationFn: (v: AssignValues) =>
      assignRole({
        userId: v.userId.trim(),
        role: v.role.trim(),
        legalEntityId: v.legalEntityId.trim() || undefined,
        validFrom: toIsoFromLocal(v.validFrom),
        validTo: toIsoFromLocal(v.validTo),
        reason: v.reason.trim() || undefined,
      }),
    onSuccess: async (stored) => {
      setSaved(`${stored.role} assigned`);
      setValues(emptyAssign);
      await queryClient.invalidateQueries({ queryKey: ["admin", "assignments"] });
    },
    onError: (error) => setErrors(mapServerFields(error, ASSIGN_FIELDS)),
  });
  const end = useMutation({
    mutationFn: (a: RoleAssignment) => endAssignment(a.assignmentId),
    onSuccess: async (stored) => {
      setEnded(`${stored.role} ended`);
      await queryClient.invalidateQueries({ queryKey: ["admin", "assignments"] });
      resultsHeading.current?.focus();
    },
    onError: () => setEnded(null),
  });
  useFocusFirstInvalid(findForm, findErrors);
  useFocusFirstInvalid(assignForm, errors);

  function changeFind(next: Partial<FindValues>) {
    setFind({ ...find, ...next });
    setFindErrors(withoutFields(findErrors, ["userId"]));
  }

  function changeAssign(next: Partial<AssignValues>) {
    setValues({ ...values, ...next });
    setErrors(withoutFields(errors, Object.keys(next)));
    setSaved(null);
    assign.reset();
  }

  function submitFind(event: FormEvent) {
    event.preventDefault();
    const found = validateFind(find);
    setFindErrors(found);
    if (Object.keys(found).length > 0) return;
    setApplied({ userId: find.userId.trim() || undefined, role: find.role.trim() || undefined, active: find.activeOnly });
    setEnded(null);
    end.reset();
  }

  function submitAssign(event: FormEvent) {
    event.preventDefault();
    const found = validateAssign(values);
    setErrors(found);
    if (Object.keys(found).length === 0) assign.mutate(values);
  }

  function confirmEnd() {
    if (!ending) return;
    end.mutate(ending);
    setEnding(null);
  }

  const now = Date.now();
  const rows = assignments.data?.pages.flatMap((page) => page.items) ?? [];
  const columns: DataTableColumn<RoleAssignment>[] = [
    { key: "role", header: "Role" },
    { key: "userId", header: "User", mono: true },
    { key: "legalEntityId", header: "Legal entity", mono: true, render: (a) => a.legalEntityId ?? "Tenant-wide" },
    { key: "validFrom", header: "Valid from", mono: true, render: (a) => when(a.validFrom) },
    { key: "validTo", header: "Valid to", mono: true, render: (a) => when(a.validTo) },
    {
      key: "status",
      header: "Status",
      render: (a) => {
        const state = standing(a, now);
        return <Badge tone={state === "active" ? "success" : state === "scheduled" ? "info" : "neutral"}>{state === "active" ? "Active" : state === "scheduled" ? "Scheduled" : "Ended"}</Badge>;
      },
    },
    { key: "reason", header: "Reason", render: (a) => a.reason ?? "" },
    { key: "grantedBy", header: "Granted by", render: (a) => a.grantedBy ?? "" },
    {
      key: "actions",
      header: "Actions",
      render: (a) =>
        standing(a, now) === "ended" ? null : (
          <Button size="sm" variant="secondary" disabled={end.isPending} onClick={() => setEnding(a)} aria-label={`${standing(a, now) === "scheduled" ? "Cancel" : "End"} ${a.role} for user ${a.userId}`}>
            {standing(a, now) === "scheduled" ? "Cancel" : "End"}
          </Button>
        ),
    },
  ];

  const assignFailure = assign.isError && !fieldsOwnFailure(assign.error, ASSIGN_FIELDS);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Role assignments"
        description="Look up who holds a role, assign roles with dates and a reason, and end assignments. Ending keeps the history; nothing is deleted."
      />
      <Card title="Assign a role">
        <form className={styles.form} onSubmit={submitAssign} noValidate aria-label="Assign a role" ref={assignForm}>
          <fieldset className={styles.fields} disabled={assign.isPending}>
            <FormField id="as-user" label="User to assign" hint="Search by name or email, or paste the user's id." error={errors.userId}>
              {(c) => <EntityLookup {...c} entity="user" value={values.userId} onChange={(e) => changeAssign({ userId: e.target.value })} invalid={Boolean(errors.userId)} />}
            </FormField>
            <FormField id="as-role" label="Role to assign" hint="The name of a duty, for example identity.security-role.maintain." error={errors.role}>
              {(c) => <Input {...c} value={values.role} onChange={(e) => changeAssign({ role: e.target.value })} invalid={Boolean(errors.role)} />}
            </FormField>
            <FormField
              id="as-entity"
              label="Legal entity"
              optional
              hint={entities.isError || entities.data?.next ? "Leave empty to assign the role across the whole tenant. Suggestions show only the first legal entities you can see; paste any other id." : "Leave empty to assign the role across the whole tenant."}
              error={errors.legalEntityId}
            >
              {(c) => (
                <>
                  <Input {...c} list="as-entity-options" value={values.legalEntityId} onChange={(e) => changeAssign({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} autoComplete="off" />
                  <datalist id="as-entity-options">
                    {(entities.data?.items ?? []).map((le) => (
                      <option key={le.legalEntityId} value={le.legalEntityId}>
                        {`${le.code} – ${le.displayName}`}
                      </option>
                    ))}
                  </datalist>
                </>
              )}
            </FormField>
            <FormField id="as-from" label="Valid from" optional hint="Leave empty to start now." error={errors.validFrom}>
              {(c) => <Input {...c} type="datetime-local" value={values.validFrom} onChange={(e) => changeAssign({ validFrom: e.target.value })} invalid={Boolean(errors.validFrom)} />}
            </FormField>
            <FormField id="as-to" label="Valid to" optional hint="Leave empty for no end date." error={errors.validTo}>
              {(c) => <Input {...c} type="datetime-local" value={values.validTo} onChange={(e) => changeAssign({ validTo: e.target.value })} invalid={Boolean(errors.validTo)} />}
            </FormField>
            <div className={styles.wide}>
              <FormField id="as-reason" label="Reason" optional hint="Recorded in the audit trail with the assignment." error={errors.reason}>
                {(c) => <Textarea {...c} rows={2} value={values.reason} onChange={(e) => changeAssign({ reason: e.target.value })} invalid={Boolean(errors.reason)} />}
              </FormField>
            </div>
          </fieldset>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={assign.isPending}>
              Assign role
            </Button>
          </div>
        </form>
      </Card>
      <Alert tone="success" live="status" title={saved ?? undefined} />
      {assignFailure ? <ApiFailure error={assign.error} title="Could not assign the role" duty={DUTY} /> : null}

      <Card title="Find assignments">
        <form className={styles.form} onSubmit={submitFind} noValidate aria-label="Find assignments" ref={findForm}>
          <FormField id="fa-user" label="Find by user" hint="List by user, by role, or both." error={findErrors.userId}>
            {(c) => <EntityLookup {...c} entity="user" value={find.userId} onChange={(e) => changeFind({ userId: e.target.value })} invalid={Boolean(findErrors.userId)} />}
          </FormField>
          <FormField id="fa-role" label="Find by role">
            {(c) => <Input {...c} value={find.role} onChange={(e) => changeFind({ role: e.target.value })} />}
          </FormField>
          <FormField id="fa-active" label="Only assignments in effect now">
            {(c) => <Input {...c} type="checkbox" checked={find.activeOnly} onChange={(e) => changeFind({ activeOnly: e.target.checked })} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={assignments.isFetching && rows.length === 0}>
              Find assignments
            </Button>
          </div>
        </form>
      </Card>
      <h2 ref={resultsHeading} tabIndex={-1} className={styles.resultsHeading}>
        Assignments
      </h2>
      <Alert tone="success" live="status" title={ended ?? undefined} />
      {end.isError ? <ApiFailure error={end.error} title="Could not end the assignment" duty={DUTY} /> : null}
      {assignments.isError ? (
        <ApiFailure
          error={assignments.error}
          title="Could not load the assignments"
          duty={DUTY}
          action={
            canRetry(assignments.error) ? (
              <Button variant="secondary" loading={assignments.isFetching} onClick={() => void assignments.refetch()}>
                Try again
              </Button>
            ) : undefined
          }
        />
      ) : applied ? (
        <>
          <DataTable
            label="Role assignments"
            columns={columns}
            rows={rows}
            getRowKey={(a) => a.assignmentId}
            loading={assignments.isPending}
            empty={{ title: "No assignments found", description: "Nothing matches this lookup. Try a different user or role, or include assignments that are no longer in effect." }}
          />
          {assignments.hasNextPage ? (
            <div className={styles.formActions}>
              <Button variant="secondary" loading={assignments.isFetchingNextPage} onClick={() => void assignments.fetchNextPage()}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      ) : (
        <p className={styles.note}>Look up a user or a role to see their assignments.</p>
      )}
      {ending ? (
        <ConfirmDialog
          title={standing(ending, now) === "scheduled" ? "Cancel this assignment?" : "End this assignment?"}
          description={
            standing(ending, now) === "scheduled"
              ? `${ending.role} for user ${ending.userId} has not started yet. Cancelling means it never takes effect.`
              : `${ending.role} for user ${ending.userId} stops applying now. The record stays in the history.`
          }
          confirmLabel={standing(ending, now) === "scheduled" ? "Cancel assignment" : "End assignment"}
          cancelLabel="Keep assignment"
          onConfirm={confirmEnd}
          onCancel={() => setEnding(null)}
        />
      ) : null}
    </div>
  );
}
