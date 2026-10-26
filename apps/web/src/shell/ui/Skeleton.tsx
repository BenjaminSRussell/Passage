import { cx } from "./cx";
import styles from "./Skeleton.module.css";

export interface SkeletonProps {
  width?: "full" | "3/4" | "1/2" | "1/4";
  height?: "text" | "control" | "tile";
}

const widthClass = { full: styles.wFull, "3/4": styles.w34, "1/2": styles.w12, "1/4": styles.w14 };

export function Skeleton({ width = "full", height = "text" }: SkeletonProps) {
  return <span className={cx(styles.skeleton, widthClass[width], styles[height])} aria-hidden="true" />;
}
