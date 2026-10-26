import { forwardRef, type SelectHTMLAttributes } from "react";
import styles from "./Control.module.css";
import { cx } from "./cx";

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
}

export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select({ invalid, className, ...rest }, ref) {
  return (
    <select
      {...rest}
      ref={ref}
      aria-invalid={invalid || rest["aria-invalid"]}
      className={cx(styles.control, styles.select, invalid && styles.invalid, className)}
    />
  );
});
