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
import { MyProfilePage } from "./MyProfilePage";
import { MyTeamPage } from "./MyTeamPage";
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
    { path: "/hr", element: createElement(WorkersPage), handle: { title: "Workers", duty: "HcmWorkerInquire" } },
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
    { path: "/hr/actions", element: createElement(PersonnelActionsPage), handle: { title: "Personnel actions", duty: "HcmActionMaintain" } },
    { path: "/hr/actions/new", element: createElement(NewPersonnelActionPage), handle: { title: "Submit personnel action", duty: "HcmActionMaintain" } },
    { path: "/hr/actions/:id", element: createElement(PersonnelActionDetailPage), handle: { title: "Personnel action", duty: "HcmActionMaintain" } },
    { path: "/hr/jobs", element: createElement(JobsPage), handle: { title: "Jobs", duty: "HcmOrgInquire" } },
    { path: "/hr/jobs/new", element: createElement(NewJobPage), handle: { title: "New job", duty: "HcmCompAdmin" } },
    { path: "/hr/jobs/:id", element: createElement(JobDetailPage), handle: { title: "Job", duty: "HcmOrgInquire" } },
    { path: "/hr/compensation/plans", element: createElement(CompensationPlansPage), handle: { title: "Compensation plans", duty: "HcmCompAdmin" } },
    { path: "/hr/compensation/plans/new", element: createElement(NewCompensationPlanPage), handle: { title: "New compensation plan", duty: "HcmCompAdmin" } },
    { path: "/hr/compensation/cycles", element: createElement(CompCyclesPage), handle: { title: "Compensation cycles", duty: "HcmCompAdmin" } },
    { path: "/hr/compensation/cycles/new", element: createElement(NewCompCyclePage), handle: { title: "New compensation cycle", duty: "HcmCompAdmin" } },
    { path: "/hr/compensation/cycles/:id", element: createElement(CompCycleDetailPage), handle: { title: "Compensation cycle", duty: "HcmCompAdmin" } },
    { path: "/hr/compensation/cycles/:id/worksheets/:managerId", element: createElement(WorksheetPage), handle: { title: "Manager worksheet", duty: "HcmCompAdmin" } },
    { path: "/hr/requisitions", element: createElement(RequisitionsPage), handle: { title: "Requisitions", duty: "HcmRecruitMaintain" } },
    { path: "/hr/requisitions/new", element: createElement(NewRequisitionPage), handle: { title: "New requisition", duty: "HcmRecruitMaintain" } },
    { path: "/hr/requisitions/:id", element: createElement(RequisitionDetailPage), handle: { title: "Requisition", duty: "HcmRecruitMaintain" } },
    { path: "/hr/candidates/:id", element: createElement(CandidatePage), handle: { title: "Candidate", duty: "HcmRecruitMaintain" } },
    { path: "/hr/offers/:id", element: createElement(OfferPage), handle: { title: "Offer", duty: "HcmRecruitMaintain" } },
    { path: "/hr/incidents", element: createElement(IncidentsPage), handle: { title: "Incident register", duty: "HcmIncidentAdmin" } },
    { path: "/hr/incidents/new", element: createElement(NewIncidentPage), handle: { title: "Report an incident", duty: "HcmIncidentReport" } },
    { path: "/hr/incidents/osha", element: createElement(OshaPage), handle: { title: "OSHA logs", duty: "HcmIncidentAdmin" } },
    { path: "/hr/incidents/:id", element: createElement(IncidentDetailPage), handle: { title: "Incident", duty: "HcmIncidentAdmin" } },
    { path: "/hr/loaned-items", element: createElement(OutstandingLoansPage), handle: { title: "Outstanding loans", duty: "HcmWorkerInquire" } },
    { path: "/hr/workers/:id/loaned-items", element: createElement(WorkerLoansPage), handle: { title: "Loaned items", duty: "HcmWorkerInquire" } },
    { path: "/hr/checklist-templates", element: createElement(ChecklistTemplatesPage), handle: { title: "Checklist templates", duty: "HcmSetupAdmin" } },
    { path: "/hr/checklist-templates/new", element: createElement(NewChecklistTemplatePage), handle: { title: "New checklist template", duty: "HcmSetupAdmin" } },
    { path: "/hr/workers/:id/checklists", element: createElement(WorkerChecklistsPage), handle: { title: "Worker checklists", duty: "HcmWorkerInquire" } },
    { path: "/hr/checklists/:id", element: createElement(ChecklistPage), handle: { title: "Checklist", duty: "HcmWorkerInquire" } },
    { path: "/hr/tasks", element: createElement(TasksPage), handle: { title: "Tasks", duty: "HcmWorkerInquire" } },
    { path: "/hr/questionnaires", element: createElement(QuestionnairesPage), handle: { title: "Questionnaires", duty: "HcmQuestionnaireAdmin" } },
    { path: "/hr/questionnaires/new", element: createElement(NewQuestionnairePage), handle: { title: "New questionnaire", duty: "HcmQuestionnaireAdmin" } },
    { path: "/hr/questionnaires/schedules/:id/results", element: createElement(ScheduleResultsPage), handle: { title: "Questionnaire results", duty: "HcmQuestionnaireAdmin" } },
    { path: "/hr/questionnaires/:id", element: createElement(QuestionnaireDetailPage), handle: { title: "Questionnaire", duty: "HcmQuestionnaireAdmin" } },
    { path: "/hr/my/questionnaires", element: createElement(MyQuestionnairesPage), handle: { title: "My questionnaires", duty: "EssQuestionnaire" } },
    { path: "/hr/my/questionnaires/:id", element: createElement(AnswerQuestionnairePage), handle: { title: "Answer questionnaire", duty: "EssQuestionnaire" } },
    { path: "/hr/my/goals", element: createElement(MyGoalsPage), handle: { title: "My goals", duty: "hr.performance.self" } },
    { path: "/hr/my/reviews", element: createElement(MyReviewsPage), handle: { title: "My reviews", duty: "hr.performance.self" } },
    { path: "/hr/workers/:id/performance", element: createElement(WorkerPerformancePage), handle: { title: "Goals and reviews", duty: "hr.performance.manage" } },
    { path: "/hr/reviews/:id", element: createElement(ReviewPage), handle: { title: "Review", duty: "hr.performance.manage" } },
    { path: "/hr/my/profile", element: createElement(MyProfilePage), handle: { title: "My profile", duty: "EssBase" } },
    { path: "/hr/my/team", element: createElement(MyTeamPage), handle: { title: "My team", duty: "MssBase" } },
    { path: "/hr/my/team/:id", element: createElement(TeamMemberPage), handle: { title: "Team member", duty: "MssBase" } },
  ],
  nav: [
    { label: "Workers", to: "/hr/workers", duty: "HcmWorkerInquire" },
    { label: "Positions", to: "/hr/positions", duty: "HcmOrgInquire" },
    { label: "Absence plans", to: "/hr/absence/plans", duty: "HcmAbsenceAdmin" },
    { label: "Absence requests", to: "/hr/absence/requests", duty: "EssAbsence" },
    { label: "Benefit plans", to: "/hr/benefits/plans", duty: "HcmSetupAdmin" },
    { label: "Skills", to: "/hr/skills", duty: "HcmWorkerInquire" },
    { label: "Skill search", to: "/hr/skills/search", duty: "HcmOrgInquire" },
    { label: "Expiring certifications", to: "/hr/certifications/expiring", duty: "HcmWorkerInquire" },
    { label: "Courses", to: "/hr/courses", duty: "HcmLearningAdmin" },
    { label: "Personnel actions", to: "/hr/actions", duty: "HcmActionMaintain" },
    { label: "Jobs", to: "/hr/jobs", duty: "HcmOrgInquire" },
    { label: "Compensation plans", to: "/hr/compensation/plans", duty: "HcmCompAdmin" },
    { label: "Compensation cycles", to: "/hr/compensation/cycles", duty: "HcmCompAdmin" },
    { label: "Requisitions", to: "/hr/requisitions", duty: "HcmRecruitMaintain" },
    { label: "Incidents", to: "/hr/incidents", duty: "HcmIncidentAdmin" },
    { label: "Report an incident", to: "/hr/incidents/new", duty: "HcmIncidentReport" },
    { label: "Outstanding loans", to: "/hr/loaned-items", duty: "HcmWorkerInquire" },
    { label: "Checklist templates", to: "/hr/checklist-templates", duty: "HcmSetupAdmin" },
    { label: "Tasks", to: "/hr/tasks", duty: "HcmWorkerInquire" },
    { label: "Questionnaires", to: "/hr/questionnaires", duty: "HcmQuestionnaireAdmin" },
    { label: "My questionnaires", to: "/hr/my/questionnaires", duty: "EssQuestionnaire" },
    { label: "My goals", to: "/hr/my/goals", duty: "hr.performance.self" },
    { label: "My reviews", to: "/hr/my/reviews", duty: "hr.performance.self" },
    { label: "My profile", to: "/hr/my/profile", duty: "EssBase" },
    { label: "My team", to: "/hr/my/team", duty: "MssBase" },
  ],
};

export default hr;
