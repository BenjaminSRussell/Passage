import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AbsencePlanDetailPage } from "./AbsencePlanDetailPage";
import { AbsencePlansPage } from "./AbsencePlansPage";
import { AbsenceRequestsPage } from "./AbsenceRequestsPage";
import { BenefitPlanDetailPage } from "./BenefitPlanDetailPage";
import { BenefitPlansPage } from "./BenefitPlansPage";
import { CourseDetailPage } from "./CourseDetailPage";
import { CourseSessionPage } from "./CourseSessionPage";
import { CoursesPage } from "./CoursesPage";
import { ExpiringCertificationsPage } from "./ExpiringCertificationsPage";
import { MyAbsencePage } from "./MyAbsencePage";
import { MyProfilePage } from "./MyProfilePage";
import { MyTeamPage } from "./MyTeamPage";
import { HrProfileChangesPage, TeamChangesPage } from "./ProfileChangesPage";
import { NewBenefitPlanPage } from "./NewBenefitPlanPage";
import { NewAbsencePlanPage } from "./NewAbsencePlanPage";
import { NewCoursePage } from "./NewCoursePage";
import { NewPositionPage } from "./NewPositionPage";
import { NewSkillPage } from "./NewSkillPage";
import { NewWorkerPage } from "./NewWorkerPage";
import { PositionDetailPage } from "./PositionDetailPage";
import { PositionsPage } from "./PositionsPage";
import { SkillSearchPage } from "./SkillSearchPage";
import { SkillsPage } from "./SkillsPage";
import { TeamMemberPage } from "./TeamMemberPage";
import { WorkerDetailPage } from "./WorkerDetailPage";
import { WorkersPage } from "./WorkersPage";
import { AnswerQuestionnairePage, MyQuestionnairesPage } from "./MyQuestionnairesPage";
import { CandidatePage } from "./CandidatePage";
import { ChecklistPage, TasksPage, WorkerChecklistsPage } from "./ChecklistsPage";
import { ChecklistTemplatesPage, NewChecklistTemplatePage } from "./ChecklistTemplatesPage";
import { CompCycleDetailPage, CompCyclesPage } from "./CompCyclesPage";
import { CompensationPlansPage, NewCompensationPlanPage } from "./CompensationPlansPage";
import { IncidentDetailPage, IncidentsPage } from "./IncidentsPage";
import { JobDetailPage, JobsPage } from "./JobsPage";
import { OutstandingLoansPage, WorkerLoansPage } from "./LoansPage";
import { NewCompCyclePage } from "./NewCompCyclePage";
import { NewIncidentPage } from "./NewIncidentPage";
import { NewJobPage } from "./NewJobPage";
import { NewPersonnelActionPage } from "./NewPersonnelActionPage";
import { NewQuestionnairePage } from "./NewQuestionnairePage";
import { MyGoalsPage, MyReviewsPage, ReviewPage, WorkerPerformancePage } from "./PerformancePages";
import { OfferPage } from "./OfferPage";
import { OshaPage } from "./OshaPage";
import { PersonnelActionDetailPage, PersonnelActionsPage } from "./PersonnelActionsPage";
import { QuestionnaireDetailPage, QuestionnairesPage } from "./QuestionnairesPage";
import { RequisitionDetailPage } from "./RequisitionDetailPage";
import { NewRequisitionPage, RequisitionsPage } from "./RequisitionsPage";
import { ScheduleResultsPage } from "./ScheduleResultsPage";
import { WorksheetPage } from "./WorksheetPage";

