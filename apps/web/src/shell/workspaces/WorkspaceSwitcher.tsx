import { copy } from "../copy";
import styles from "../Shell.module.css";
import { Input, Select } from "../ui";
import { useWorkspaceNav } from "./WorkspaceNav";

export function WorkspaceSwitcher() {
  const { available, held, active, setActive, toggleHeld } = useWorkspaceNav();
  if (available.length === 0) {
    return null;
  }
  const heldIds = new Set(held.map((w) => w.id));
  return (
    <div className={styles.switcher}>
      <label htmlFor="workspace-switcher" className={styles.switcherLabel}>
        {copy.workspaces.switcherLabel}
      </label>
      <Select id="workspace-switcher" value={active?.id} onChange={(e) => setActive(e.target.value)}>
        {held.map((w) => (
          <option key={w.id} value={w.id}>
            {w.label}
          </option>
        ))}
      </Select>
      {available.length > 1 ? (
        <details className={styles.holdDetails}>
          <summary>{copy.workspaces.chooseSummary}</summary>
          <fieldset className={styles.holdList}>
            <legend className="visually-hidden">{copy.workspaces.chooseLegend}</legend>
            {available.map((w) => (
              <div key={w.id} className={styles.holdOption}>
                <Input
                  id={`hold-${w.id}`}
                  type="checkbox"
                  checked={heldIds.has(w.id)}
                  disabled={heldIds.has(w.id) && heldIds.size === 1}
                  onChange={() => toggleHeld(w.id)}
                />
                <label htmlFor={`hold-${w.id}`}>{w.label}</label>
              </div>
            ))}
          </fieldset>
        </details>
      ) : null}
    </div>
  );
}
