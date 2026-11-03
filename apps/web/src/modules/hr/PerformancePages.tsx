import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { Alert, Button, Card, DataTable, EmptyState, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import {
  getReview,
  listMyGoals,
  listMyReviews,
  listWorkerGoals,
  listWorkerReviews,
  openReview,
  signOffReview,
  submitSelfAssessment,
  updateGoalProgress,
  updateMyGoalProgress,
  type Review,
} from "./api/performance";
import { todayIso } from "./format";
import styles from "./pages.module.css";
import { BackLink, Entry, Facts, FormActions, LoadFailure, Loaded, SelectField, TextAreaField, TextField, labelled, useSubmit } from "./parts";
import { GoalProgressForm, GoalsTable, NewGoalForm, Notice, ReviewStatus, ReviewText, validatePeriod, validateSelfAssessment, validateSignOff } from "./PerformanceParts";
import { useLoad } from "./useLoad";

const period = (r: Review) => `${r.periodStart} to ${r.periodEnd}`;

function SectionError({ title, error, reload }: { title: string; error: { message: string; code: string }; reload(): void }) {
  return (
    <Alert tone="danger" title={title} live="alert" action={<Button size="sm" onClick={reload}>Try again</Button>}>
      {error.message} ({error.code})
    </Alert>
  );
}

/** HCM-ESS-001: the sign-in is not linked to a worker, so there are no own goals or reviews to show. */
const NO_LINKED_WORKER = "HCM-ESS-001";

function NoLinkedWorker() {
  return (
    <Card>
      <EmptyState headingLevel={3} title="No worker record is linked to your sign-in" description="Ask HR to link your sign-in to your worker record." />
    </Card>
  );
}

/** The signed-in worker's goals, with progress they can report themselves. */
export function MyGoalsPage() {
  const state = useLoad((signal) => listMyGoals(signal), "my-goals");
  const [notice, setNotice] = useState<string | null>(null);
  const goals = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader title="My goals" description="Goals your manager set for you. Report your progress here; only your manager can add or cancel a goal." />
      <Notice>{notice}</Notice>
      {state.error ? (
        state.error.code === NO_LINKED_WORKER ? (
          <NoLinkedWorker />
        ) : (
          <SectionError title="Could not load your goals" error={state.error} reload={state.reload} />
        )
      ) : state.loading && !state.data ? (
        <Skeleton width="3/4" />
      ) : (
        <>
          <GoalsTable goals={goals} label="My goals" />
          {goals.some((g) => g.status !== "CANCELLED") ? (
            <Card title="Report progress">
              <GoalProgressForm
                key={goals.map((g) => `${g.id}${g.updatedAt}`).join()}
                goals={goals}
                canCancel={false}
                save={updateMyGoalProgress}
                onDone={(goal) => {
                  setNotice(`Progress on "${goal.title}" is now ${goal.progressPercent}%.`);
                  state.reload();
                }}
              />
            </Card>
          ) : null}
        </>
      )}
    </div>
  );
}

function SelfAssessmentForm({ review, onDone }: { review: Review; onDone(r: Review): void }) {
  const [values, setValues] = useState({ achievements: "", development: "" });
  const [errors, setErrors] = useState<Partial<Record<"achievements" | "development", string>>>({});
  const form = useSubmit();
  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSelfAssessment(values);
    setErrors(found);
    if (input) void form.run(() => submitSelfAssessment(review.id, input), onDone);
  }
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Self-assessment">
      <TextAreaField id={`self-achievements-${review.id}`} label="Achievements" rows={5} value={values.achievements} error={errors.achievements ?? form.errors.achievements} onChange={(achievements) => setValues({ ...values, achievements })} />
      <TextAreaField id={`self-development-${review.id}`} label="Development" optional rows={4} hint="What you want to learn or do next." value={values.development} error={errors.development ?? form.errors.development} onChange={(development) => setValues({ ...values, development })} />
      <p className={styles.note}>Once submitted your self-assessment can't be changed, and your manager is asked to sign off.</p>
      <FormActions busy={form.busy} submit="Submit self-assessment" failure={form.failure} failureTitle="Could not submit the self-assessment" errors={form.errors} />
    </form>
  );
}

function ReviewResult({ review }: { review: Review }) {
  return (
    <>
      <ReviewText title="Your achievements">{review.selfAchievements}</ReviewText>
      <ReviewText title="Your development">{review.selfDevelopment}</ReviewText>
      {review.status === "signed_off" ? (
        <Card title="Manager sign-off">
          <Facts>
            <Entry term="Overall rating" mono>
              {review.overallRating !== undefined ? `${review.overallRating} of 5` : "Not rated"}
            </Entry>
            <Entry term="Signed off">{review.signedOffAt?.slice(0, 10) ?? "–"}</Entry>
          </Facts>
          <p className={styles.note}>{review.managerComments ?? "No comments."}</p>
        </Card>
      ) : null}
    </>
  );
}

