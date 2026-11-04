import { useNavigate } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Button, FormField, PageHeader, Select } from "../../shell/ui";
import { createParty, type PartyType } from "./api/client";
import { DUTY_WRITE, partyTypeLabel } from "./format";
import { emptyNames, NameFieldset, pickNames, validateNames, type NameValues } from "./nameFields";
import styles from "./pages.module.css";
import { ProblemAlert } from "./parts";
import { useWriteForm } from "./useWriteForm";

interface Values extends NameValues {
  partyType: PartyType;
}

const initial: Values = { ...emptyNames, partyType: "organization" };

/** Create an organization or person (POST /v1/party/parties), then open it. */
export function NewPartyPage() {
  const canWrite = holdsDuty(usePrincipalDuties(), DUTY_WRITE);
  const navigate = useNavigate();
  const form = useWriteForm<Values>(
    initial,
    (values) => validateNames(values.partyType, values),
    async (values, key) => {
      const created = await createParty({ partyType: values.partyType, ...pickNames(values.partyType, values) }, key);
      navigate(`/party/${created.id}`);
    },
  );

  return (
    <div className={styles.stack}>
      <PageHeader title="New party" description="Create an organization or a person. The party number is assigned by the service." />
      {canWrite ? (
        <form className={styles.form} onSubmit={(event) => void form.submit(event)} noValidate aria-label="Create a party">
          <FormField id="new-party-type" label="Type">
            {(control) => (
              <Select
                {...control}
                value={form.values.partyType}
                onChange={(event) => form.set("partyType", event.target.value as PartyType)}
              >
                {(["organization", "person"] as const).map((type) => (
                  <option key={type} value={type}>
                    {partyTypeLabel[type]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <NameFieldset type={form.values.partyType} idPrefix="new-party" values={form.values} errors={form.errors} onChange={form.set} />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={form.busy}>
              Create party
            </Button>
          </div>
        </form>
      ) : (
        <Alert tone="info" title="Creating a party needs party.write">
          Ask for the party.write duty to create parties.
        </Alert>
      )}
      {form.failure ? <ProblemAlert problem={form.failure} title="Could not create the party" /> : null}
    </div>
  );
}
