import { useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  createTeam,
  getTeam,
  listTeamMembers,
  listTeams,
  listTeamTypes,
  setTeamStatus,
  withFreshRowOnConflict,
  type Team,
  type TeamMember,
  type TeamType,
} from "./api/structure";
import { useLegalEntity } from "./legalEntityContext";
import styles from "./pages.module.css";
import { StatusAction, useRows } from "./referenceParts";
import { AddMemberForm, CreateTeamTypeForm, EditTeamForm, MemberActions } from "./TeamWrites";
import { ProblemMessage } from "../../shell/errors";

const codePattern = /^[A-Za-z0-9][A-Za-z0-9_-]{1,19}$/;
const datePattern = /^\d{4}-\d{2}-\d{2}$/;
type Errors = Partial<Record<"code" | "name" | "teamTypeId" | "effectiveFrom" | "description", string>>;

export interface TeamValues {
  code: string;
  name: string;
  teamTypeId: string;
  effectiveFrom: string;
  description: string;
}

export function validateTeam({ code, name, teamTypeId, effectiveFrom, description }: TeamValues): Errors {
  const errors: Errors = {};
  if (!codePattern.test(code)) {
    errors.code = "Use 2 to 20 letters, digits, - or _.";
  }
  if (name.trim() === "" || name.trim().length > 60) {
    errors.name = "Enter a name of 1 to 60 characters.";
  }
  if (teamTypeId === "") {
    errors.teamTypeId = "Select a team type.";
  }
  if (!datePattern.test(effectiveFrom) || Number.isNaN(Date.parse(effectiveFrom))) {
    errors.effectiveFrom = "Enter a date as YYYY-MM-DD.";
  }
  if (description.length > 500) {
    errors.description = "Use at most 500 characters.";
  }
  return errors;
}

function today(): string {
  const now = new Date();
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
}
const empty = (): TeamValues => ({ code: "", name: "", teamTypeId: "", effectiveFrom: today(), description: "" });

function CreateTeamForm({ types, onCreated }: { types: TeamType[]; onCreated(): void }) {
  const [values, setValues] = useState<TeamValues>(empty);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateTeam(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createTeam({
        code: values.code.toUpperCase(),
        name: values.name.trim(),
        teamTypeId: values.teamTypeId,
        effectiveFrom: values.effectiveFrom,
        description: values.description.trim() === "" ? undefined : values.description.trim(),
      });
      setValues(empty());
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-TEAM-CODE-DUPLICATE") {
        setErrors({ code: "A team with this code already exists." });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof TeamValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <Card title="Add team">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="team-code" label="Code" error={errors.code}>
          {(control) => <Input {...control} {...text("code")} maxLength={20} />}
        </FormField>
        <FormField id="team-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} {...text("name")} maxLength={60} />}
        </FormField>
        <FormField id="team-type" label="Team type" error={errors.teamTypeId}>
          {(control) => (
            <Select {...control} {...text("teamTypeId")}>
              <option value="">Select a team type</option>
              {types.map((type) => (
                <option key={type.id} value={type.id}>
                  {type.code} - {type.name}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="team-effective-from" label="Effective from" hint="YYYY-MM-DD" error={errors.effectiveFrom}>
          {(control) => <Input {...control} {...text("effectiveFrom")} />}
        </FormField>
        <FormField id="team-description" label="Description" optional error={errors.description}>
          {(control) => <Textarea {...control} {...text("description")} maxLength={500} />}
        </FormField>
        {failure ? (
          <Alert tone="danger" title="Could not add the team" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div>
          <Button type="submit" variant="primary" loading={busy}>
            Add team
          </Button>
        </div>
      </form>
    </Card>
  );
}

const memberColumns: DataTableColumn<TeamMember>[] = [
  { key: "memberKind", header: "Kind" },
  { key: "memberId", header: "Member", mono: true },
  { key: "memberRole", header: "Role" },
  { key: "validFrom", header: "From", mono: true },
  { key: "validTo", header: "To", mono: true },
];

function TeamMembers({ team, canWrite }: { team: Team; canWrite: boolean }) {
  const members = useRows((signal) => listTeamMembers(team.id, { signal }), team.id);
  const columns: DataTableColumn<TeamMember>[] = canWrite
    ? [...memberColumns, { key: "actions", header: "Actions", render: (row) => <MemberActions team={team} member={row} onChanged={members.reload} /> }]
    : memberColumns;
  return (
    <Card title={`Members of ${team.code}`}>
      {members.error ? (
        <Alert tone="danger" title="Could not load the members" live="alert">
          <ProblemMessage problem={members.error} />
        </Alert>
      ) : null}
      <DataTable
        label={`Members of ${team.code}`}
        columns={columns}
        rows={members.rows}
        getRowKey={(row) => row.id}
        loading={members.loading}
        empty={{ title: "No members" }}
      />
      {canWrite ? <AddMemberForm team={team} onAdded={members.reload} /> : null}
    </Card>
  );
}

export function TeamsPage() {
  const { canWriteOperatingUnit, canAdministerTenant } = useLegalEntity();
  // Team writes are tenant-level: the service also requires tenant.administer and no legal-entity claim.
  const canWrite = canWriteOperatingUnit && canAdministerTenant;
  const types = useRows((signal) => listTeamTypes({ signal }), "team-types");
  const state = useRows((signal) => listTeams({ signal }), "teams");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = state.rows.find((row) => row.id === selectedId) ?? null;
  const typeName = (id: string) => types.rows.find((type) => type.id === id)?.code ?? id;
  const columns: DataTableColumn<Team>[] = [
    { key: "code", header: "Code", mono: true },
    { key: "name", header: "Name" },
    { key: "teamTypeId", header: "Type", render: (row) => typeName(row.teamTypeId) },
    { key: "effectiveFrom", header: "Effective from", mono: true },
    { key: "effectiveTo", header: "Effective to", mono: true },
    { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) => (
        <div className={styles.rowActions}>
          <Button size="sm" aria-label={`Show members of ${row.code}`} onClick={() => setSelectedId(row.id)}>
            Members
          </Button>
          {canWrite ? (
            <StatusAction
              subject={`team ${row.code}`}
              status={row.status}
              apply={(next) =>
                withFreshRowOnConflict(
                  () => setTeamStatus(row.id, row.version, next),
                  () => getTeam(row.id),
                  (fresh) => state.update((rows) => rows.map((item) => (item.id === row.id ? fresh : item))),
                )
              }
            />
          ) : null}
        </div>
      ),
    },
  ];
  return (
    <>
      <PageHeader title="Teams" description="Groups of users and parties with effective-dated membership." />
      {canWrite ? <CreateTeamTypeForm onCreated={types.reload} /> : null}
      {canWrite ? <CreateTeamForm types={types.rows} onCreated={state.reload} /> : null}
      {types.error ? (
        <Alert tone="danger" title="Could not load team types" live="alert">
          <ProblemMessage problem={types.error} />
        </Alert>
      ) : null}
      {state.error ? (
        <Alert tone="danger" title="Could not load teams" live="alert">
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : null}
      <DataTable label="Teams" columns={columns} rows={state.rows} getRowKey={(row) => row.id} loading={state.loading} empty={{ title: "No teams" }} />
      {selected && canWrite ? (
        <EditTeamForm
          key={selected.id}
          team={selected}
          onSaved={(next) => state.update((rows) => rows.map((item) => (item.id === selected.id ? { ...item, ...next } : item)))}
        />
      ) : null}
      {selected ? <TeamMembers team={selected} canWrite={canWrite} /> : null}
    </>
  );
}
