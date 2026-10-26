import { Link } from "react-router-dom";
import { copy } from "../copy";
import { buttonClassName } from "./Button";
import { Card } from "./Card";
import { EmptyState } from "./EmptyState";
import type { IconName } from "./Icon";
import { PageHeader } from "./PageHeader";
import { Badge } from "./StatusPill";

export interface WorkspacePlaceholderProps {
  title: string;
  description: string;
  icon: IconName;
}

export function WorkspacePlaceholder({ title, description, icon }: WorkspacePlaceholderProps) {
  return (
    <>
      <PageHeader title={title} meta={<Badge tone="neutral">{copy.placeholder.badge}</Badge>} />
      <Card>
        <EmptyState
          icon={icon}
          title={copy.placeholder.title}
          description={description}
          action={
            <Link to="/" className={buttonClassName("secondary")}>
              {copy.buttons.browseWorkspaces}
            </Link>
          }
        />
      </Card>
    </>
  );
}
