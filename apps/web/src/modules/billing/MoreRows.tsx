import { Alert, Button } from "../../shell/ui";
import styles from "./pages.module.css";
import type { PagedState } from "./usePaged";

/** The "Load more" control under a paged list, with the error of a failed page. */
export function MoreRows({ state, noun }: { state: PagedState<unknown>; noun: string }) {
  return (
    <>
      {state.moreError ? (
        <Alert tone="danger" title={`Could not load more ${noun}`} live="alert" action={<Button size="sm" onClick={state.loadMore}>Try again</Button>}>
          {state.moreError.message} ({state.moreError.code})
        </Alert>
      ) : null}
      {state.hasMore && !state.moreError ? (
        <div className={styles.more}>
          <Button onClick={state.loadMore} loading={state.loadingMore}>
            {`Load more ${noun}`}
          </Button>
        </div>
      ) : null}
    </>
  );
}
