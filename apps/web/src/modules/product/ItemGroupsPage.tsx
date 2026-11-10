import { useRef, useState, type FormEvent } from "react";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Button, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { createItemGroup, ProductApiError, type ItemGroup } from "./api/client";
import { DUTY_SETUP } from "./labels";
import { ProductProblem } from "./ProductProblem";
import { useItemGroups } from "./useProduct";

const CODE_PATTERN = /^[A-Z0-9][A-Z0-9._-]{0,19}$/;

const columns: DataTableColumn<ItemGroup>[] = [
  { key: "code", header: "Code", mono: true },
  { key: "name", header: "Name" },
];

/** Item groups tie products to posting and reporting; this is where they are set up. */
export function ItemGroupsPage() {
  const canSetUp = holdsDuty(usePrincipalDuties(), DUTY_SETUP);
  const { groups, failed, loading, reload } = useItemGroups();
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    const cleanCode = code.trim().toUpperCase();
    if (cleanCode === "") found.code = "Enter a code for the item group.";
    else if (!CODE_PATTERN.test(cleanCode)) found.code = "Use capital letters, digits, dots, dashes or underscores, starting with a letter or digit, up to 20 characters.";
    if (name.trim() === "") found.name = "Enter a name for the item group.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      queueMicrotask(() => formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
      return;
    }
    setBusy(true);
    try {
      await createItemGroup({ code: cleanCode, name: name.trim() });
      setCode("");
      setName("");
      reload();
    } catch (error) {
      setFailure(error);
      setErrors(error instanceof ProductApiError ? error.fields : {});
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHeader title="Item groups" description="Group products for posting and reporting. A product is put in a group when it is created or edited." />
      {failed ? (
        <ProductProblem error={null} title="Could not load the item groups" retry={reload} />
      ) : loading ? (
        <Skeleton />
      ) : groups.length === 0 ? (
        <EmptyState title="No item groups yet" description={canSetUp ? "Add the first one below." : "Ask someone who sets up products to add them."} />
      ) : (
        <DataTable label="Item groups" columns={columns} rows={groups} getRowKey={(g) => g.id} />
      )}
      {canSetUp ? (
        <Card title="Add an item group">
          <form ref={formRef} onSubmit={(e) => void submit(e)} noValidate>
            <FormField id="item-group-code" label="Code" error={errors.code}>
              {(c) => <Input {...c} value={code} onChange={(e) => setCode(e.target.value)} maxLength={20} autoComplete="off" />}
            </FormField>
            <FormField id="item-group-name" label="Name" error={errors.name}>
              {(c) => <Input {...c} value={name} onChange={(e) => setName(e.target.value)} maxLength={120} autoComplete="off" />}
            </FormField>
            {failure ? <ProductProblem error={failure} title="Could not add the item group" /> : null}
            <Button type="submit" variant="primary" loading={busy}>
              Add item group
            </Button>
          </form>
        </Card>
      ) : null}
    </>
  );
}
