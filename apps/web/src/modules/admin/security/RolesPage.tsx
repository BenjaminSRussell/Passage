import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../../shell/ui";
import { getRole, listDuties, listRoles, SecurityApiError, type DutyDetail, type DutySummary, type RoleSummary } from "./api";
import styles from "./security.module.css";
import { ApiFailure, canRetry } from "./support";

const DUTY = "Maintain security roles";

const roleColumns: DataTableColumn<RoleSummary>[] = [
  {
    key: "name",
    header: "Role",
    render: (r) => <Link to={`/admin/security/roles/${encodeURIComponent(r.name)}`}>{r.name}</Link>,
  },
  { key: "duties", header: "Duties", render: (r) => r.duties.join(", ") },
];

const dutyColumns: DataTableColumn<DutySummary>[] = [
  { key: "name", header: "Duty" },
  {
    key: "serviceOnly",
    header: "Kind",
    render: (d) => (d.serviceOnly ? <Badge tone="warning">Service only</Badge> : <Badge tone="info">Person</Badge>),
  },
  { key: "permissionCount", header: "Routes granted", align: "end" },
];

const permissionColumns: DataTableColumn<DutyDetail["permissions"][number]>[] = [
  { key: "resource", header: "Route", mono: true },
  {
    key: "methods",
    header: "Methods",
    mono: true,
    render: (p) => p.methods.join(", "),
  },
];

function retry(error: unknown, fetching: boolean, refetch: () => void) {
  return canRetry(error) ? (
    <Button variant="secondary" loading={fetching} onClick={refetch}>
      Try again
    </Button>
  ) : undefined;
}

export function RolesPage() {
  const [draft, setDraft] = useState({ q: "", serviceOnly: "" });
  const [applied, setApplied] = useState({ q: "", serviceOnly: "" });
  const roles = useQuery({
    queryKey: ["admin", "roles", applied.q],
    queryFn: () => listRoles(applied.q || undefined),
    retry: false,
  });
  const duties = useQuery({
    queryKey: ["admin", "duties", applied],
    queryFn: () => listDuties(applied.q || undefined, applied.serviceOnly === "" ? undefined : applied.serviceOnly === "service"),
    retry: false,
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    setApplied({ q: draft.q.trim(), serviceOnly: draft.serviceOnly });
  }

  const filtered = applied.q !== "";

  return (
    <div className={styles.stack}>
      <PageHeader title="Role catalogue" description="The roles that can be assigned and the duties and routes behind them. This catalogue is read-only." />
      <Card title="Filter">
        <form className={styles.form} onSubmit={submit} aria-label="Filter the catalogue">
          <FormField id="cat-q" label="Search" hint="Matches the role or duty name.">
            {(c) => (
              <Input {...c} type="search" maxLength={100} value={draft.q} onChange={(e) => setDraft({ ...draft, q: e.target.value })} autoComplete="off" />
            )}
          </FormField>
          <FormField id="cat-kind" label="Duty kind" hint="Applies to the duties list.">
            {(c) => (
              <Select {...c} value={draft.serviceOnly} onChange={(e) => setDraft({ ...draft, serviceOnly: e.target.value })}>
                <option value="">All duties</option>
                <option value="person">Person duties only</option>
                <option value="service">Service-only duties</option>
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Apply filter
            </Button>
          </div>
        </form>
      </Card>
      <h2 className={styles.resultsHeading}>Roles</h2>
      {roles.isError ? (
        <ApiFailure
          error={roles.error}
          title="Could not load the roles"
          duty={DUTY}
          action={retry(roles.error, roles.isFetching, () => void roles.refetch())}
        />
      ) : (
        <DataTable
          label="Roles"
          columns={roleColumns}
          rows={roles.data ?? []}
          getRowKey={(r) => r.name}
          loading={roles.isPending}
          empty={{
            title: filtered ? "No roles match" : "No roles yet",
            description: filtered ? "Clear the search to see every role." : undefined,
          }}
        />
      )}
      <h2 className={styles.resultsHeading}>Duties</h2>
      {duties.isError ? (
        <ApiFailure
          error={duties.error}
          title="Could not load the duties"
          duty={DUTY}
          action={retry(duties.error, duties.isFetching, () => void duties.refetch())}
        />
      ) : (
        <DataTable
          label="Duties"
          columns={dutyColumns}
          rows={duties.data ?? []}
          getRowKey={(d) => d.name}
          loading={duties.isPending}
          empty={{
            title: "No duties match",
            description: "Clear the search or duty kind to see every duty.",
          }}
        />
      )}
    </div>
  );
}

export function RoleDetailPage() {
  const name = useParams().name ?? "";
  const role = useQuery({
    queryKey: ["admin", "role", name],
    queryFn: () => getRole(name),
    retry: false,
  });
  const notFound = role.error instanceof SecurityApiError && role.error.status === 404;

  return (
    <div className={styles.stack}>
      <PageHeader title={role.data?.name ?? "Role"} description="The duties this role holds and the routes each duty grants." />
      <p>
        <Link to="/admin/security/roles">Back to the role catalogue</Link>
      </p>
      {role.isError ? (
        <ApiFailure
          error={role.error}
          title={notFound ? "Role not found" : "Could not load the role"}
          duty={DUTY}
          action={retry(role.error, role.isFetching, () => void role.refetch())}
        />
      ) : null}
      <p className={styles.note} role="status">
        {role.isPending ? "Loading role…" : null}
      </p>
      {role.data && role.data.duties.length === 0 ? <p className={styles.note}>This role holds no duties.</p> : null}
      {role.data?.duties.map((duty) => (
        <Card key={duty.name} title={duty.name}>
          <DataTable
            label={`Routes granted by ${duty.name}`}
            columns={permissionColumns}
            rows={duty.permissions}
            getRowKey={(p) => p.resource}
            empty={{
              title: "No routes",
              description: "This duty grants no routes.",
            }}
          />
        </Card>
      ))}
    </div>
  );
}
