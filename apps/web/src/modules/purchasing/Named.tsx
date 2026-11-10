import { EntityName } from "../../shell/lookup/EntityName";
import type { LookupEntityKey } from "../../shell/lookup/entities";
import styles from "./Named.module.css";

/** The name (and number) behind an id; a dash when there is no id. Never place inside a link or button. */
export function Named({ entity, id, fallback }: { entity: LookupEntityKey; id?: string; fallback?: string }) {
  if (!id) return <>{"—"}</>;
  return (
    <span className={styles.named}>
      <EntityName entity={entity} id={id} fallback={fallback} />
    </span>
  );
}
