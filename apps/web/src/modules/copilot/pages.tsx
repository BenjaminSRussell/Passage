import { PageHeader } from "../../shell/ui";
import { CopilotPanel } from "./CopilotPanel";
import styles from "./pages.module.css";

export function CopilotHome() {
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Copilot"
        description="Ask about your data, explain a record, draft text or summarize a workflow. Every answer lists the tools it used and the sources it cites, and runs with your permissions."
      />
      <CopilotPanel />
    </div>
  );
}
