export type SemanticState = 'backlog' | 'ready' | 'active' | 'blocked' | 'completed';
export type Priority = 'low' | 'normal' | 'high' | 'urgent';
export type ActorKind = 'human' | 'agent';
export type Scope = 'projects:read' | 'projects:write' | 'tasks:read' | 'tasks:write' | 'tasks:claim' | 'events:read';

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

export interface Task {
  id: string;
  number: number;
  key: string;
  project_id: string;
  column_id: string;
  title: string;
  description?: string;
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
  description?: string | null;
  due_at?: string | null;
  assignee?: string | null;
  labels?: string[] | null;
  label_ids?: string[] | null;
};