/** The signed-in worker's reviews: step one is their own self-assessment. */
export function MyReviewsPage() {
  const state = useLoad((signal) => listMyReviews(signal), "my-reviews");
  const [notice, setNotice] = useState<string | null>(null);
  const reviews = state.data ?? [];
  return (
    <div className={styles.stack}>
      <PageHeader title="My reviews" description="Write your self-assessment when a review is opened for you. Your manager then signs it off." />
      <Notice>{notice}</Notice>
      {state.error ? (
        state.error.code === NO_LINKED_WORKER ? (
          <NoLinkedWorker />
        ) : (
          <SectionError title="Could not load your reviews" error={state.error} reload={state.reload} />
        )
      ) : state.loading && !state.data ? (
        <Skeleton width="3/4" />
      ) : reviews.length === 0 ? (
        <Card>
          <p className={styles.note}>No review has been opened for you yet.</p>
        </Card>
      ) : (
        reviews.map((review) => (
          <Card key={review.id} title={`Review ${period(review)}`} actions={<ReviewStatus review={review} />}>
            <div className={styles.stack}>
              {review.status === "draft" ? (
                <SelfAssessmentForm
                  review={review}
                  onDone={() => {
                    setNotice(`Your self-assessment for ${period(review)} was submitted.`);
                    state.reload();
                  }}
                />
              ) : (
                <ReviewResult review={review} />
              )}
            </div>
          </Card>
        ))
      )}
    </div>
  );
}

function OpenReviewForm({ workerId, onDone, onCancel }: { workerId: string; onDone(r: Review): void; onCancel(): void }) {
  const year = todayIso().slice(0, 4);
  const [values, setValues] = useState({ periodStart: `${year}-01-01`, periodEnd: `${year}-12-31` });
  const [errors, setErrors] = useState<Partial<Record<"periodStart" | "periodEnd", string>>>({});
  const form = useSubmit({ "HCM-ENTITY-MISMATCH": "periodEnd" });
  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePeriod(values);
    setErrors(found);
    if (input) void form.run((key) => openReview(workerId, input, key), onDone);
  }
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Open a review" onChange={form.touch}>
      <TextField id="review-start" label="Period start" type="date" value={values.periodStart} error={errors.periodStart ?? form.errors.periodStart} onChange={(periodStart) => setValues({ ...values, periodStart })} />
      <TextField id="review-end" label="Period end" type="date" value={values.periodEnd} error={errors.periodEnd ?? form.errors.periodEnd} onChange={(periodEnd) => setValues({ ...values, periodEnd })} />
      <FormActions busy={form.busy} submit="Open review" failure={form.failure} failureTitle="Could not open the review" errors={form.errors} />
      <div className={styles.formActions}>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

const reviewColumns: DataTableColumn<Review>[] = [
  { key: "period", header: "Period", mono: true, render: (row) => <Link to={`/hr/reviews/${row.id}`}>{period(row)}</Link> },
  { key: "status", header: "Status", render: (row) => <ReviewStatus review={row} /> },
  { key: "overallRating", header: "Rating", mono: true, align: "end", render: (row) => (row.overallRating !== undefined ? `${row.overallRating} of 5` : "–") },
];

