import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatusPill, type DataTableColumn } from "../../../shell/ui";
import {
  getUser,
  listAssignments,
  listLegalEntities,
  listUsers,
  SecurityApiError,
  USER_STATUSES,
  UUID,
  type LegalEntity,
  type RoleAssignment,
  type User,
  type UserQuery,
  type UserStatus,
} from "./api";
import styles from "./security.module.css";
import { ApiFailure, retryAction } from "./support";

const DUTY = "identity.user.maintain";

async function listAllLegalEntities(): Promise<LegalEntity[]> {
  const all: LegalEntity[] = [];
  let after: string | undefined;
  do {
    const page = await listLegalEntities(after);
    all.push(...page.items);
    after = page.next;
  } while (after);
  return all;
}

const columns: DataTableColumn<User>[] = [
  {
    key: "displayName",
    header: "Name",
    render: (u) => <Link to={`/admin/security/users/${encodeURIComponent(u.userId)}`}>{u.displayName}</Link>,
  },
  { key: "email", header: "Email", render: (u) => u.email ?? "None" },
  {
    key: "status",
    header: "Status",
    render: (u) => <StatusPill status={u.status} />,
  },
  {
    key: "createdAt",
    header: "Created",
    mono: true,
    render: (u) => <time dateTime={u.createdAt}>{u.createdAt}</time>,
  },
];

const assignmentColumns: DataTableColumn<RoleAssignment>[] = [
  { key: "role", header: "Role" },
  {
    key: "legalEntityId",
    header: "Legal entity",
    mono: true,
    render: (a) => a.legalEntityId ?? "Tenant-wide",
  },
  {
    key: "validFrom",
    header: "Valid from",
    mono: true,
    render: (a) => <time dateTime={a.validFrom}>{a.validFrom}</time>,
  },
  {
    key: "validTo",
    header: "Valid to",
    mono: true,
    render: (a) => (a.validTo ? <time dateTime={a.validTo}>{a.validTo}</time> : "Open-ended"),
  },
];

