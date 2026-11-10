import { useQuery } from "@tanstack/react-query";
import { EntityLookup } from "../../../shell/lookup/EntityLookup";
import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { Badge, Button, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../../shell/ui";
import { fetchEffectivePermissions, SecurityApiError, UUID, type Permission } from "./api";
import styles from "./security.module.css";
import { ApiFailure, canRetry, useFocusFirstInvalid, withoutFields, type Errors } from "./support";

const DUTY = "identity.effective-permission.view";
const SCOPE_NOTE =
  'This report needs the "identity.effective-permission.view" or "identity.user.maintain" duty and a legal-entity scope of all entities. A caller scoped to one entity cannot see tenant-wide assignments, so the report would be incomplete.';
const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE"] as const;

interface Request {
  userId: string;
  legalEntityId: string;
}

const columns: DataTableColumn<Permission>[] = [
  { key: "resource", header: "Route", mono: true },
  { key: "method", header: "Method", mono: true },
  {
    key: "allow",
    header: "Decision",
    render: (p) => (p.allow ? <Badge tone="success">Allowed</Badge> : <Badge tone="danger">Denied</Badge>),
  },
  {
    key: "duty",
    header: "Duty",
    render: (p) => (p.allow ? (p.duty ?? "") : p.dutyMissing ? `Missing: ${p.dutyMissing}` : "No duty grants this route"),
  },
];

function validate(values: Request): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.userId.trim())) errors.userId = "Enter the user id as a UUID.";
  if (values.legalEntityId.trim() !== "" && !UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id as a UUID.";
  return errors;
}

export function EffectivePermissionsPage() {
  const form = useRef<HTMLFormElement>(null);
  const [params, setParams] = useSearchParams();
  const linkedUser = params.get("userId")?.trim() ?? "";
  const linked: Request | null = UUID.test(linkedUser) ? { userId: linkedUser, legalEntityId: "" } : null;
  const [values, setValues] = useState<Request>(linked ?? { userId: "", legalEntityId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [request, setRequest] = useState<Request | null>(linked);
  useEffect(() => {
    if (!UUID.test(linkedUser)) return;
    const next: Request = { userId: linkedUser, legalEntityId: "" };
    setRequest((prev) => (prev?.userId === linkedUser ? prev : next));
    setValues((prev) => (prev.userId.trim() === linkedUser ? prev : next));
  }, [linkedUser]);
  const [search, setSearch] = useState("");
  const [method, setMethod] = useState("");
  const [decision, setDecision] = useState("");
  const report = useQuery({
    queryKey: ["admin", "effective-permissions", request],
    queryFn: () => fetchEffectivePermissions(request!.userId, request!.legalEntityId || undefined),
    enabled: request !== null,
    retry: false,
  });
  useFocusFirstInvalid(form, errors);

  const visible = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return (report.data?.permissions ?? []).filter(
      (p) =>
        (needle === "" || p.resource.toLowerCase().includes(needle) || (p.duty ?? p.dutyMissing ?? "").toLowerCase().includes(needle)) &&
        (method === "" || p.method === method) &&
        (decision === "" || (decision === "allowed") === p.allow),
    );
  }, [report.data, search, method, decision]);

  function change(next: Partial<Request>) {
    setValues({ ...values, ...next });
    setErrors(withoutFields(errors, Object.keys(next)));
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validate(values);
    setErrors(found);
    if (Object.keys(found).length === 0) {
      setRequest({ userId: values.userId.trim(), legalEntityId: values.legalEntityId.trim() });
      setParams({ userId: values.userId.trim() });
    }
  }

  const notFound = report.error instanceof SecurityApiError && report.error.status === 404;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Effective permissions"
        description="See which routes and methods a user can call through the duties they hold today, and which duty would be missing for the rest."
      />
      <Card title="Choose a user">
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Choose a user" ref={form}>
          <FormField id="ep-user" label="User id" hint="The user's UUID." error={errors.userId}>
            {(c) => <Input {...c} value={values.userId} onChange={(e) => change({ userId: e.target.value })} invalid={Boolean(errors.userId)} autoComplete="off" />}
          </FormField>
          <FormField
            id="ep-entity"
            label="Legal entity"
            optional
            hint="Include assignments for this legal entity as well as tenant-wide ones."
            error={errors.legalEntityId}
          >
            {(c) => (
              <EntityLookup entity="legalEntity" prefill={false} {...c} value={values.legalEntityId} onChange={(e) => change({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />
            )}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={report.isFetching}>
              Show permissions
            </Button>
          </div>
        </form>
      </Card>
      {report.isError ? (
        <ApiFailure
          error={report.error}
          title={notFound ? "User not found" : "Could not load the effective permissions"}
          duty={DUTY}
          scopeNote={SCOPE_NOTE}
          action={
            canRetry(report.error) ? (
              <Button variant="secondary" loading={report.isFetching} onClick={() => void report.refetch()}>
                Try again
              </Button>
            ) : undefined
          }
        />
      ) : null}
      <p className={styles.note} role="status">{report.isFetching && !report.data ? "Loading permissions…" : null}</p>
      {report.data ? (
        <>
          <Card title="Duties in effect">
            <ul className={styles.summary} aria-label="Duties in effect">
              {report.data.duties.length === 0 ? <li>This user holds no duties right now.</li> : report.data.duties.map((d) => <li key={d}><Badge tone="info">{d}</Badge></li>)}
            </ul>
            {report.data.ignoredRoles.length > 0 ? (
              <p className={styles.note}>{`Not duties, so they grant nothing: ${report.data.ignoredRoles.join(", ")}.`}</p>
            ) : null}
          </Card>
          <Card title="Routes">
            <div className={styles.form}>
              <FormField id="ep-search" label="Search routes" hint="Matches the route or the duty name.">
                {(c) => <Input {...c} type="search" value={search} onChange={(e) => setSearch(e.target.value)} />}
              </FormField>
              <FormField id="ep-method" label="Method">
                {(c) => (
                  <Select {...c} value={method} onChange={(e) => setMethod(e.target.value)}>
                    <option value="">All methods</option>
                    {METHODS.map((m) => (
                      <option key={m} value={m}>
                        {m}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
              <FormField id="ep-decision" label="Decision">
                {(c) => (
                  <Select {...c} value={decision} onChange={(e) => setDecision(e.target.value)}>
                    <option value="">Allowed and denied</option>
                    <option value="allowed">Allowed only</option>
                    <option value="denied">Denied only</option>
                  </Select>
                )}
              </FormField>
            </div>
            <p className={styles.note}>{`Showing ${visible.length} of ${report.data.permissions.length} routes.`}</p>
            <DataTable
              label="Route permissions"
              columns={columns}
              rows={visible}
              getRowKey={(p) => `${p.method} ${p.resource}`}
              empty={{ title: "No routes match", description: "Clear the search or filters to see every route." }}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}
