import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, type DataTableColumn } from "../../shell/ui";
import { createVendorGroup, listVendorGroups, PartyApiError, type VendorGroup } from "./api/client";
import styles from "../../shell/pageLayout.module.css";
import { ProblemMessage } from "../../shell/errors";

const CODE = /^[A-Z0-9_-]{1,10}$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface VendorGroupDraft {
  code: string;
  name: string;
  /** Optional: the id vendors already carry for this group (loaded before groups had codes). */
  id: string;
}

const emptyDraft: VendorGroupDraft = { code: "", name: "", id: "" };

/** Client checks that mirror POST /vendor-groups; the service has the last word. */
export function validateVendorGroup(draft: VendorGroupDraft): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!CODE.test(draft.code.trim().toUpperCase())) errors.code = "Use 1 to 10 characters: letters, digits, dash or underscore.";
  const name = draft.name.trim();
  if (name === "" || name.length > 100) errors.name = "Enter a name of up to 100 characters.";
  const id = draft.id.trim();
  if (id !== "" && !UUID.test(id)) errors.id = "Enter the group ID as a UUID, or leave it empty.";
  return errors;
}

const columns: DataTableColumn<VendorGroup>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
  { key: "id", header: "Group ID", mono: true },
];

/** Vendor groups and their short codes, which receiving posts as the vendor group ledger dimension. */
export function VendorGroupsPage() {
  const client = useQueryClient();
  const [draft, setDraft] = useState<VendorGroupDraft>(emptyDraft);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<unknown>(null);
  const key = useRef(crypto.randomUUID());

  const groups = useQuery({ queryKey: ["party", "vendor-groups"], queryFn: () => listVendorGroups() });
  const create = useMutation({
    mutationFn: (input: VendorGroupDraft) =>
      createVendorGroup({ ...(input.id.trim() === "" ? {} : { id: input.id.trim() }), code: input.code.trim().toUpperCase(), name: input.name.trim() }, key.current),
    onSuccess: () => {
      key.current = crypto.randomUUID();
      setDraft(emptyDraft);
      setFailure(null);
      void client.invalidateQueries({ queryKey: ["party", "vendor-groups"] });
    },
    onError: (error) => {
      if (error instanceof PartyApiError && Object.keys(error.fields).length > 0) setErrors(error.fields);
      setFailure(error);
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateVendorGroup(draft);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length === 0) create.mutate(draft);
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Vendor groups" description="The code of a vendor group is posted on the vendor-control account, so accounts payable can be compared with the ledger per group. A code cannot be changed once it is used." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New vendor group">
          <FormField id="vendor-group-code" label="Code" hint="Up to 10 letters, digits, dash or underscore." error={errors.code}>
            {(c) => <Input {...c} className={styles.mono} value={draft.code} onChange={(e) => setDraft({ ...draft, code: e.target.value })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="vendor-group-name" label="Name" error={errors.name}>
            {(c) => <Input {...c} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="vendor-group-id" label="Existing group UUID" hint="Optional. The vendor group ID vendors already carry, to give that group a code; leave empty for a new group." error={errors.id}>
            {(c) => <Input {...c} className={styles.mono} value={draft.id} onChange={(e) => setDraft({ ...draft, id: e.target.value })} invalid={Boolean(errors.id)} />}
          </FormField>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not add the vendor group" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={create.isPending}>
              Add vendor group
            </Button>
          </div>
        </form>
      </Card>
      {groups.isError ? (
        <Alert tone="danger" title="Could not load the vendor groups" live="alert" action={<Button size="sm" onClick={() => void groups.refetch()}>Try again</Button>}>
          {(groups.error as Error).message}
        </Alert>
      ) : (
        <DataTable
          label="Vendor groups"
          columns={columns}
          rows={groups.data ?? []}
          getRowKey={(g) => g.id}
          loading={groups.isPending}
          empty={{ title: "No vendor groups", description: "Add a group with a short code. Vendors that name an unregistered group have no code and post without it." }}
        />
      )}
    </div>
  );
}
