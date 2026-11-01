import { Alert, Button } from "../../shell/ui";
import type { Paged } from "./usePaged";

/** The "Load more" button and the error of a failed next page, under a paged list. */
export function LoadMore({ paged, noun }: { paged: Paged<unknown>; noun: string }) {
  return (
    <>
      {paged.moreError ? (
        <Alert tone="danger" title={`Could not load more ${noun}`} live="alert">
          {paged.moreError.message} ({paged.moreError.code})
        </Alert>
      ) : null}
      {paged.hasMore ? (
        <div>
          <Button onClick={paged.loadMore} disabled={paged.loadingMore}>
            {paged.loadingMore ? "Loading" : "Load more"}
          </Button>
        </div>
      ) : null}
    </>
  );
}
