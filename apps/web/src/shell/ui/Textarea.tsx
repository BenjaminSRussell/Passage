import { forwardRef, type TextareaHTMLAttributes } from "react";
import styles from "./Control.module.css";
import { cx } from "./cx";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
  mono?: boolean;
}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { invalid, mono, className, ...rest },
  ref,
) {
  return (
    <textarea
      {...rest}
      ref={ref}
      aria-invalid={invalid || rest["aria-invalid"]}
      className={cx(styles.control, styles.textarea, mono && styles.mono, invalid && styles.invalid, className)}
    />
  );
});
