import { useId } from "react";
import { ALL } from "./api/client";
import { useLegalEntity } from "./legalEntityContext";
import { Select } from "../../shell/ui";
import styles from "./pages.module.css";

export function LegalEntitySwitcher() {
  const { entities, scope, setScope, canAll, loading } = useLegalEntity();
  const id = useId();
  return (
    <div className={styles.switcher}>
      <label htmlFor={id} className={styles.switcherLabel}>
        Legal entity
      </label>
      <Select id={id} value={scope} disabled={loading || (entities.length === 0 && !canAll)} onChange={(e) => setScope(e.target.value)}>
        {scope === "" ? <option value="">{loading ? "Loading…" : "No legal entities"}</option> : null}
        {entities.map((entity) => (
          <option key={entity.id} value={entity.id}>
            {entity.code} - {entity.name}
          </option>
        ))}
        {canAll ? <option value={ALL}>All entities (read-only)</option> : null}
      </Select>
    </div>
  );
}
