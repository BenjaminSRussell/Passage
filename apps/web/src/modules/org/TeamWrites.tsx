import { useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select, Textarea } from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  addTeamMember,
  createTeamType,
  endTeamMember,
  getTeam,
  memberKinds,
  memberRoles,
  removeTeamMember,
  updateTeam,
  withFreshRowOnConflict,
  type Team,
  type TeamMember,
} from "./api/structure";
import { dayNumber } from "./hierarchyTree";
import styles from "./pages.module.css";

const typeCodePattern = /^[A-Za-z0-9][A-Za-z0-9_-]{1,19}$/;
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// A real calendar date as YYYY-MM-DD (2026-02-30 is refused, as the service refuses it).
const validDate = (value: string) => dayNumber(value) !== null;

type Errors<K extends string> = Partial<Record<K, string>>;

export interface TeamTypeValues {
  code: string;
  name: string;
  kinds: string;
}

export function validateTeamType({ code, name, kinds }: TeamTypeValues): Errors<"code" | "name" | "kinds"> {
  const errors: Errors<"code" | "name" | "kinds"> = {};
  if (!typeCodePattern.test(code)) {
    errors.code = "Use 2 to 20 letters, digits, - or _.";
  }
  if (name.trim() === "" || name.trim().length > 60) {
    errors.name = "Enter a name of 1 to 60 characters.";
  }
  if (kinds === "") {
    errors.kinds = "Select who can be a member.";
  }
  return errors;
}

export interface MemberValues {
  memberKind: string;
  memberId: string;
  memberRole: string;
  validFrom: string;
  validTo: string;
}

export function validateMember({ memberKind, memberId, validFrom, validTo }: MemberValues): Errors<"memberKind" | "memberId" | "validFrom" | "validTo"> {
  const errors: Errors<"memberKind" | "memberId" | "validFrom" | "validTo"> = {};
  if (memberKind === "") {
    errors.memberKind = "Select user or party.";
  }
  if (!uuidPattern.test(memberId.trim())) {
    errors.memberId = "Enter the member's id (a UUID).";
  }
  if (!validDate(validFrom)) {
    errors.validFrom = "Enter a date as YYYY-MM-DD.";
  }
  if (validTo !== "" && !validDate(validTo)) {
    errors.validTo = "Enter a date as YYYY-MM-DD.";
  } else if (validTo !== "" && validDate(validFrom) && validTo < validFrom) {
    errors.validTo = "The end date cannot be before the start date.";
  }
  return errors;
}

const kindChoices: { value: string; label: string; kinds: string[] }[] = [
  { value: "user", label: "Users", kinds: ["user"] },
  { value: "party", label: "Parties", kinds: ["party"] },
  { value: "both", label: "Users and parties", kinds: ["user", "party"] },
];

function FailureAlert({ title, failure }: { title: string; failure: Problem | null }) {
  return failure ? (
    <Alert tone="danger" title={title} live="alert">
      {failure.message} ({failure.code})
    </Alert>
  ) : null;
}

