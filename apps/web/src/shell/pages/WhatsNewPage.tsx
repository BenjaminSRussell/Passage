import whatsNewSource from "../../../../../docs/user/whats-new.md?raw";
import { copy } from "../copy";
import { parseWhatsNew } from "../help/whatsNew";
import { Card, EmptyState, PageHeader } from "../ui";

const releases = parseWhatsNew(whatsNewSource);

export function WhatsNewPage() {
  return (
    <>
      <PageHeader title={copy.whatsNew.pageTitle} description={copy.whatsNew.description} />
      {releases.length === 0 ? (
        <Card>
          <EmptyState icon="info" headingLevel={2} title={copy.whatsNew.empty} />
        </Card>
      ) : (
        releases.map((release) => (
          <Card key={release.date}>
            <h3>
              <time dateTime={release.date}>{release.date}</time>
            </h3>
            <ul>
              {release.items.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          </Card>
        ))
      )}
    </>
  );
}
