import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, EmptyState, Skeleton } from "../../shell/ui";
import { listResources, type Resource } from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

interface Props {
  /** Why the form needs a resource, shown when there is none. */
  need: string;
  children(resources: Resource[]): ReactNode;
}

/** Loads the tenant's resources for a form, with loading, error and empty states. */
export function ResourceGate({ need, children }: Props) {
  const resources = useLoad((signal) => listResources(signal), "resources");
  if (resources.error) {
    return (
      <Alert tone="danger" title="Could not load resources" live="alert" action={<Button size="sm" onClick={resources.reload}>Try again</Button>}>
        {resources.error.message} ({resources.error.code})
      </Alert>
    );
  }
  if (!resources.data) {
    return (
      <div className={styles.loading} aria-busy="true">
        <span className="visually-hidden">Loading resources…</span>
        <Skeleton height="control" />
        <Skeleton height="control" />
      </div>
    );
  }
  if (resources.data.length === 0) {
    return (
      <EmptyState
        headingLevel={3}
        title="Add a resource first"
        description={need}
        action={
          <Link className={buttonClassName("secondary")} to="/scheduling/resources/new">
            Add a resource
          </Link>
        }
      />
    );
  }
  return <>{children(resources.data)}</>;
}