export function UsersPage() {
  const [draft, setDraft] = useState<{ q: string; status: UserStatus | ""; legalEntityId: string }>({ q: "", status: "", legalEntityId: "" });
  const [applied, setApplied] = useState<UserQuery>({});
  const users = useInfiniteQuery({
    queryKey: ["admin", "users", applied],
    queryFn: ({ pageParam }) => listUsers({ ...applied, after: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
    retry: false,
  });
  const entities = useQuery({
    queryKey: ["admin", "legal-entities", "options"],
    queryFn: listAllLegalEntities,
    retry: false,
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    setApplied({
      q: draft.q.trim(),
      status: draft.status || undefined,
      legalEntityId: draft.legalEntityId || undefined,
    });
  }

  const rows = users.data?.pages.flatMap((page) => page.items) ?? [];
  const filtered = Boolean(applied.q || applied.status || applied.legalEntityId);

  return (
    <div className={styles.stack}>
      <PageHeader title="Users" description="The people and services in your tenant. Open a user to see their role assignments and effective permissions." />
      <Card title="Find users">
        <form className={styles.form} onSubmit={submit} aria-label="Find users">
          <FormField id="usr-q" label="Search" hint="Matches name, email or identity-provider subject.">
            {(c) => (
              <Input {...c} type="search" maxLength={100} value={draft.q} onChange={(e) => setDraft({ ...draft, q: e.target.value })} autoComplete="off" />
            )}
          </FormField>
          <FormField id="usr-status" label="Status">
            {(c) => (
              <Select {...c} value={draft.status} onChange={(e) => setDraft({ ...draft, status: e.target.value as UserStatus | "" })}>
                <option value="">Any status</option>
                {USER_STATUSES.map((status) => (
                  <option key={status} value={status}>
                    {status === "active" ? "Active" : "Deprovisioned"}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="usr-entity" label="Legal entity" hint="Users with a role assignment for this entity.">
            {(c) => (
              <Select {...c} value={draft.legalEntityId} onChange={(e) => setDraft({ ...draft, legalEntityId: e.target.value })}>
                <option value="">All legal entities</option>
                {(entities.data ?? []).map((le) => (
                  <option key={le.legalEntityId} value={le.legalEntityId}>
                    {`${le.code} - ${le.displayName}`}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={users.isFetching && !users.isFetchingNextPage}>
              Search users
            </Button>
          </div>
        </form>
      </Card>
      {users.isError ? (
        <ApiFailure
          error={users.error}
          title="Could not load the users"
          duty={DUTY}
          action={retryAction(users.error, users.isFetching, () => void users.refetch())}
        />
      ) : (
        <>
          <DataTable
            label="Users"
            columns={columns}
            rows={rows}
            getRowKey={(u) => u.userId}
            loading={users.isPending}
            empty={
              filtered
                ? {
                    title: "No users match",
                    description: "Clear the search or filters to see every user.",
                  }
                : {
                    title: "No users yet",
                    description: "Users appear here once they sign in.",
                  }
            }
          />
          {users.hasNextPage ? (
            <div className={styles.formActions}>
              <Button variant="secondary" loading={users.isFetchingNextPage} onClick={() => void users.fetchNextPage()}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}

export function UserDetailPage() {
  const userId = useParams().userId ?? "";
  const validId = UUID.test(userId);
  const user = useQuery({
    queryKey: ["admin", "user", userId],
    queryFn: () => getUser(userId),
    enabled: validId,
    retry: false,
  });
  const assignments = useInfiniteQuery({
    queryKey: ["admin", "assignments", { userId, active: true }],
    queryFn: ({ pageParam }) => listAssignments({ userId, active: true, after: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
    enabled: user.isSuccess,
    retry: false,
  });
  const notFound = user.error instanceof SecurityApiError && user.error.status === 404;
  const rows = assignments.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <div className={styles.stack}>
      <PageHeader title={user.data?.displayName ?? "User"} description="Profile and current role assignments." />
      <p>
        <Link to="/admin/security/users">Back to users</Link>
      </p>
      {!validId ? (
        <Alert tone="danger" title="User not found" live="alert">
          The user id in the address is not a valid UUID.
        </Alert>
      ) : null}
      {user.isError ? (
        <ApiFailure
          error={user.error}
          title={notFound ? "User not found" : "Could not load the user"}
          duty={DUTY}
          action={retryAction(user.error, user.isFetching, () => void user.refetch())}
        />
      ) : null}
      <p className={styles.note} role="status">
        {validId && user.isPending ? "Loading user…" : null}
      </p>
      {user.data ? (
        <>
          <Card title="Profile">
            <dl className={styles.facts}>
              <dt>Email</dt>
              <dd>{user.data.email ?? "None"}</dd>
              <dt>Status</dt>
              <dd>
                <StatusPill status={user.data.status} />
              </dd>
              <dt>Identity-provider subject</dt>
              <dd className={styles.mono}>{user.data.idpSubject}</dd>
              <dt>User id</dt>
              <dd className={styles.mono}>{user.data.userId}</dd>
            </dl>
            <p>
              <Link to={`/admin/security/permissions?userId=${encodeURIComponent(user.data.userId)}`}>View effective permissions</Link>
            </p>
          </Card>
          <Card title="Role assignments">
            <p className={styles.note}>
              <Badge tone="info">Active</Badge> assignments only. Use Role assignments to assign, end or review history.
            </p>
            {assignments.isError ? (
              <ApiFailure
                error={assignments.error}
                title="Could not load the role assignments"
                duty="identity.security-role.maintain"
                action={retryAction(assignments.error, assignments.isFetching, () => void assignments.refetch())}
              />
            ) : (
              <>
                <DataTable
                  label="User role assignments"
                  columns={assignmentColumns}
                  rows={rows}
                  getRowKey={(a) => a.assignmentId}
                  loading={assignments.isPending}
                  empty={{
                    title: "No active assignments",
                    description: "This user holds no roles right now.",
                  }}
                />
                {assignments.hasNextPage ? (
                  <div className={styles.formActions}>
                    <Button variant="secondary" loading={assignments.isFetchingNextPage} onClick={() => void assignments.fetchNextPage()}>
                      Load more
                    </Button>
                  </div>
                ) : null}
              </>
            )}
          </Card>
        </>
      ) : null}
    </div>
  );
}
