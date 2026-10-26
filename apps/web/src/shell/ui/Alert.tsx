import type { ReactNode } from "react";
import { cx } from "./cx";
import styles from "./Alert.module.css";
import { Icon, type IconName } from "./Icon";

export type AlertTone = "info" | "success" | "warning" | "danger";

export interface AlertProps {
  tone: AlertTone;
  title?: string;
  children?: ReactNode;
  action?: ReactNode;
  live?: "alert" | "status" | "off";
}

const toneIcon: Record<AlertTone, IconName> = {
  info: "info",
  success: "checkCircle",
  warning: "alertTriangle",
  danger: "xCircle",
};

export function Alert({ tone, title, children, action, live = "off" }: AlertProps) {
  const filled = Boolean(title) || Boolean(children) || Boolean(action);
  return (
    <div className={cx(styles.alert, styles[tone])} role={live === "off" ? undefined : live}>
      {filled ? (
        <>
          <span className={styles.icon}>
            <Icon name={toneIcon[tone]} />
          </span>
          <div className={styles.body}>
            {title ? <p className={styles.title}>{title}</p> : null}
            {children ? <div className={styles.message}>{children}</div> : null}
          </div>
          {action ? <div className={styles.action}>{action}</div> : null}
        </>
      ) : null}
    </div>
  );
}