const hr: PassageModule = {
  id: "hr",
  title: "Human resources",
  description:
    "Workers with masked personal data and an audited reveal, effective-dated employments, positions with FTE and reports-to lines, assignments, each worker's position and manager on any date, absence plans, accrual, balances and requests, benefit plans with eligibility simulation, life events and enrollments, and skills: the catalog, worker ratings and certificates, position skill profiles, gap analysis, skill search and expiring certifications, and courses: the catalog, sessions with seats and a waitlist, registrations with approval, and attendance that writes skills and certificates, and performance: goals with progress the worker reports themselves, the two-step review (self-assessment, then manager sign-off) and skill requirements and gap reports per job.",
  routes: [
    { path: "/hr", element: createElement(WorkersPage), handle: { title: "Workers", duty: "hr.worker.inquire" } },
    { path: "/hr/workers", element: createElement(WorkersPage), handle: { title: "Workers" } },
    { path: "/hr/workers/new", element: createElement(NewWorkerPage), handle: { title: "New worker" } },
    { path: "/hr/workers/:id", element: createElement(WorkerDetailPage), handle: { title: "Worker" } },
    { path: "/hr/positions", element: createElement(PositionsPage), handle: { title: "Positions" } },
    { path: "/hr/positions/new", element: createElement(NewPositionPage), handle: { title: "New position" } },
    { path: "/hr/positions/:id", element: createElement(PositionDetailPage), handle: { title: "Position" } },
    { path: "/hr/absence/plans", element: createElement(AbsencePlansPage), handle: { title: "Absence plans" } },
    { path: "/hr/absence/plans/new", element: createElement(NewAbsencePlanPage), handle: { title: "New absence plan" } },
    { path: "/hr/absence/plans/:id", element: createElement(AbsencePlanDetailPage), handle: { title: "Absence plan" } },
    { path: "/hr/absence/requests", element: createElement(AbsenceRequestsPage), handle: { title: "Absence requests" } },
    { path: "/hr/benefits/plans", element: createElement(BenefitPlansPage), handle: { title: "Benefit plans" } },
    { path: "/hr/benefits/plans/new", element: createElement(NewBenefitPlanPage), handle: { title: "New benefit plan" } },
    { path: "/hr/benefits/plans/:id", element: createElement(BenefitPlanDetailPage), handle: { title: "Benefit plan" } },
    { path: "/hr/skills", element: createElement(SkillsPage), handle: { title: "Skills" } },
    { path: "/hr/skills/new", element: createElement(NewSkillPage), handle: { title: "New skill" } },
    { path: "/hr/skills/search", element: createElement(SkillSearchPage), handle: { title: "Find workers by skill" } },
    { path: "/hr/certifications/expiring", element: createElement(ExpiringCertificationsPage), handle: { title: "Expiring certifications" } },
    { path: "/hr/courses", element: createElement(CoursesPage), handle: { title: "Courses" } },
    { path: "/hr/courses/new", element: createElement(NewCoursePage), handle: { title: "New course" } },
    { path: "/hr/courses/sessions/:id", element: createElement(CourseSessionPage), handle: { title: "Course session" } },
    { path: "/hr/courses/:id", element: createElement(CourseDetailPage), handle: { title: "Course" } },
    { path: "/hr/actions", element: createElement(PersonnelActionsPage), handle: { title: "Personnel actions", duty: "hr.action.maintain" } },
    { path: "/hr/actions/new", element: createElement(NewPersonnelActionPage), handle: { title: "Submit personnel action", duty: "hr.action.maintain" } },
    { path: "/hr/actions/:id", element: createElement(PersonnelActionDetailPage), handle: { title: "Personnel action", duty: "hr.action.maintain" } },
    { path: "/hr/jobs", element: createElement(JobsPage), handle: { title: "Jobs", duty: "hr.org.inquire" } },
    { path: "/hr/jobs/new", element: createElement(NewJobPage), handle: { title: "New job", duty: "hr.compensation.admin" } },
    { path: "/hr/jobs/:id", element: createElement(JobDetailPage), handle: { title: "Job", duty: "hr.org.inquire" } },
    { path: "/hr/compensation/plans", element: createElement(CompensationPlansPage), handle: { title: "Compensation plans", duty: "hr.compensation.admin" } },
    { path: "/hr/compensation/plans/new", element: createElement(NewCompensationPlanPage), handle: { title: "New compensation plan", duty: "hr.compensation.admin" } },
    { path: "/hr/compensation/cycles", element: createElement(CompCyclesPage), handle: { title: "Compensation cycles", duty: "hr.compensation.admin" } },
    { path: "/hr/compensation/cycles/new", element: createElement(NewCompCyclePage), handle: { title: "New compensation cycle", duty: "hr.compensation.admin" } },
    { path: "/hr/compensation/cycles/:id", element: createElement(CompCycleDetailPage), handle: { title: "Compensation cycle", duty: "hr.compensation.admin" } },
    { path: "/hr/compensation/cycles/:id/worksheets/:managerId", element: createElement(WorksheetPage), handle: { title: "Manager worksheet", duty: "hr.compensation.admin" } },
    { path: "/hr/requisitions", element: createElement(RequisitionsPage), handle: { title: "Requisitions", duty: "hr.recruiting.maintain" } },
    { path: "/hr/requisitions/new", element: createElement(NewRequisitionPage), handle: { title: "New requisition", duty: "hr.recruiting.maintain" } },
    { path: "/hr/requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Requisition", duty: "hr.recruiting.maintain" } },
    { path: "/hr/candidates/:id", element: createElement(CandidatePage), handle: { title: "Candidate", duty: "hr.recruiting.maintain" } },
    { path: "/hr/offers/:id", element: createElement(OfferPage), handle: { title: "Offer", duty: "hr.recruiting.maintain" } },
    { path: "/hr/incidents", element: createElement(IncidentsPage), handle: { title: "Incident register", duty: "hr.incident.admin" } },
    { path: "/hr/incidents/new", element: createElement(NewIncidentPage), handle: { title: "Report an incident", duty: "hr.incident.report" } },
    { path: "/hr/incidents/osha", element: createElement(OshaPage), handle: { title: "OSHA logs", duty: "hr.incident.admin" } },
    { path: "/hr/incidents/:id", element: createElement(IncidentDetailPage), handle: { title: "Incident", duty: "hr.incident.admin" } },
    { path: "/hr/loaned-items", element: createElement(OutstandingLoansPage), handle: { title: "Outstanding loans", duty: "hr.worker.inquire" } },
    { path: "/hr/workers/:id/loaned-items", element: createElement(WorkerLoansPage), handle: { title: "Loaned items", duty: "hr.worker.inquire" } },
    { path: "/hr/checklist-templates", element: createElement(ChecklistTemplatesPage), handle: { title: "Checklist templates", duty: "hr.setup.admin" } },
    { path: "/hr/checklist-templates/new", element: createElement(NewChecklistTemplatePage), handle: { title: "New checklist template", duty: "hr.setup.admin" } },
    { path: "/hr/workers/:id/checklists", element: createElement(WorkerChecklistsPage), handle: { title: "Worker checklists", duty: "hr.worker.inquire" } },
    { path: "/hr/checklists/:id", element: createElement(ChecklistPage), handle: { title: "Checklist", duty: "hr.worker.inquire" } },
    { path: "/hr/tasks", element: createElement(TasksPage), handle: { title: "Tasks", duty: "hr.worker.inquire" } },
    { path: "/hr/questionnaires", element: createElement(QuestionnairesPage), handle: { title: "Questionnaires", duty: "hr.questionnaire.admin" } },
    { path: "/hr/questionnaires/new", element: createElement(NewQuestionnairePage), handle: { title: "New questionnaire", duty: "hr.questionnaire.admin" } },
    { path: "/hr/questionnaires/schedules/:id/results", element: createElement(ScheduleResultsPage), handle: { title: "Questionnaire results", duty: "hr.questionnaire.admin" } },
    { path: "/hr/questionnaires/:id", element: createElement(QuestionnaireDetailPage), handle: { title: "Questionnaire", duty: "hr.questionnaire.admin" } },
    { path: "/hr/my/questionnaires", element: createElement(MyQuestionnairesPage), handle: { title: "My questionnaires", duty: "hr.questionnaire.self" } },
    { path: "/hr/my/questionnaires/:id", element: createElement(AnswerQuestionnairePage), handle: { title: "Answer questionnaire", duty: "hr.questionnaire.self" } },
    { path: "/hr/my/goals", element: createElement(MyGoalsPage), handle: { title: "My goals", duty: "hr.performance.self" } },
    { path: "/hr/my/reviews", element: createElement(MyReviewsPage), handle: { title: "My reviews", duty: "hr.performance.self" } },
    { path: "/hr/workers/:id/performance", element: createElement(WorkerPerformancePage), handle: { title: "Goals and reviews", duty: "hr.performance.manage" } },
    { path: "/hr/reviews/:id", element: createElement(ReviewPage), handle: { title: "Review", duty: "hr.performance.manage" } },
    { path: "/hr/my/profile", element: createElement(MyProfilePage), handle: { title: "My profile", duty: "hr.profile.self" } },
    { path: "/hr/my/absence", element: createElement(MyAbsencePage), handle: { title: "My absence", duty: "hr.absence.self" } },
    { path: "/hr/my/team", element: createElement(MyTeamPage), handle: { title: "My team", duty: "hr.team.manage" } },
    { path: "/hr/my/team/changes", element: createElement(TeamChangesPage), handle: { title: "Team changes", duty: "hr.team.approve" } },
    { path: "/hr/profile-changes", element: createElement(HrProfileChangesPage), handle: { title: "Profile changes", duty: "hr.worker.maintain" } },
    { path: "/hr/my/team/:id", element: createElement(TeamMemberPage), handle: { title: "Team member", duty: "hr.team.manage" } },
  ],
  nav: [
    { label: "Workers", to: "/hr/workers", duty: "hr.worker.inquire" },
    { label: "Positions", to: "/hr/positions", duty: "hr.org.inquire" },
    { label: "Absence plans", to: "/hr/absence/plans", duty: "hr.absence.admin" },
    { label: "Absence requests", to: "/hr/absence/requests", duty: "hr.absence.self" },
    { label: "Benefit plans", to: "/hr/benefits/plans", duty: "hr.setup.admin" },
    { label: "Skills", to: "/hr/skills", duty: "hr.worker.inquire" },
    { label: "Skill search", to: "/hr/skills/search", duty: "hr.org.inquire" },
    { label: "Expiring certifications", to: "/hr/certifications/expiring", duty: "hr.worker.inquire" },
    { label: "Courses", to: "/hr/courses", duty: "hr.learning.admin" },
    { label: "Personnel actions", to: "/hr/actions", duty: "hr.action.maintain" },
    { label: "Jobs", to: "/hr/jobs", duty: "hr.org.inquire" },
    { label: "Compensation plans", to: "/hr/compensation/plans", duty: "hr.compensation.admin" },
    { label: "Compensation cycles", to: "/hr/compensation/cycles", duty: "hr.compensation.admin" },
    { label: "Requisitions", to: "/hr/requisitions", duty: "hr.recruiting.maintain" },
    { label: "Incidents", to: "/hr/incidents", duty: "hr.incident.admin" },
    { label: "Report an incident", to: "/hr/incidents/new", duty: "hr.incident.report" },
    { label: "Outstanding loans", to: "/hr/loaned-items", duty: "hr.worker.inquire" },
    { label: "Checklist templates", to: "/hr/checklist-templates", duty: "hr.setup.admin" },
    { label: "Tasks", to: "/hr/tasks", duty: "hr.worker.inquire" },
    { label: "Questionnaires", to: "/hr/questionnaires", duty: "hr.questionnaire.admin" },
    { label: "My questionnaires", to: "/hr/my/questionnaires", duty: "hr.questionnaire.self" },
    { label: "My goals", to: "/hr/my/goals", duty: "hr.performance.self" },
    { label: "My reviews", to: "/hr/my/reviews", duty: "hr.performance.self" },
    { label: "My profile", to: "/hr/my/profile", duty: "hr.profile.self" },
    { label: "My absence", to: "/hr/my/absence", duty: "hr.absence.self" },
    { label: "My team", to: "/hr/my/team", duty: "hr.team.manage" },
    { label: "Team changes", to: "/hr/my/team/changes", duty: "hr.team.approve" },
    { label: "Profile changes", to: "/hr/profile-changes", duty: "hr.worker.maintain" },
  ],
};

export default hr;
