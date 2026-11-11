import { useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, Select } from "../../shell/ui";
import { ApiError, deleteResource, updateResource, type Resource } from "./api/client";
import { errorText } from "./screens";
import styles from "./pages.module.css";
import { EntityLookup } from "../../shell/lookup/EntityLookup";

const resourceTypes = ["machine", "tool", "labor", "vendor", "location"];
const forbiddenStatus = 403;
const conflictStatus = 409;

function message(error: unknown): string {
  if (error instanceof ApiError && error.status === forbiddenStatus) return "You do not have permission to change resources.";
  if (error instanceof ApiError && error.code === "ENG-RES-VERSION-CONFLICT") return "Someone else saved this resource first. Reload it and make your change again.";
  if (error instanceof ApiError && error.status === conflictStatus && error.code === "ENG-RES-IN-USE") return error.message;
  return errorText(error);
}

/**
 * Edit and delete controls for a resource (BR-9.11, BR-9.3 style locking). The edit form changes the
 * name, type, efficiency, capacity, exclusivity, vendor, group and calendar and sends the
 * capabilities back as loaded; code and site are the resource's identity. Delete asks to confirm and
 * shows the service's ENG-RES-IN-USE reason when an active route requires the resource.
 */
export function ResourceActions({ resource, onSaved, onDeleted }: { resource: Resource; onSaved: () => void; onDeleted?: () => void }) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [name, setName] = useState(resource.name);
  const [type, setType] = useState(resource.type);
  const [efficiency, setEfficiency] = useState(resource.efficiencyPct ?? "");
  const [capacity, setCapacity] = useState(resource.capacityPerDayH ?? "");
  const [exclusive, setExclusive] = useState(resource.exclusive);
  const [vendor, setVendor] = useState(resource.vendorPartyId ?? "");
  const [groupId, setGroupId] = useState(resource.groupId ?? "");
  const [calendarId, setCalendarId] = useState(resource.calendarId ?? "");
  const attempt = useRef({ body: "", key: "" });

  async function save(event: FormEvent) {
    event.preventDefault();
    const body = {
      ...resource,
      name,
      type,
      exclusive,
      efficiencyPct: efficiency || undefined,
      capacityPerDayH: capacity || undefined,
      vendorPartyId: type === "vendor" ? vendor : undefined,
      groupId: groupId || undefined,
      calendarId: calendarId || undefined,
      version: resource.version ?? 1,
    };
    const text = JSON.stringify(body);
    if (attempt.current.body !== text) attempt.current = { body: text, key: crypto.randomUUID() };
    setBusy(true);
    setError("");
    try {
      await updateResource(resource.id, body, attempt.current.key);
      setEditing(false);
      onSaved();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    setError("");
    try {
      await deleteResource(resource.id);
      onDeleted?.();
    } catch (e) {
      setConfirming(false);
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <div className={styles.toolbar}>
        {!editing ? (
          <Button size="sm" onClick={() => setEditing(true)} disabled={busy}>
            Edit resource
          </Button>
        ) : null}
        {confirming ? (
          <>
            <Button size="sm" onClick={remove} disabled={busy}>
              Confirm delete
            </Button>
            <Button size="sm" onClick={() => setConfirming(false)} disabled={busy}>
              Keep resource
            </Button>
          </>
        ) : (
          <Button size="sm" onClick={() => setConfirming(true)} disabled={busy}>
            Delete resource
          </Button>
        )}
      </div>
      <div className={styles.alertSlot}>
        <Alert tone="danger" live="alert">
          {error || null}
        </Alert>
      </div>
      {editing ? (
        <Card title="Edit resource">
          <form onSubmit={save} className={styles.form}>
            <div className={styles.field}>
              <FormField id="res-name" label="Name">
                {(control) => <Input {...control} value={name} onChange={(e) => setName(e.target.value)} required maxLength={200} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="res-type" label="Type">
                {(control) => (
                  <Select {...control} value={type} onChange={(e) => setType(e.target.value)}>
                    {resourceTypes.map((t) => (
                      <option key={t} value={t}>
                        {t}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="res-efficiency" label="Efficiency %" hint="Above 0 to 100.">
                {(control) => <Input {...control} inputMode="decimal" value={efficiency} onChange={(e) => setEfficiency(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="res-capacity" label="Capacity per day (h)">
                {(control) => <Input {...control} inputMode="decimal" value={capacity} onChange={(e) => setCapacity(e.target.value)} />}
              </FormField>
            </div>
            {type === "vendor" ? (
              <div className={styles.field}>
                <FormField id="res-vendor" label="Vendor party">
                  {(control) => <Input {...control} value={vendor} onChange={(e) => setVendor(e.target.value)} required maxLength={64} />}
                </FormField>
              </div>
            ) : null}
            <div className={styles.field}>
              <FormField id="res-group" label="Resource group" optional hint="A resource group at the same site; leave empty for none.">
                {(control) => <EntityLookup {...control} entity="resourceGroup" value={groupId} onChange={(e) => setGroupId(e.target.value)} />}
              </FormField>
            </div>
            <div className={styles.field}>
              <FormField id="res-calendar" label="Calendar" optional hint="Leave empty to use the group's calendar.">
                {(control) => <EntityLookup {...control} entity="calendar" value={calendarId} onChange={(e) => setCalendarId(e.target.value)} />}
              </FormField>
            </div>
            <span className={styles.check}>
              <Input id="res-exclusive" type="checkbox" checked={exclusive} onChange={(e) => setExclusive(e.target.checked)} />
              <label htmlFor="res-exclusive">Exclusive</label>
            </span>
            <div className={styles.toolbar}>
              <Button type="submit" size="sm" disabled={busy}>
                Save resource
              </Button>
              <Button size="sm" onClick={() => setEditing(false)} disabled={busy}>
                Cancel edit
              </Button>
            </div>
          </form>
        </Card>
      ) : null}
    </>
  );
}
