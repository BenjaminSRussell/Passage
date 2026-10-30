import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { usePrincipalDuties } from "../../../shell/principal";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, StatusPill, type DataTableColumn } from "../../../shell/ui";
import { createLegalEntity, listLegalEntities, type LegalEntity } from "./api";
import styles from "./security.module.css";
import { ApiFailure, canRetry, fieldsOwnFailure, mapServerFields, useFocusFirstInvalid, withoutFields, type Errors } from "./support";

const DUTY = "Maintain legal entities";
const FIELDS = ["code", "displayName"] as const;
const CODE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/;
const NAME_MAX = 200;

const columns: DataTableColumn<LegalEntity>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "displayName", header: "Name" },
  { key: "status", header: "Status", render: (le) => <StatusPill status={le.status} /> },
  { key: "createdAt", header: "Created", mono: true, render: (le) => <time dateTime={le.createdAt}>{le.createdAt}</time> },
];

function validate(values: { code: string; displayName: string }): Errors {
  const errors: Errors = {};
  if (!CODE.test(values.code)) errors.code = "Use up to 32 letters, digits, hyphens or underscores, starting with a letter or digit.";
  if (values.displayName.trim() === "") errors.displayName = "Enter a name.";
  else if (values.displayName.length > NAME_MAX) errors.displayName = `Keep the name to ${NAME_MAX} characters or fewer.`;
  return errors;
}

export function LegalEntitiesPage() {
  const queryClient = useQueryClient();
  const canCreate = usePrincipalDuties().includes(DUTY);
  const form = useRef<HTMLFormElement>(null);
  const [values, setValues] = useState({ code: "", displayName: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [saved, setSaved] = useState<string | null>(null);
  const entities = useInfiniteQuery({
    queryKey: ["admin", "legal-entities"],
    queryFn: ({ pageParam }) => listLegalEntities(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => page.next,
    retry: false,
  });
  const create = useMutation({
    mutationFn: (v: { code: string; displayName: string }) => createLegalEntity({ code: v.code, displayName: v.displayName.trim() }),
    onSuccess: async (stored) => {
      setSaved(`Legal entity ${stored.code} created`);
      setValues({ code: "", displayName: "" });
      await queryClient.invalidateQueries({ queryKey: ["admin", "legal-entities"] });
    },
    onError: (error) => setErrors(mapServerFields(error, FIELDS)),
  });
  useFocusFirstInvalid(form, errors);

  function change(next: Partial<typeof values>) {
    setValues({ ...values, ...next });
    setErrors(withoutFields(errors, Object.keys(next)));
    setSaved(null);
    create.reset();
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = { ...values, code: values.code.trim() };
    const found = validate(trimmed);
    setErrors(found);
    if (Object.keys(found).length === 0) create.mutate(trimmed);
  }

  const rows = entities.data?.pages.flatMap((page) => page.items) ?? [];
  const createFailure = create.isError && !fieldsOwnFailure(create.error, FIELDS);

  return (
    <div className={styles.stack}>
      <PageHeader title="Legal entities" description="The legal entities in your tenant. Roles, ledgers and documents are scoped to them." />
      {canCreate ? (
        <Card title="New legal entity">
          <form className={styles.form} onSubmit={submit} noValidate aria-label="New legal entity" ref={form}>
            <fieldset className={styles.fields} disabled={create.isPending}>
              <FormField id="le-code" label="Code" hint="A short unique code, for example DE01." error={errors.code}>
                {(c) => <Input {...c} value={values.code} maxLength={32} onChange={(e) => change({ code: e.target.value })} invalid={Boolean(errors.code)} autoComplete="off" />}
              </FormField>
              <FormField id="le-name" label="Name" error={errors.displayName}>
                {(c) => <Input {...c} value={values.displayName} onChange={(e) => change({ displayName: e.target.value })} invalid={Boolean(errors.displayName)} />}
              </FormField>
            </fieldset>
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={create.isPending}>
                Create legal entity
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
      <Alert tone="success" live="status" title={saved ?? undefined} />
      {createFailure ? <ApiFailure error={create.error} title="Could not create the legal entity" duty={DUTY} /> : null}
      {entities.isError ? (
        <ApiFailure
          error={entities.error}
          title="Could not load the legal entities"
          duty={DUTY}
          action={
            canRetry(entities.error) ? (
              <Button variant="secondary" loading={entities.isFetching} onClick={() => void entities.refetch()}>
                Try again
              </Button>
            ) : undefined
          }
        />
      ) : (
        <>
          <DataTable
            label="Legal entities"
            columns={columns}
            rows={rows}
            getRowKey={(le) => le.legalEntityId}
            loading={entities.isPending}
            empty={{ title: "No legal entities yet", description: "Legal entities you create appear here." }}
          />
          {entities.hasNextPage ? (
            <div className={styles.formActions}>
              <Button variant="secondary" loading={entities.isFetchingNextPage} onClick={() => void entities.fetchNextPage()}>
                Load more
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