export function CreateTeamTypeForm({ onCreated }: { onCreated(): void }) {
  const [values, setValues] = useState<TeamTypeValues>({ code: "", name: "", kinds: "" });
  const [errors, setErrors] = useState<Errors<"code" | "name" | "kinds">>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateTeamType(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await createTeamType({
        code: values.code.toUpperCase(),
        name: values.name.trim(),
        memberKinds: kindChoices.find((choice) => choice.value === values.kinds)?.kinds ?? [],
      });
      setValues({ code: "", name: "", kinds: "" });
      onCreated();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-TEAM-TYPE-CODE-DUPLICATE") {
        setErrors({ code: "A team type with this code already exists." });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof TeamTypeValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <Card title="Add team type">
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="team-type-code" label="Team type code" error={errors.code}>
          {(control) => <Input {...control} {...text("code")} maxLength={20} />}
        </FormField>
        <FormField id="team-type-name" label="Team type name" error={errors.name}>
          {(control) => <Input {...control} {...text("name")} maxLength={60} />}
        </FormField>
        <FormField id="team-type-kinds" label="Members can be" error={errors.kinds}>
          {(control) => (
            <Select {...control} {...text("kinds")}>
              <option value="">Select member kinds</option>
              {kindChoices.map((choice) => (
                <option key={choice.value} value={choice.value}>
                  {choice.label}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FailureAlert title="Could not add the team type" failure={failure} />
        <div>
          <Button type="submit" loading={busy}>
            Add team type
          </Button>
        </div>
      </form>
    </Card>
  );
}

export function EditTeamForm({ team, onSaved }: { team: Team; onSaved(next: Team): void }) {
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description ?? "");
  const [effectiveTo, setEffectiveTo] = useState(team.effectiveTo ?? "");
  const [errors, setErrors] = useState<Errors<"name" | "description" | "effectiveTo">>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found: Errors<"name" | "description" | "effectiveTo"> = {};
    if (name.trim() === "" || name.trim().length > 60) {
      found.name = "Enter a name of 1 to 60 characters.";
    }
    if (description.length > 500) {
      found.description = "Use at most 500 characters.";
    }
    if (effectiveTo !== "" && (!validDate(effectiveTo) || effectiveTo < team.effectiveFrom)) {
      found.effectiveTo = "Enter a date as YYYY-MM-DD, not before the team's start.";
    }
    setErrors(found);
    setFailure(null);
    setSaved(false);
    if (Object.keys(found).length > 0) {
      return;
    }
    const patch: { name?: string; description?: string; effectiveTo?: string } = {};
    if (name.trim() !== team.name) {
      patch.name = name.trim();
    }
    if (description.trim() !== (team.description ?? "")) {
      patch.description = description.trim();
    }
    if (effectiveTo !== (team.effectiveTo ?? "")) {
      // An empty value clears the end date (the service treats "" as no effective-to).
      patch.effectiveTo = effectiveTo;
    }
    if (Object.keys(patch).length === 0) {
      setFailure({ code: "ORG-WEB-NO-CHANGE", message: "Nothing to save: change a field first." });
      return;
    }
    setBusy(true);
    try {
      await withFreshRowOnConflict(
        () => updateTeam(team.id, team.version, patch),
        () => getTeam(team.id),
        onSaved,
      );
      setSaved(true);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title={`Edit ${team.code}`}>
      <form onSubmit={submit} noValidate className={styles.form}>
        <FormField id="edit-team-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} value={name} maxLength={60} onChange={(event) => setName(event.target.value)} />}
        </FormField>
        <FormField id="edit-team-description" label="Description" optional error={errors.description}>
          {(control) => <Textarea {...control} value={description} maxLength={500} onChange={(event) => setDescription(event.target.value)} />}
        </FormField>
        <FormField id="edit-team-effective-to" label="Effective to" optional hint="YYYY-MM-DD; ends the team on that date." error={errors.effectiveTo}>
          {(control) => <Input {...control} value={effectiveTo} onChange={(event) => setEffectiveTo(event.target.value)} />}
        </FormField>
        <FailureAlert title="Could not save the team" failure={failure} />
        {saved ? (
          <Alert tone="success" title="Team saved" live="status">
            The team was updated.
          </Alert>
        ) : null}
        <div>
          <Button type="submit" loading={busy}>
            Save team
          </Button>
        </div>
      </form>
    </Card>
  );
}

const today = () => new Date().toISOString().slice(0, 10);

export function AddMemberForm({ team, onAdded }: { team: Team; onAdded(): void }) {
  const initial: MemberValues = { memberKind: "user", memberId: "", memberRole: "member", validFrom: today(), validTo: "" };
  const [values, setValues] = useState<MemberValues>(initial);
  const [errors, setErrors] = useState<Errors<"memberKind" | "memberId" | "validFrom" | "validTo">>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const found = validateMember(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) {
      return;
    }
    setBusy(true);
    try {
      await addTeamMember(team.id, {
        memberKind: values.memberKind,
        memberId: values.memberId.trim().toLowerCase(),
        memberRole: values.memberRole,
        validFrom: values.validFrom,
        validTo: values.validTo === "" ? undefined : values.validTo,
      });
      setValues(initial);
      onAdded();
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ORG-TEAM-MEMBER-DUPLICATE") {
        setErrors({ memberId: "This member is already in the team for an overlapping period." });
      } else {
        setFailure(problem);
      }
    } finally {
      setBusy(false);
    }
  };

  const text = (key: keyof MemberValues) => ({
    value: values[key],
    onChange: (event: { target: { value: string } }) => setValues({ ...values, [key]: event.target.value }),
  });

  return (
    <form onSubmit={submit} noValidate className={styles.form} aria-label={`Add member to ${team.code}`}>
      <FormField id="member-kind" label="Member kind" error={errors.memberKind}>
        {(control) => (
          <Select {...control} {...text("memberKind")}>
            {memberKinds.map((kind) => (
              <option key={kind} value={kind}>
                {kind}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="member-id" label="Member id" hint="The user or party id (a UUID)." error={errors.memberId}>
        {(control) => <Input {...control} {...text("memberId")} />}
      </FormField>
      <FormField id="member-role" label="Role">
        {(control) => (
          <Select {...control} {...text("memberRole")}>
            {memberRoles.map((role) => (
              <option key={role} value={role}>
                {role}
              </option>
            ))}
          </Select>
        )}
      </FormField>
      <FormField id="member-valid-from" label="From" hint="YYYY-MM-DD" error={errors.validFrom}>
        {(control) => <Input {...control} {...text("validFrom")} />}
      </FormField>
      <FormField id="member-valid-to" label="To" optional hint="YYYY-MM-DD" error={errors.validTo}>
        {(control) => <Input {...control} {...text("validTo")} />}
      </FormField>
      <FailureAlert title="Could not add the member" failure={failure} />
      <div>
        <Button type="submit" variant="primary" loading={busy}>
          Add member
        </Button>
      </div>
    </form>
  );
}

type Mode = "idle" | "ending" | "removing";

export function MemberActions({ team, member, onChanged }: { team: Team; member: TeamMember; onChanged(): void }) {
  const [mode, setMode] = useState<Mode>("idle");
  const [validTo, setValidTo] = useState(member.validTo ?? today());
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const subject = `${member.memberKind} ${member.memberId.slice(0, 8)}`;

  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setFailure(null);
    try {
      await action();
      setMode("idle");
      onChanged();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  };

  const confirmEnd = () => {
    if (!validDate(validTo) || validTo < member.validFrom || (member.validTo !== undefined && validTo > member.validTo)) {
      setFailure({
        code: "ORG-WEB-DATE",
        message: member.validTo
          ? `Enter an end date as YYYY-MM-DD from ${member.validFrom} to ${member.validTo}: an end date can only move earlier.`
          : "Enter an end date as YYYY-MM-DD, not before the member's start.",
      });
      return;
    }
    void run(() => endTeamMember(team.id, member.id, validTo));
  };

  return (
    <div className={styles.rowActions}>
      {mode === "idle" ? (
        <>
          <Button size="sm" aria-label={`End membership of ${subject}`} onClick={() => setMode("ending")}>
            {member.validTo ? "End earlier" : "End"}
          </Button>
          {member.source === "rule" ? null : (
            <Button size="sm" variant="danger" aria-label={`Remove ${subject} from ${team.code}`} onClick={() => setMode("removing")}>
              Remove
            </Button>
          )}
        </>
      ) : null}
      {mode === "ending" ? (
        <>
          <Input aria-label={`End date for ${subject}`} value={validTo} onChange={(event) => setValidTo(event.target.value)} />
          <Button size="sm" loading={busy} aria-label={`Confirm end of ${subject}`} onClick={confirmEnd}>
            Confirm
          </Button>
          <Button size="sm" variant="ghost" aria-label={`Cancel end of ${subject}`} onClick={() => setMode("idle")}>
            Cancel
          </Button>
        </>
      ) : null}
      {mode === "removing" ? (
        <>
          <Button size="sm" variant="danger" loading={busy} aria-label={`Confirm removal of ${subject}`} onClick={() => void run(() => removeTeamMember(team.id, member.id))}>
            Confirm
          </Button>
          <Button size="sm" variant="ghost" aria-label={`Cancel removal of ${subject}`} onClick={() => setMode("idle")}>
            Cancel
          </Button>
        </>
      ) : null}
      {failure ? (
        <span role="alert" className={styles.rowError}>
          {failure.message} ({failure.code})
        </span>
      ) : null}
    </div>
  );
}
