import { forwardRef, type InputHTMLAttributes } from "react";
import styles from "./Control.module.css";
import { cx } from "./cx";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean;
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ invalid, className, type, ...rest }, ref) {
  const checkbox = type === "checkbox";
  return (
    <input
      {...rest}
      ref={ref}
      type={type}
      aria-invalid={invalid || rest["aria-invalid"]}
      className={cx(checkbox ? styles.checkbox : styles.control, invalid && styles.invalid, className)}
    />
  );
});
