export type SemanticState = 'backlog' | 'ready' | 'active' | 'blocked' | 'completed';
export type Priority = 'low' | 'normal' | 'high' | 'urgent';
export type TaskKind = 'task' | 'bug';
export type BugSeverity = 's1' | 's2' | 's3' | 's4';
export type BugResolution = 'fixed' | 'duplicate' | 'not_planned' | 'cannot_reproduce' | 'works_as_designed';
export type ActorKind = 'human' | 'agent';
export type Scope = 'projects:read' | 'projects:write' | 'tasks:read' | 'tasks:write' | 'tasks:claim' | 'events:read';

/** States published by an agent while it is actively working a task. */
export type AgentWorkState = 'working' | 'waiting' | 'verifying' | 'handoff';
/** Published states plus server-derived collection filters. */
export type AgentWorkStateFilter = AgentWorkState | 'stale' | 'missing';

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

export interface Project {
  id: string;
  key: string;
  slug: string;
  name: string;
  description: string;
  color: string;
  favorite: boolean;
  archived_at?: string;
  created_at?: string;
  updated_at?: string;
  task_count?: number;
  completed_task_count?: number;
  completed_count?: number;
}

/** Runtime task JSON uses actor IDs; the partial shape keeps old board fixtures compatible. */
export type TaskActorReference = string | Pick<Actor, 'id' | 'kind' | 'name'>;

export interface Column {
  id: string;
  project_id: string;
  name: string;
  semantic_state: SemanticState;
  position: number;
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
}

export interface Comment {
  id: string;
  task_id: string;
  actor_id: string;
  body: string;
  created_at: string;
  updated_at: string;
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
};
