export type SemanticState = 'backlog' | 'ready' | 'active' | 'blocked' | 'completed';
export type Priority = 'low' | 'normal' | 'high' | 'urgent';
export type TaskKind = 'task' | 'bug';
export type BugSeverity = 's1' | 's2' | 's3' | 's4';
export type BugResolution = 'fixed' | 'duplicate' | 'not_planned' | 'cannot_reproduce' | 'works_as_designed';
export type ActorKind = 'human' | 'agent';
export type ChecklistCompletionPolicy = 'warn' | 'require';
export type Scope = 'projects:read' | 'projects:write' | 'tasks:read' | 'tasks:write' | 'tasks:claim' | 'events:read';

/** States published by an agent while it is actively working a task. */
export type AgentWorkState = 'working' | 'waiting' | 'verifying' | 'handoff';
/** Published states plus server-derived collection filters. */
export type AgentWorkStateFilter = AgentWorkState | 'stale' | 'missing';

/** Stable ordering terms accepted by global search and saved views. */
export interface SearchSort {
  field: 'updated_at' | 'created_at' | 'due_at' | 'title' | 'key' | 'priority' | 'state' | 'position' | string;
  direction: 'asc' | 'desc';
}

export interface SavedView {
  id: string;
  owner_id: string;
  name: string;
  description?: string;
  filters: Record<string, unknown>;
  sort: SearchSort[];
  shared: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface SearchResponse {
  data: Task[];
  next_cursor?: string | null;
  projects?: Project[];
  views?: SavedView[];
}

/**
 * The server's current, structured progress for an agent-owned task.
 *
 * `stale` and `action_needed` are response-time conveniences supplied by the
 * API. Consumers should still derive them from `updated_at` when rendering a
 * long-lived response so the displayed state does not age silently.
 */
export interface AgentWork {
  operation_id: string;
  actor_id: string;
  state: AgentWorkState;
  phase?: string;
  summary: string;
  next_action?: string;
  checkpoint_refs: string[];
  checkpoint_completed?: number;
  checkpoint_total?: number;
  started_at: string;
  updated_at: string;
  stale: boolean;
  action_needed: boolean;
}

/** Fields accepted when an agent publishes or refreshes task work. */
export interface AgentWorkInput {
  operation_id: string;
  state: AgentWorkState;
  phase?: string;
  summary: string;
  next_action?: string;
  checkpoint_refs?: string[];
  checkpoint_completed?: number;
  checkpoint_total?: number;
}

/** More explicit alias for callers that prefer the endpoint's action name. */
export type PublishAgentWorkInput = AgentWorkInput;

export interface Actor {
  id: string;
  kind: ActorKind;
  name: string;
  /** Present for human actors; agent email is unsupported and omitted. */
  email?: string;
  description?: string;
  admin?: boolean;
  disabled_at?: string;
  project_ids?: string[];
  tokens?: ApiToken[];
  created_at?: string;
  updated_at?: string;
}

export interface CodexAccountStatus {
  connected: boolean;
  account_type?: string;
  email?: string;
  plan_type?: string;
  requires_openai_auth: boolean;
}

export interface CodexDeviceLogin {
  login_id: string;
  verification_url: string;
  user_code: string;
}

export interface TaskDraftSuggestion {
  title: string;
  description: string;
  acceptance_criteria: string[];
  priority: Priority;
  rationale: string;
  supporting_task_keys: string[];
}

/** Small actor shape used by read-only coordination surfaces. */
export type ActorSummary = Pick<Actor, 'id' | 'kind' | 'name'>;

export interface Project {
  id: string;
  key: string;
  slug: string;
  name: string;
  description: string;
  color: string;
  favorite: boolean;
  archived_at?: string;
  checklist_completion_policy?: ChecklistCompletionPolicy;
  created_at?: string;
  updated_at?: string;
  task_count?: number;
  completed_task_count?: number;
  completed_count?: number;
  version?: number;
}

/** Runtime task JSON uses actor IDs; the partial shape keeps old board fixtures compatible. */
export type TaskActorReference = string | Pick<Actor, 'id' | 'kind' | 'name'>;

export interface Column {
  id: string;
  project_id: string;
  name: string;
  semantic_state: SemanticState;
  position: number;
  archived_at?: string;
  version?: number;
  /** Incremented whenever a card order in this column changes. */
  ordering_version?: number;
  created_at?: string;
  updated_at?: string;
}

export interface Label {
  id: string;
  project_id: string;
  name: string;
  color?: string;
  created_at?: string;
  updated_at?: string;
}

/** Bug metadata is nested under a task in the runtime API response. */
export interface BugDetails {
  reporter_id: string;
  severity?: BugSeverity | null;
  actual_behavior: string;
  expected_behavior: string;
  reproduction_steps: string;
  environment: string;
  affected_version: string;
  resolution?: BugResolution | null;
  resolved_by?: string | null;
  resolved_at?: string | null;
  duplicate_of?: string | null;
}

/** Fields accepted when creating or patching nested bug metadata. */
export type BugInput = Partial<Pick<BugDetails, 'severity' | 'expected_behavior' | 'reproduction_steps' | 'environment' | 'affected_version'>> & {
  actual_behavior: string;
};
export type BugPatch = Partial<Pick<BugDetails, 'severity' | 'actual_behavior' | 'expected_behavior' | 'reproduction_steps' | 'environment' | 'affected_version'>>;

export interface TriageInput {
  severity: BugSeverity;
  priority?: Priority;
  assignee?: string | null;
  column_id?: string | null;
}

export interface ResolveInput {
  resolution: BugResolution;
  duplicate_of?: string | null;
  note?: string;
}

export interface ReopenInput {
  reason: string;
}

/** Derived direct-edge counts embedded in task collections and detail reads. */
export interface DependencySummary {
  prerequisite_count: number;
  unmet_prerequisite_count: number;
  dependent_count: number;
  blocked: boolean;
}

/** One bounded task reference returned by the dependency graph endpoint. */
export interface TaskReference {
  id: string;
  key: string;
  title: string;
  completed_at: string | null;
  satisfied: boolean;
}

/** Direct dependency edges in both directions for one task. */
export interface TaskDependencies {
  prerequisites: TaskReference[];
  dependents: TaskReference[];
}

export interface TaskChecklistItem {
  id: string;
  task_id: string;
  text: string;
  position: number;
  completed: boolean;
  completed_at?: string | null;
  completed_by?: string | null;
  created_at?: string;
  updated_at?: string;
}

export interface TaskChecklistSummary {
  total: number;
  completed: number;
  open: number;
  percent: number;
  completion_policy: ChecklistCompletionPolicy;
  warning: boolean;
}

export interface TaskChecklistCollection {
  task_id: string;
  version: number;
  data: TaskChecklistItem[];
  summary: TaskChecklistSummary;
}

export interface TaskChecklistItemInput {
  text: string;
  completed?: boolean;
  position?: number;
}

export interface TaskChecklistItemPatch {
  text?: string;
  completed?: boolean;
  position?: number;
}

/** A bounded parent/child relation reference returned by hierarchy reads. */
export interface TaskHierarchyReference {
  id: string;
  number: number;
  key: string;
  project_id: string;
  title: string;
  kind?: TaskKind;
  column_id: string;
  semantic_state: SemanticState | string;
  state?: SemanticState | string;
  version: number;
  parent_id?: string | null;
  completed_at?: string | null;
  agent_work?: AgentWork | null;
}

/** Server-derived direct-child rollups. */
export interface HierarchySummary {
  child_count: number;
  completed_child_count: number;
  completion_percent: number;
  state_counts: Record<string, number>;
  blocked_child_count: number;
  live_agent_work_count: number;
  action_needed_count: number;
  stale_agent_work_count: number;
}

export interface TaskHierarchy {
  parent?: TaskHierarchyReference | null;
  children: TaskHierarchyReference[];
  ancestors: TaskHierarchyReference[];
  descendants: TaskHierarchyReference[];
  summary: HierarchySummary;
}

export interface Task {
  id: string;
  number: number;
  key: string;
  project_id: string;
  column_id: string;
  title: string;
  description?: string;
  /** Existing task responses may omit kind while older servers are upgraded. */
  kind?: TaskKind;
  bug?: BugDetails | null;
  priority: Priority;
  position: number;
  /** Runtime task JSON uses actor IDs and omits unset references. */
  assignee?: TaskActorReference;
  claimed_by?: TaskActorReference;
  claim_expires_at?: string;
  due_at?: string;
  version: number;
  created_at?: string;
  updated_at?: string;
  completed_at?: string;
  labels?: Label[];
  comment_count?: number;
  /** Structured progress published by the current agent, when present. */
  agent_work?: AgentWork | null;
  /** Present on current servers; optional keeps retained-server fixtures usable. */
  dependency_summary?: DependencySummary;
  /** Ordered acceptance criteria; omitted by retained-server fixtures. */
  checklist?: TaskChecklistItem[];
  checklist_summary?: TaskChecklistSummary;
  parent_task_id?: string | null;
  parent_id?: string | null;
  parent?: TaskHierarchyReference | null;
  hierarchy_summary?: HierarchySummary;
}

export interface Comment {
  id: string;
  task_id: string;
  actor_id: string;
  body: string;
  /** Optimistic-concurrency version used by edit/delete mutations. */
  version?: number;
  created_at: string;
  updated_at: string;
  /** Tombstones are retained by the server but omitted from active reads. */
  deleted_at?: string;
  /** @deprecated The runtime response has no author object. */
  author?: Pick<Actor, 'name'> | string;
  /** @deprecated The runtime response has no actor object. */
  actor?: Pick<Actor, 'name'> | string;
}

export interface ActivityEvent {
  cursor: number;
  id: string;
  type: string;
  project_id?: string;
  task_id?: string;
  actor_id?: string;
  payload?: Record<string, unknown>;
  created_at: string;
  /** @deprecated These aliases are not emitted by the runtime event JSON. */
  kind?: never;
  action?: never;
  message?: never;
  actor?: Pick<Actor, 'name'> | string;
  metadata?: never;
}

/** The discriminated payload kinds returned by the durable task timeline. */
export type TaskTimelineKind = 'agent_progress' | 'comment' | 'task_change';
export type TaskTimelineFilter = 'all' | TaskTimelineKind;

export interface TimelineActor {
  id: string;
  kind: ActorKind;
  name: string;
}

export interface TaskTimelineProgress {
  operation_id: string;
  actor_id: string;
  state: AgentWorkState;
  phase: string;
  summary: string;
  next_action: string;
  checkpoint_refs: string[];
  checkpoint_completed?: number | null;
  checkpoint_total?: number | null;
  started_at: string;
}

export interface TaskTimelineChange {
  event_id: string;
  event_type: string;
  payload: Record<string, unknown>;
}

/** One newest-first immutable activity row for a task. */
export interface TaskTimelineItem {
  id: string;
  cursor: string;
  kind: TaskTimelineKind;
  task_id: string;
  actor: TimelineActor | null;
  created_at: string;
  progress: TaskTimelineProgress | null;
  comment: Comment | null;
  change: TaskTimelineChange | null;
}

export interface TaskTimelineCollection {
  data: TaskTimelineItem[];
  next_cursor?: string | null;
}

/** The bounded activity filters exposed by the Roadmap follow-along view. */
export type RoadmapActivityFilter = 'all' | 'agent-updates' | 'comments' | 'task-changes';
export type RoadmapActivityKind = Exclude<RoadmapActivityFilter, 'all'>;
export type TaskRouteIntent = 'details' | 'activity';

export interface RoadmapSummary {
  task_total: number;
  completed: number;
  completion_percent: number;
  state_counts: Record<SemanticState, number>;
  overdue: number;
  due_soon: number;
  upcoming: Task[];
  recent_activity: ActivityEvent[];
  project?: Project;
  projects?: RoadmapProject[];
  /** Optional aliases retained for the current UI and older payloads. */
  total_tasks?: number;
  completion_percentage?: number;
  completed_count?: number;
  overdue_count?: number;
  due_soon_count?: number;
  upcoming_tasks?: Task[];
}

/** Server-authoritative issue health aggregate used by the Issues view. */
export interface IssueMetrics {
  reopened: number;
  window_days: number;
  since: string;
  as_of: string;
}

/** Scalar navigation counts; no task rows are returned by this endpoint. */
export interface SidebarCounts {
  issues: number;
  my_work: number;
  view: 'live' | 'assigned';
}

export interface RoadmapProject {
  project: Project;
  total_tasks: number;
  completed_tasks: number;
  completion_percentage: number;
  state_counts?: Partial<Record<SemanticState, number>> & Record<string, number>;
}

export interface Agent {
  id: string;
  kind: 'agent';
  name: string;
  description?: string;
  admin: false;
  project_ids?: string[];
  created_at: string;
  updated_at: string;
  disabled_at?: string;
  tokens?: ApiToken[];
}

export interface ApiToken {
  id: string;
  agent_id: string;
  actor_id: string;
  name: string;
  scopes: Scope[];
  project_ids: string[];
  created_at: string;
  expires_at?: string | null;
  token?: string;
  /** @deprecated The runtime calls this field token; plaintext is never returned again. */
  plaintext?: never;
  last_used_at?: string;
}

export interface AuthStatus {
  mode: 'local' | 'cloudflare' | 'disabled';
  configured: boolean;
  setup_required: boolean;
  authenticated: boolean;
  actor: Actor | null;
  user: Actor | null;
  /** @deprecated Compatibility alias not emitted by the runtime. */
  needs_setup?: never;
}

export interface Collection<T> {
  data: T[];
  next_cursor?: string | null;
}

/** Versioned archive returned by the portable export endpoint. */
export interface PortableArchive {
  format: 'helm.portable' | string;
  version: number;
  exported_at?: string;
  source?: { product?: string; api?: string };
  projects: Array<Record<string, unknown>>;
  columns?: Array<Record<string, unknown>>;
  tasks?: Array<Record<string, unknown>>;
  labels?: Array<Record<string, unknown>>;
  actors?: Array<Record<string, unknown>>;
  relationships?: Record<string, unknown>;
  activity?: Record<string, unknown>;
  comments?: Array<Record<string, unknown>>;
  [key: string]: unknown;
}

export interface PortableImportCounts {
  [key: string]: number;
}

export interface PortableImportRemap {
  entity: string;
  source: string;
  target: string;
  field?: string;
  reason: string;
}

export interface PortableImportIssue {
  entity: string;
  id?: string;
  field?: string;
  message: string;
}

export interface PortableImportReport {
  format: string;
  version: number;
  dry_run: boolean;
  conflict: string;
  counts: PortableImportCounts;
  remaps: PortableImportRemap[];
  warnings: string[];
  errors: PortableImportIssue[];
}

export interface BoardDescriptor {
  id: string;
  project_id: string;
  name: string;
  slug: string;
  default: boolean;
  enabled: boolean;
}

export interface ApiErrorShape {
  error: {
    code: string;
    message: string;
    details?: Record<string, unknown>;
  };
}

export class ApiError extends Error {
  code: string;
  status: number;
  details: Record<string, unknown>;