/** A worker's goals and reviews for their manager (or a performance admin). */
export function WorkerPerformancePage() {
  const { id = "" } = useParams();
  const goals = useLoad((signal) => listWorkerGoals(id, signal), `goals|${id}`);
  const reviews = useLoad((signal) => listWorkerReviews(id, signal), `reviews|${id}`);
  const [adding, setAdding] = useState(false);
  const [opening, setOpening] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const back = <BackLink to={`/hr/workers/${id}`}>Back to the worker</BackLink>;

  if (goals.error?.status === 404 || reviews.error?.status === 404) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Goals and reviews" actions={back} />
        <LoadFailure error={(goals.error ?? reviews.error)!} reload={goals.reload} what="worker" backTo="/hr/workers" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title="Goals and reviews" description="Goals you set for this worker, and the two-step review: their self-assessment, then your sign-off." actions={back} />
      <Notice>{notice}</Notice>
      <Card
        title="Goals"
        actions={
          adding ? null : (
            <Button size="sm" onClick={() => setAdding(true)}>
              New goal
            </Button>
          )
        }
      >
        <div className={styles.stack}>
          {adding ? (
            <NewGoalForm
              workerId={id}
              onCancel={() => setAdding(false)}
              onDone={(goal) => {
                setAdding(false);
                setNotice(`Goal "${goal.title}" added.`);
                goals.reload();
              }}
            />
          ) : null}
          {goals.error ? (
            <SectionError title="Could not load the goals" error={goals.error} reload={goals.reload} />
          ) : goals.loading && !goals.data ? (
            <Skeleton width="3/4" />
          ) : (
            <>
              <GoalsTable goals={goals.data ?? []} />
              {(goals.data ?? []).some((g) => g.status !== "CANCELLED") ? (
                <GoalProgressForm
                  key={(goals.data ?? []).map((g) => `${g.id}${g.updatedAt}`).join()}
                  goals={goals.data ?? []}
                  canCancel
                  save={updateGoalProgress}
                  onDone={(goal) => {
                    setNotice(`Goal "${goal.title}" is ${goal.status.toLowerCase()} at ${goal.progressPercent}%.`);
                    goals.reload();
                  }}
                />
              ) : null}
            </>
          )}
        </div>
      </Card>
      <Card
        title="Reviews"
        actions={
          opening ? null : (
            <Button size="sm" onClick={() => setOpening(true)}>
              Open a review
            </Button>
          )
        }
      >
        <div className={styles.stack}>
          {opening ? (
            <OpenReviewForm
              workerId={id}
              onCancel={() => setOpening(false)}
              onDone={(review) => {
                setOpening(false);
                setNotice(`Review for ${period(review)} is open. The worker can now write their self-assessment.`);
                reviews.reload();
              }}
            />
          ) : null}
          {reviews.error ? (
            <SectionError title="Could not load the reviews" error={reviews.error} reload={reviews.reload} />
          ) : (
            <DataTable
              label="Reviews"
              columns={reviewColumns}
              rows={reviews.data ?? []}
              getRowKey={(row) => row.id}
              loading={reviews.loading && !reviews.data}
              empty={{ title: "No reviews yet", description: "Open a review for a period; the worker writes their self-assessment first." }}
            />
          )}
        </div>
      </Card>
    </div>
  );
}

function SignOffForm({ review, onDone }: { review: Review; onDone(r: Review): void }) {
  const [values, setValues] = useState({ comments: "", rating: "" });
  const [errors, setErrors] = useState<Partial<Record<"comments" | "rating", string>>>({});
  const form = useSubmit({ "HCM-REV-SELF": "comments" });
  function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSignOff(values);
    setErrors(found);
    if (input) void form.run(() => signOffReview(review.id, input), onDone);
  }
  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label="Manager sign-off">
      <TextAreaField id="signoff-comments" label="Comments" optional rows={4} value={values.comments} error={errors.comments ?? form.errors.comments ?? form.errors.managerComments} onChange={(comments) => setValues({ ...values, comments })} />
      <SelectField id="signoff-rating" label="Overall rating" optional placeholder="No rating" value={values.rating} error={errors.rating ?? form.errors.overallRating} onChange={(rating) => setValues({ ...values, rating })} options={labelled(["1", "2", "3", "4", "5"])} />
      <FormActions busy={form.busy} submit="Sign off" failure={form.failure} failureTitle="Could not sign off the review" errors={form.errors} />
    </form>
  );
}

/** One review for the manager: the worker's self-assessment and, once submitted, the sign-off step. */
export function ReviewPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getReview(id, signal), `review|${id}`);
  const [notice, setNotice] = useState<string | null>(null);
  return (
    <Loaded state={state} title="Review" what="review" backTo="/hr/workers">
      {(review) => (
        <div className={styles.stack}>
          <PageHeader title={`Review ${period(review)}`} actions={<BackLink to={`/hr/workers/${review.workerId}/performance`}>Back to goals and reviews</BackLink>} />
          <Notice>{notice}</Notice>
          <Card title="Status">
            <ReviewStatus review={review} />
          </Card>
          {review.status === "draft" ? (
            <Alert tone="info" title="Waiting for the self-assessment">
              The worker writes their self-assessment first. You can sign off after they submit it.
            </Alert>
          ) : (
            <>
              <ReviewText title="Achievements">{review.selfAchievements}</ReviewText>
              <ReviewText title="Development">{review.selfDevelopment}</ReviewText>
            </>
          )}
          {review.status === "self_submitted" ? (
            <Card title="Sign off">
              <SignOffForm
                review={review}
                onDone={() => {
                  setNotice("The review is signed off.");
                  state.reload();
                }}
              />
            </Card>
          ) : null}
          {review.status === "signed_off" ? <ReviewResult review={review} /> : null}
        </div>
      )}
    </Loaded>
  );
}
