import styles from "./demo.module.css";

/** Fixed notice on every page of the Pages demo build. */
export function DemoBanner() {
  return (
    <div className={styles.banner} role="note">
      <strong>Demo</strong> Sample data for a fictional company. Changes stay in this tab and reset when you reload.
    </div>
  );
}
