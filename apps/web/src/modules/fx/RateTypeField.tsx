import { useQuery } from "@tanstack/react-query";
import { FormField, Select } from "../../shell/ui";
import { fetchRateTypes } from "./api/client";

interface Props {
  id: string;
  value: string;
  error?: string;
  onChange(code: string): void;
}

export function RateTypeField({ id, value, error, onChange }: Props) {
  const types = useQuery({ queryKey: ["fx", "rate-types"], queryFn: () => fetchRateTypes() });
  const hint = types.isError ? "Rate types could not be loaded. Reload the page to try again." : undefined;
  return (
    <FormField id={id} label="Rate type" hint={hint} error={error}>
      {(control) => (
        <Select {...control} value={value} onChange={(e) => onChange(e.target.value)} invalid={Boolean(error)}>
          <option value="">{types.isPending ? "Loading rate types…" : "Choose a rate type"}</option>
          {(types.data ?? []).map((t) => (
            <option key={t.code} value={t.code}>{`${t.code} – ${t.name}`}</option>
          ))}
        </Select>
      )}
    </FormField>
  );
}