  constructor(message: string, status: number, code = 'request_failed', details: Record<string, unknown> = {}) {
    super(message);
    this.name = 'ApiError';
    this.code = code;
    this.status = status;
    this.details = details;
  }
}

export type TaskPatch = Partial<Pick<Task, 'title' | 'description' | 'priority' | 'column_id' | 'position'>> & {
  kind?: TaskKind;
  bug?: BugPatch | null;
  description?: string | null;
  due_at?: string | null;
  assignee?: string | null;
  labels?: string[] | null;
  label_ids?: string[] | null;
  parent?: string | null;
  parent_id?: string | null;
  parent_task_id?: string | null;
};

/** A server-owned board audit lifecycle state. */
export type AuditRunStatus = 'queued' | 'running' | 'complete' | 'partial' | 'failed' | 'finalized';
export type AuditTerminalStatus = 'complete' | 'partial' | 'failed';

export type AuditVerdict = 'correct' | 'needs_attention' | 'move_proposed';
export type AuditReviewState = 'pending' | 'approved' | 'dismissed';

/**
 * Append-only snapshot metadata returned by the audit collection endpoint.
 * The API may include aggregate counters on list responses and findings on a
 * detail response, so those fields are intentionally optional.
 */
export interface AuditRun {
  id: string;
  project_id: string;
  actor_id?: string;
  scope?: string;
  status: AuditRunStatus | string;
  started_at?: string;
  finalized_at?: string | null;
  created_at?: string;
  updated_at?: string;
  finding_count?: number;
  findings_count?: number;
  counts?: Partial<Record<AuditVerdict, number>> & Record<string, number>;
  findings?: AuditFinding[];
}

/** A finding captured from one task during a board audit. */
export interface AuditFinding {
  id: string;
  audit_id: string;
  task_id: string;
  captured_version: number;
  source_column: string | Pick<Column, 'id' | 'name' | 'semantic_state'>;
  verdict: AuditVerdict | string;
  proposed_semantic_destination?: SemanticState | null;
  confidence: number;
  reason: string;
  evidence_refs?: string[];
  evidence?: string[];
  review_state: AuditReviewState | string;
  version?: number;
  current_version?: number;
  current_column?: string | Pick<Column, 'id' | 'name' | 'semantic_state'>;
  current_column_id?: string;
  changed_since_audit?: boolean;
  current_task?: Task;
  task?: Task;
  current?: {
    version?: number;
    column_id?: string;
    column?: string | Pick<Column, 'id' | 'name' | 'semantic_state'>;
    task?: Task;
  };
  created_at?: string;
  updated_at?: string;
}

export interface AuditDetail extends AuditRun {
  findings: AuditFinding[];
}

export interface AuditFindingPatch {
  review_state: AuditReviewState;
  proposed_semantic_destination?: SemanticState | null;
}

/** Explicit task reconciliation or precise board placement intent. */
export interface TaskMoveInput {
  destination_column_id: string;
  expected_source_column_id: string;
  source: string;
  reason?: string;
  before_task_id?: string;
  after_task_id?: string;
  placement?: 'first' | 'before' | 'between' | 'after' | 'last';
  expected_ordering_version?: number;
  expected_source_ordering_version?: number;
  expected_destination_ordering_version?: number;
  /** @deprecated The server allocates positions; use anchors instead. */
  position?: number;
}

/** Precise card placement intent used by the board drag and keyboard paths. */
export type TaskReorderInput = TaskMoveInput;
