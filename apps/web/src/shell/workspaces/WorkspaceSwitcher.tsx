import { copy } from "../copy";
import styles from "../Shell.module.css";
import { Input, Select } from "../ui";
import { useWorkspaceNav } from "./WorkspaceNav";

export interface WorkspaceSwitcherProps {
  announce(message: string): void;
}

export function WorkspaceSwitcher({ announce }: WorkspaceSwitcherProps) {
  const { available, held, active, setActive, toggleHeld } = useWorkspaceNav();
  if (available.length === 0) {
    return null;
  }
  const heldIds = new Set(held.map((w) => w.id));
  const onlyOne = heldIds.size === 1;
  return (
    <div className={styles.switcher}>
      <label htmlFor="workspace-switcher" className={styles.switcherLabel}>
        {copy.workspaces.switcherLabel}
      </label>
      <Select
        id="workspace-switcher"
        value={active?.id}
        onChange={(e) => {
          setActive(e.target.value);
          const chosen = held.find((w) => w.id === e.target.value);
          if (chosen) {
            announce(copy.announce.workspaceSwitched(chosen.label));
          }
        }}
      >
        {held.map((w) => (
          <option key={w.id} value={w.id}>
            {w.label}
          </option>
        ))}
      </Select>
      {available.length > 1 ? (
        <details className={styles.holdDetails}>
          <summary>{copy.workspaces.chooseSummary}</summary>
          <fieldset className={styles.holdList} aria-describedby="hold-help">
            <legend className="visually-hidden">{copy.workspaces.chooseLegend}</legend>
            <p id="hold-help">{copy.workspaces.keepOne}</p>
            {available.map((w) => (
              <div key={w.id} className={styles.holdOption}>
                <Input
                  id={`hold-${w.id}`}
                  type="checkbox"
                  checked={heldIds.has(w.id)}
                  aria-disabled={onlyOne && heldIds.has(w.id) ? true : undefined}
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
