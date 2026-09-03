<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { API_PREFIX, api, listAllIssues, listAllTasks, unwrapActor } from './lib/api';
  import {
    actorId,
    actorName,
    agentWorkActionNeeded,
    agentWorkBucket as workBucket,
    agentWorkForTask as agentWorkFor,
    agentWorkState as workState,
    agentWorkStatusCounts,
    agentWorkUpdatedAt as workUpdatedAt,
    bugReporterId,
    bugResolution,
    bugSeverity,
    dateToIso,
    displayEvent,
    filterTasks,
    formatDate,
    formatRelative,
    isDueSoon,
    isAgentWorkStale as isWorkStale,
    isMissingAgentWorkCandidate,
    isOverdue,
    loadRecentProjects,
    matchesAgentWorkFilter,
    moveTaskLocal,
    nextPosition,
    projectInitials,
    parseTaskRoute,
    rememberProject,
    taskDeepLink,
    shouldShowAgentPulse,
    sortTasks,
    toInputDate,
    type BoardFilters
  } from './lib/state';
  import {
    ApiError,
    type ActivityEvent,
    type Actor,
    type Agent,
    type ApiToken,
    type AuthStatus,
    type BugResolution,
    type BugSeverity,
    type Column,
    type Label,
    type Project,
    type RoadmapActivityFilter,
    type RoadmapSummary,
    type Task,
    type TaskReference,
    type TaskTimelineFilter,
    type TaskTimelineKind,
    type TaskTimelineItem,
    type TaskRouteIntent,
    type Priority
  } from './lib/types';
  import AgentPulse from './lib/components/AgentPulse.svelte';
  import AgentWorkPanel from './lib/components/AgentWorkPanel.svelte';
  import AuditReview from './lib/components/AuditReview.svelte';
  import BoardTimeline from './lib/components/BoardTimeline.svelte';
  import LiveWorkRow from './lib/components/LiveWorkRow.svelte';
  import RoadmapActivity from './lib/components/RoadmapActivity.svelte';
  import RoadmapLiveWork from './lib/components/RoadmapLiveWork.svelte';
  import TaskActivityTimeline from './lib/components/TaskActivityTimeline.svelte';
  import TaskDependencies from './lib/components/TaskDependencies.svelte';
  import {
    mergeAuthoritativeTask,
    mergeAuthoritativeTaskList,
    taskMutationsAfter,
    type TaskMutationKind,
    type TaskMutationRecord,
    type TaskMutationScope
  } from './lib/liveness';

  type View = 'board' | 'timeline' | 'issues' | 'my-work' | 'roadmap' | 'audits' | 'settings';
  type AuthView = 'login' | 'setup';
  type ToastKind = 'success' | 'error' | 'info';
  type CommandChoice = {
    kind: 'project' | 'view' | 'issue';
    id: string;
    label: string;
    hint: string;
    project?: Project;
    view?: View;
    task?: Task;
  };
  type WorkFilter = 'all' | 'action-needed' | 'working' | 'waiting' | 'verifying' | 'stale' | 'handoff' | 'missing';
  type MyWorkView = 'live' | 'assigned';
  type DrawerView = 'details' | 'activity';
  type MyWorkRow = { task: Task; project?: Project; column?: Column };

  const priorityLabels: Record<Priority, string> = {
    low: 'Low',
    normal: 'Normal',
    high: 'High',
    urgent: 'Urgent'
  };
  const stateLabels: Record<string, string> = {
    backlog: 'Backlog',
    ready: 'Ready',
    active: 'In progress',
    blocked: 'Blocked',
    completed: 'Done'
  };
  const severityLabels: Record<BugSeverity, string> = {
    s1: 'S1 · Critical',
    s2: 'S2 · High',
    s3: 'S3 · Medium',
    s4: 'S4 · Low'
  };
  const resolutionLabels: Record<BugResolution, string> = {
    fixed: 'Fixed',
    duplicate: 'Duplicate',
    not_planned: 'Not planned',
    cannot_reproduce: 'Cannot reproduce',
    works_as_designed: 'Works as designed'
  };
  const resolutionOptions: BugResolution[] = ['fixed', 'duplicate', 'not_planned', 'cannot_reproduce', 'works_as_designed'];
  const labelPalette = ['#6d5efc', '#2ea879', '#d49534', '#dc626f', '#4b9cf5'];
  const scopeOptions = ['projects:read', 'projects:write', 'tasks:read', 'tasks:write', 'tasks:claim', 'events:read'];
  const livenessRefreshIntervalMs = 60 * 1000;

  let booting = true;
  let authStatus: AuthStatus | null = null;
  let user: Actor | null = null;
  let authView: AuthView = 'login';
  let authSubmitting = false;
  let authError = '';
  let loginEmail = '';
  let loginPassword = '';
  let setupName = '';
  let setupEmail = '';
  let setupPassword = '';

  const accessBootstrapKey = 'roadmap.cloudflare-access-bootstrap';

  let theme: 'light' | 'dark' = 'light';
  let view: View = 'board';
  let projects: Project[] = [];
  let activeProjectSlug = '';
  let auditIdFromRoute = '';
  let recentProjectIds: string[] = [];
  let projectsLoading = false;
  let projectsError = '';
  let activeProject: Project | undefined;
  let columns: Column[] = [];
  let tasks: Task[] = [];
  let labels: Label[] = [];
  let boardLoading = false;
  let boardError = '';
  let issueTasks: Task[] = [];
  let issueColumns: Column[] = [];
  let issuesLoading = false;
  let issuesError = '';
  let issueRequest = 0;
  let filters: BoardFilters = { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all' };
  let boardWorkFilter: WorkFilter = 'all';
  let issueFilters: BoardFilters = {
    query: '',
    priority: 'all',
    label: 'all',
    assignee: 'all',
    state: 'all',
    kind: 'bug',
    severity: 'all',
    reporter: 'all',
    resolution: 'all'
  };
  let issueProjectFilter = 'all';
  let projectSwitcherOpen = false;
  let projectSwitcherQuery = '';
  let commandOpen = false;
  let commandQuery = '';
  let commandIndex = 0;
  let commandInput: HTMLInputElement;
  let projectListRequest = 0;
  let projectSwitchVersion = 0;
  let boardRequest = 0;
  let roadmapRequest = 0;
  let taskModalColumnsRequest = 0;
  let taskDetailRequest = 0;
  let myWorkRequest = 0;
  let sessionGeneration = 0;
  let taskMutationRevision = 0;
  const taskMutations: Record<TaskMutationScope, Map<string, TaskMutationRecord>> = {
    board: new Map(),
    issues: new Map(),
    'my-work-live': new Map(),
    'my-work-assigned': new Map()
  };
  let dialogReturnFocus: { element: HTMLElement | null; fallbackSelector: string } | null = null;

  let drawerTask: Task | null = null;
  let drawerDependencyPanel: { refreshRelationships: () => Promise<boolean> } | null = null;
  let drawerDependencyRefresh = 0;
  // The route intent is kept separate from drawer rendering so the drawer can
  // add its Activity tab without changing dashboard link semantics.
  let taskRouteIntent: TaskRouteIntent = 'details';
  let taskRouteOrigin = '';
  let drawerView: DrawerView = 'details';
  let drawerLoading = false;
  let drawerSaving = false;
  let drawerError = '';
  let commentBody = '';
  let commentSending = false;
  let drawerTimelineItems: TaskTimelineItem[] = [];
  let drawerTimelineNextCursor = '';
  let drawerTimelineLoading = false;
  let drawerTimelineLoadingOlder = false;
  let drawerTimelineError = '';
  let drawerTimelineFilter: TaskTimelineFilter = 'all';
  let drawerTimelineTaskId = '';
  let drawerTimelineRequest = 0;
  let boardTimelineItems: TaskTimelineItem[] = [];
  let boardTimelineNextCursor = '';
  let boardTimelineLoading = false;
  let boardTimelineLoadingOlder = false;
  let boardTimelineError = '';
  let boardTimelineFilter: TaskTimelineFilter = 'all';
  let boardTimelineProjectId = '';
  let boardTimelineRequest = 0;
  let draftTitle = '';
  let draftDescription = '';
  let draftPriority: Priority = 'normal';
  let draftDueDate = '';
  let draftAssignee = '';
  let draftLabels = '';
  let draftBugActual = '';
  let draftBugExpected = '';
  let draftBugReproduction = '';
  let draftBugEnvironment = '';
  let draftBugVersion = '';
  let triageSeverityDraft: BugSeverity = 's3';
  let resolutionDraft: BugResolution = 'fixed';
  let duplicateOfDraft = '';
  let resolutionNoteDraft = '';
  let reopenReasonDraft = '';
  let blockReasonDraft = '';
  let blockReasonOpen = false;

  let draggingTaskId = '';
  let quickAddColumn = '';
  let quickAddTitle: Record<string, string> = {};
  let taskActionLoading = '';
  let labelDeleting = '';

  let myWorkTasks: Task[] = [];
  let myWorkLoading = false;
  let myWorkError = '';
  let myWorkFilter: WorkFilter = 'all';
  let myWorkView: MyWorkView = 'live';
  let myWorkColumnsByProject: Record<string, Column[]> = {};
  let myWorkColumnsLoading = false;
  let roadmap: RoadmapSummary | null = null;
  let roadmapProjectId: string | undefined;
  let roadmapLoading = false;
  let roadmapError = '';
  let roadmapLiveTasks: Task[] = [];
  let roadmapLiveColumnsByProject: Record<string, Column[]> = {};
  let roadmapLiveLoading = false;
  let roadmapLiveError = '';
  let roadmapActivityTasks: Record<string, Task> = {};
  let roadmapActivityLoading = false;
  let roadmapActivityError = '';
  let roadmapActivityFilter: RoadmapActivityFilter = 'all';
  let roadmapLiveRequest = 0;
  let roadmapActivityRequest = 0;
  let roadmapActorsLoaded = false;
  let roadmapActorsLoading = false;
  let roadmapActors: Record<string, Pick<Actor, 'id' | 'kind' | 'name'>> = {};
  let auditRefreshToken = 0;

  let agents: Agent[] = [];
  let agentsLoading = false;
  let agentsError = '';
  let agentNameDraft = '';
  let agentDescriptionDraft = '';
  let selectedAgentId = '';
  let tokenNameDraft = '';
  let tokenScopes: string[] = ['projects:read', 'projects:write', 'tasks:read', 'tasks:write'];
  let tokenProjectIds: string[] = [];
  let tokenCreating = false;
  let revealedToken: ApiToken | null = null;
  let showAgentForm = false;
  let showTokenForm = false;

  let showProjectModal = false;
  let projectCreating = false;
  let projectFormError = '';
  let projectKeyDraft = '';
  let projectNameDraft = '';
  let projectDescriptionDraft = '';
  let projectColorDraft = '#6d5efc';
  let showTaskModal = false;
  let taskModalLoading = false;
  let taskModalCreating = false;
  let taskModalError = '';
  let taskModalProjectId = '';
  let taskModalColumnId = '';
  let taskModalColumns: Column[] = [];
  let taskModalTitle = '';
  let taskModalDescription = '';
  let taskModalPriority: Priority = 'normal';
  let taskModalDueDate = '';
  let taskModalAssignee = '';

  let showBugModal = false;
  let bugModalLoading = false;
  let bugModalCreating = false;
  let bugModalError = '';
  let bugModalProjectId = '';
  let bugModalColumnId = '';
  let bugModalColumns: Column[] = [];
  let bugModalTitle = '';
  let bugModalDescription = '';
  let bugModalActual = '';
  let bugModalExpected = '';
  let bugModalReproduction = '';
  let bugModalEnvironment = '';
  let bugModalVersion = '';
  let bugModalLabels = '';
  let bugModalSeverity: BugSeverity | '' = '';
  let bugModalPriority: Priority = 'normal';

  let events: ActivityEvent[] = [];
  let eventsCursor: number | undefined;
  let pollTimer: number | undefined;
  let pulseTimer: number | undefined;
  let livenessRefreshTimer: number | undefined;
  let pollInFlight: Promise<void> | null = null;
  let boardLivenessRequest = 0;
  let myWorkLivenessRequest = 0;
  let drawerLivenessRequest = 0;
  let boardLivenessInFlight: Promise<boolean> | null = null;
  let myWorkLivenessInFlight: Promise<boolean> | null = null;
  let drawerLivenessInFlight: { taskId: string; requestId: number; promise: Promise<boolean> } | null = null;
  let pulseClock = Date.now();
  let liveAnnouncement = '';
  let announcementTimer: number | undefined;
  let workTransitionSnapshot = new Map<string, string>();
  let toastSequence = 0;
  let toasts: { id: number; kind: ToastKind; message: string }[] = [];

  $: activeProject = projects.find((project) => project.slug === activeProjectSlug);
  $: visibleTasks = filterTasks(tasks, columns, filters).filter((task) => matchesWorkFilter(task, boardWorkFilter, pulseClock));
  $: boardWorkCounts = agentWorkStatusCounts(tasks, pulseClock, (task) => semanticStateForTask(task));
  $: visibleIssues = filterTasks(
    issueTasks.filter((task) => issueProjectFilter === 'all' || task.project_id === issueProjectFilter),
    issueColumns,
    issueFilters
  );
  $: issueReporterOptions = Array.from(new Set(issueTasks.map((task) => bugReporterId(task)).filter(Boolean))).sort();
  $: issueMetrics = {
    open: issueTasks.filter((task) => !task.bug?.resolution).length,
    untriaged: issueTasks.filter((task) => !task.bug?.severity && !task.bug?.resolution).length,
    severe: issueTasks.filter((task) => !task.bug?.resolution && (task.bug?.severity === 's1' || task.bug?.severity === 's2')).length,
    recentlyResolved: issueTasks.filter((task) => task.bug?.resolved_at && Date.now() - Date.parse(task.bug.resolved_at) <= 7 * 24 * 60 * 60 * 1000).length,
    reopened: new Set(events.filter((event) => event.type === 'bug.reopened' && event.task_id).map((event) => event.task_id)).size
  };
  $: if (view === 'issues') syncIssueViewURL(issueFilters, issueProjectFilter);
  $: sortedColumns = [...columns].sort((a, b) => a.position - b.position);
  // Keep the board's column buckets as a reactive value. Calling a helper
  // from the template does not give Svelte a dependency on visibleTasks, so
  // mutations could otherwise leave cards/counts rendered in their old
  // column until an unrelated update occurred.
  $: tasksByColumn = sortedColumns.reduce<Record<string, Task[]>>((groups, column) => {
    groups[column.id] = sortTasks(visibleTasks.filter((task) => task.column_id === column.id));
    return groups;
  }, {});
  $: favoriteProjects = projects.filter((project) => project.favorite);
  $: recentProjects = recentProjectIds
    .map((id) => projects.find((project) => project.id === id))
    .filter((project): project is Project => Boolean(project));
  $: filteredSwitcherProjects = projects.filter((project) =>
    `${project.name} ${project.key}`.toLowerCase().includes(projectSwitcherQuery.trim().toLowerCase())
  );
  $: commandChoices = buildCommandChoices(commandQuery);
  $: if (commandChoices.length && commandIndex >= commandChoices.length) commandIndex = commandChoices.length - 1;
  $: myWorkRows = myWorkTasks.map((task) => ({
    task,
    project: projectForTask(task),
    column: myWorkColumnsByProject[task.project_id]?.find((item) => item.id === task.column_id)
  })).filter((row) => matchesWorkFilter(row.task, myWorkFilter, pulseClock));
  $: myWorkActionRows = sortWorkRows(myWorkRows.filter((row) => isActionNeeded(row.task, pulseClock)));
  $: myWorkStaleRows = sortWorkRows(myWorkRows.filter((row) => workBucket(row.task, pulseClock) === 'stale'));
  $: myWorkWaitingRows = sortWorkRows(myWorkRows.filter((row) => workBucket(row.task, pulseClock) === 'waiting'));
  $: myWorkHandoffRows = sortWorkRows(myWorkRows.filter((row) => workBucket(row.task, pulseClock) === 'handoff'));
  $: myWorkVerifyingRows = sortWorkRows(myWorkRows.filter((row) => workBucket(row.task, pulseClock) === 'verifying'));
  $: myWorkWorkingRows = sortWorkRows(myWorkRows.filter((row) => workBucket(row.task, pulseClock) === 'working'));
  $: roadmapTotal = roadmap?.task_total ?? roadmap?.total_tasks ?? 0;
  $: roadmapCompletion = Math.max(0, Math.min(100, roadmap?.completion_percentage ?? roadmap?.completion_percent ?? 0));
  $: roadmapCompleted = roadmap?.completed_count ?? roadmap?.completed ?? 0;
  $: roadmapOverdue = roadmap?.overdue_count ?? roadmap?.overdue ?? 0;
  $: roadmapDueSoon = roadmap?.due_soon_count ?? roadmap?.due_soon ?? 0;
  $: roadmapUpcoming = roadmap?.upcoming_tasks ?? roadmap?.upcoming ?? [];
  $: roadmapProject = projects.find((project) => project.id === roadmapProjectId);
  $: roadmapActivityEvents = roadmap?.recent_activity ?? [];
  $: roadmapProjectRows = roadmap?.projects?.length
    ? roadmap.projects
    : projects.map((project) => ({
        project,
        total_tasks: project.task_count ?? 0,
        completed_tasks: project.completed_task_count ?? project.completed_count ?? 0,
        completion_percentage: project.task_count ? ((project.completed_task_count ?? project.completed_count ?? 0) / project.task_count) * 100 : 0
      }));
  $: taskModalProject = projects.find((project) => project.id === taskModalProjectId);

  const focusableSelector = [
    'a[href]',
    'area[href]',
    'button:not(:disabled)',
    'input:not(:disabled):not([type="hidden"])',
    'select:not(:disabled)',
    'textarea:not(:disabled)',
    'audio[controls]',
    'video[controls]',
    '[contenteditable="true"]',
    '[tabindex]:not([tabindex="-1"])'
  ].join(',');

  function isFocusableVisible(element: HTMLElement): boolean {
    if (!element.isConnected || element.hasAttribute('hidden') || element.getAttribute('aria-hidden') === 'true' || element.matches(':disabled')) return false;
    if (typeof window === 'undefined') return true;
    const style = window.getComputedStyle(element);
    return style.display !== 'none' && style.visibility !== 'hidden';
  }

  function focusableElements(node: HTMLElement): HTMLElement[] {
    return Array.from(node.querySelectorAll<HTMLElement>(focusableSelector)).filter(isFocusableVisible);
  }

  // A single action keeps keyboard focus inside every modal, while resolving
  // the focusable list on each Tab keypress so async/dynamic controls are
  // included without needing action updates or per-dialog handlers.
  function focusTrap(node: HTMLElement) {
    if (!node.hasAttribute('tabindex')) node.tabIndex = -1;

    const handleKeydown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return;
      const focusable = focusableElements(node);
      if (!focusable.length) {
        event.preventDefault();
        node.focus();
        return;
      }
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const index = active ? focusable.indexOf(active) : -1;
      if (event.shiftKey) {
        if (index <= 0) {
          event.preventDefault();
          focusable[focusable.length - 1].focus();
        }
      } else if (index < 0 || index === focusable.length - 1) {
        event.preventDefault();
        focusable[0].focus();
      }
    };

    node.addEventListener('keydown', handleKeydown);
    void tick().then(() => {
      if (!node.isConnected || node.contains(document.activeElement)) return;
      const initial = node.querySelector<HTMLElement>('[data-dialog-initial-focus]');
      const focusable = focusableElements(node);
      const target = initial && focusable.includes(initial) ? initial : focusable[0];
      (target || node).focus();
    });

    return { destroy: () => node.removeEventListener('keydown', handleKeydown) };
  }

  function isActionNeeded(task: Task, now = pulseClock): boolean {
    return agentWorkActionNeeded(task, now, semanticStateForTask(task));
  }

  function matchesWorkFilter(task: Task, filter: WorkFilter, now = pulseClock): boolean {
    return matchesAgentWorkFilter(task, filter, now, semanticStateForTask(task));
  }

  function sortWorkRows(rows: MyWorkRow[]): MyWorkRow[] {
    return [...rows].sort((a, b) => {
      const aTime = Date.parse(workUpdatedAt(a.task) || (myWorkView === 'assigned' ? a.task.updated_at || '' : '')) || 0;
      const bTime = Date.parse(workUpdatedAt(b.task) || (myWorkView === 'assigned' ? b.task.updated_at || '' : '')) || 0;
      return bTime - aTime || a.task.number - b.task.number;
    });
  }

  function agentLabelForTask(task: Task): string {
    const work = agentWorkFor(task);
    const actor = work?.actor_id;
    if (actor) return agents.find((agent) => agent.id === actor)?.name || actor;
    return actorName(task.claimed_by) || actorId(task.claimed_by);
  }

  function semanticStateForTask(task: Task, localColumns = columns): string {
    return localColumns.find((column) => column.id === task.column_id)?.semantic_state || '';
  }

  function myWorkMutationScope(workView = myWorkView): TaskMutationScope {
    return workView === 'live' ? 'my-work-live' : 'my-work-assigned';
  }

  function recordTaskMutation(
    taskId: string,
    kind: TaskMutationKind,
    scopes: TaskMutationScope[]
  ) {
    const revision = ++taskMutationRevision;
    scopes.forEach((scope) => {
      taskMutations[scope].set(taskId, { revision, kind });
    });
  }

  function recordScopedTaskMutation(
    taskId: string,
    mutations: Partial<Record<TaskMutationScope, TaskMutationKind>>
  ) {
    const revision = ++taskMutationRevision;
    Object.entries(mutations).forEach(([scope, kind]) => {
      if (kind) taskMutations[scope as TaskMutationScope].set(taskId, { revision, kind });
    });
  }

  function mutationsForRequest(scope: TaskMutationScope, requestRevision: number): Map<string, TaskMutationKind> {
    return taskMutationsAfter(taskMutations[scope], requestRevision);
  }

  function isMissingPulseCandidate(task: Task, localColumns = columns): boolean {
    return isMissingAgentWorkCandidate(task, semanticStateForTask(task, localColumns));
  }

  function showAgentPulse(task: Task, localColumns = columns): boolean {
    return shouldShowAgentPulse(task, semanticStateForTask(task, localColumns));
  }

  function claimIsActive(task: Task, now = pulseClock): boolean {
    if (!task.claimed_by || !task.claim_expires_at) return Boolean(task.claimed_by);
    const expires = Date.parse(task.claim_expires_at);
    return Number.isNaN(expires) || expires > now;
  }

  function claimCountdown(task: Task, now = pulseClock): string {
    if (!task.claim_expires_at) return '';
    const expires = Date.parse(task.claim_expires_at);
    if (Number.isNaN(expires)) return '';
    const difference = expires - now;
    if (difference <= 0) return 'expired';
    const minutes = Math.ceil(difference / 60000);
    if (minutes < 60) return `${minutes}m left`;
    const hours = Math.floor(minutes / 60);
    const remainder = minutes % 60;
    if (hours < 24) return `${hours}h${remainder ? ` ${remainder}m` : ''} left`;
    return `${Math.floor(hours / 24)}d${hours % 24 ? ` ${hours % 24}h` : ''} left`;
  }

  function claimExpiryExact(value?: string): string {
    if (!value) return '';
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
      timeZoneName: 'short'
    }).format(parsed);
  }

  function workTransitionKey(task: Task): string {
    if (!showAgentPulse(task)) return '';
    const work = agentWorkFor(task);
    if (!work) return '';
    return `${work.state || ''}:${isWorkStale(task)}:${Boolean(work.action_needed)}`;
  }

  function observeWorkTransitions(nextTasks: Task[]) {
    const next = new Map(workTransitionSnapshot);
    nextTasks.forEach((task) => {
      const key = workTransitionKey(task);
      next.set(task.id, key);
      const previous = workTransitionSnapshot.get(task.id);
      if (previous && key && previous !== key) {
        const status = isWorkStale(task) && workState(task) !== 'waiting' ? 'stale' : workState(task) || 'updated';
        announce(`${task.key} is now ${status}${isActionNeeded(task) ? ' · action needed' : ''}.`);
      }
    });
    workTransitionSnapshot = next;
  }

  function announce(message: string) {
    liveAnnouncement = message;
    if (announcementTimer) window.clearTimeout(announcementTimer);
    announcementTimer = window.setTimeout(() => {
      liveAnnouncement = '';
    }, 3500);
  }

  onMount(() => {
    theme = (localStorage.getItem('roadmap.theme') as 'light' | 'dark' | null) || 'light';
    applyTheme();
    recentProjectIds = loadRecentProjects(localStorage);
    const cleanup = () => {
      if (pollTimer) window.clearInterval(pollTimer);
      if (pulseTimer) window.clearInterval(pulseTimer);
      if (livenessRefreshTimer) window.clearInterval(livenessRefreshTimer);
      if (announcementTimer) window.clearTimeout(announcementTimer);
    };
    void bootstrap();
    const keyHandler = (event: KeyboardEvent) => handleKeydown(event);
    window.addEventListener('keydown', keyHandler);
    window.addEventListener('popstate', handlePopState);
    return () => {
      cleanup();
      window.removeEventListener('keydown', keyHandler);
      window.removeEventListener('popstate', handlePopState);
    };
  });

  function applyTheme() {
    if (typeof document !== 'undefined') {
      document.documentElement.dataset.theme = theme;
      const meta = document.querySelector('meta[name="theme-color"]');
      meta?.setAttribute('content', theme === 'dark' ? '#12131a' : '#f8fafc');
    }
  }

  function toggleTheme() {
    theme = theme === 'dark' ? 'light' : 'dark';
    localStorage.setItem('roadmap.theme', theme);
    applyTheme();
  }

  async function bootstrap() {
    booting = true;
    authError = '';
    let authStatusLoaded = false;
    try {
      authStatus = await api.authStatus();
      authStatusLoaded = true;
      sessionStorage.removeItem(accessBootstrapKey);
      if (authStatus.setup_required || authStatus.needs_setup) {
        authView = 'setup';
        booting = false;
        return;
      }
      if (authStatus.authenticated === false) {
        authView = 'login';
        booting = false;
        return;
      }
      try {
        user = authStatus.user || authStatus.actor || (await api.authMe());
      } catch (error) {
        // Development's disabled mode intentionally has no session actor.
        if (authStatus.mode === 'disabled') {
          user = { id: 'local', kind: 'human', name: 'Local user', admin: true };
        } else if (error instanceof ApiError && error.status === 401) {
          authView = 'login';
          booting = false;
          return;
        } else {
          throw error;
        }
      }
      await finishAuthentication();
    } catch (error) {
      // Cloudflare Access protects the browser UI and API as distinct
      // applications so agents can use Service Auth on /api/v1/*. A browser
      // therefore needs one top-level API navigation to receive the API-path
      // authorization cookie; an XHR cannot complete that cross-origin login
      // redirect. The status endpoint sends HTML navigations back here after
      // Access has issued the cookie.
      if (
        !authStatusLoaded &&
        error instanceof TypeError &&
        window.location.protocol === 'https:' &&
        sessionStorage.getItem(accessBootstrapKey) !== window.location.origin
      ) {
        sessionStorage.setItem(accessBootstrapKey, window.location.origin);
        window.location.assign(`${API_PREFIX}/auth/status`);
        return;
      }
      authError = friendlyError(error, 'Roadmap could not connect to the server.');
    } finally {
      booting = false;
    }
  }

  async function finishAuthentication() {
    // Every successful authentication starts a fresh client session. Any
    // request left behind by a previous session must fail its generation
    // check even when the browser logs back in as the same actor.
    sessionGeneration += 1;
    const requestedSession = sessionGeneration;
    await loadProjects();
    if (sessionGeneration !== requestedSession || !user) return;
    startPolling();
  }

  async function submitAuth() {
    authSubmitting = true;
    authError = '';
    try {
      if (authView === 'setup') {
        if (!setupName.trim() || !setupEmail.trim() || setupPassword.length < 12) {
          throw new Error('Enter your name, a valid email, and a password with at least 12 characters.');
        }
        await api.authSetup({ name: setupName.trim(), email: setupEmail.trim(), password: setupPassword });
        // Setup intentionally creates the first administrator without a
        // session. Sign in immediately so first-run onboarding lands in the
        // workspace instead of leaving the UI with an unauthenticated actor.
        const result = await api.authLogin({ email: setupEmail.trim(), password: setupPassword });
        user = unwrapActor(result);
      } else {
        if (!loginEmail.trim() || !loginPassword) throw new Error('Enter your email and password.');
        const result = await api.authLogin({ email: loginEmail.trim(), password: loginPassword });
        user = unwrapActor(result);
      }
      await finishAuthentication();
    } catch (error) {
      authError = friendlyError(error, 'We could not sign you in. Check your details and try again.');
    } finally {
      authSubmitting = false;
    }
  }

  async function logout() {
    // Invalidate reads before awaiting the network logout. Otherwise a poll
    // or list request can still resolve while the logout request is pending,
    // and its response could repopulate the session being cleared below.
    sessionGeneration += 1;
    user = null;
    projectListRequest += 1;
    boardRequest += 1;
    roadmapRequest += 1;
    roadmapLiveRequest += 1;
    roadmapActivityRequest += 1;
    drawerTimelineRequest += 1;
    boardTimelineRequest += 1;
    taskModalColumnsRequest += 1;
    issueRequest += 1;
    if (drawerTask) closeDrawer();
    activeProjectSlug = '';
    auditIdFromRoute = '';
    roadmapProjectId = undefined;
    projects = [];
    columns = [];
    tasks = [];
    labels = [];
    issueTasks = [];
    issueColumns = [];
    myWorkTasks = [];
    myWorkColumnsByProject = {};
    roadmap = null;
    roadmapLiveTasks = [];
    roadmapLiveColumnsByProject = {};
    roadmapLiveError = '';
    roadmapActivityTasks = {};
    roadmapActivityError = '';
    boardTimelineItems = [];
    boardTimelineNextCursor = '';
    boardTimelineError = '';
    boardTimelineProjectId = '';
    roadmapActors = {};
    roadmapActorsLoaded = false;
    roadmapActorsLoading = false;
    events = [];
    eventsCursor = undefined;
    if (pollTimer) window.clearInterval(pollTimer);
    if (pulseTimer) window.clearInterval(pulseTimer);
    if (livenessRefreshTimer) window.clearInterval(livenessRefreshTimer);
    pollTimer = undefined;
    pulseTimer = undefined;
    livenessRefreshTimer = undefined;
    pollInFlight = null;
    boardLivenessRequest += 1;
    myWorkLivenessRequest += 1;
    drawerLivenessRequest += 1;
    drawerTimelineItems = [];
    drawerTimelineNextCursor = '';
    drawerTimelineError = '';
    drawerTimelineTaskId = '';
    boardLivenessInFlight = null;
    myWorkLivenessInFlight = null;
    drawerLivenessInFlight = null;
    taskMutationRevision = 0;
    Object.values(taskMutations).forEach((records) => records.clear());
    workTransitionSnapshot = new Map();
    try {
      await api.authLogout();
    } catch {
      // Clearing the local session is still the least surprising UI result.
    }
  }

  async function loadProjects() {
    const requestId = ++projectListRequest;
    const selectionVersion = projectSwitchVersion;
    const requestedSession = sessionGeneration;
    projectsLoading = true;
    projectsError = '';
    try {
      const result = await api.listAllProjects();
      if (requestId !== projectListRequest || sessionGeneration !== requestedSession || !user) return;
      const nextProjects = result.data.filter((project) => !project.archived_at);
      projects = nextProjects;
      if (selectionVersion !== projectSwitchVersion) return;
      const routeSlug = getProjectSlugFromLocation();
      const taskRoute = getTaskRouteFromLocation();
      const remembered = localStorage.getItem('roadmap.last-project');
      const target = nextProjects.find((project) => project.slug === routeSlug) || nextProjects.find((project) => project.slug === remembered) || nextProjects[0];
      if (target) {
        activeProjectSlug = target.slug;
        if (view === 'board' || routeSlug) await loadBoard();
      } else {
        activeProjectSlug = '';
        columns = [];
        tasks = [];
        labels = [];
        if (!routeSlug) view = 'board';
      }
      if (
        requestId !== projectListRequest
        || selectionVersion !== projectSwitchVersion
        || sessionGeneration !== requestedSession
        || !user
      ) return;
      const path = window.location.pathname;
      if (taskRoute && target) {
        roadmapProjectId = undefined;
        view = 'board';
        await openTaskFromRoute(taskRoute, target);
      } else if (/^\/my-work\/?$/.test(path)) {
        roadmapProjectId = undefined;
        view = 'my-work';
        await loadMyWork();
      } else if (/^\/issues\/?$/.test(path)) {
        roadmapProjectId = undefined;
        applyIssueRouteFilters(new URL(window.location.href).searchParams);
        view = 'issues';
        await loadIssues();
      } else if (/^\/roadmap\/?$/.test(path)) {
        roadmapProjectId = undefined;
        view = 'roadmap';
        await loadRoadmap();
      } else if (/^\/settings\/?$/.test(path)) {
        roadmapProjectId = undefined;
        view = 'settings';
        await loadAgents();
      } else if (routeSlug && isProjectAuditLocation()) {
        roadmapProjectId = undefined;
        auditIdFromRoute = getAuditIdFromLocation();
        view = 'audits';
      } else if (routeSlug && isProjectTimelineLocation()) {
        roadmapProjectId = undefined;
        view = 'timeline';
        await loadBoardTimeline(target?.id, { reset: true });
      } else if (routeSlug && isProjectRoadmapLocation()) {
        roadmapProjectId = target?.id;
        view = 'roadmap';
        await loadRoadmap(roadmapProjectId);
      }
    } catch (error) {
      if (requestId === projectListRequest && sessionGeneration === requestedSession && user) {
        projectsError = friendlyError(error, 'Projects could not be loaded.');
      }
    } finally {
      if (requestId === projectListRequest && sessionGeneration === requestedSession) projectsLoading = false;
    }
  }

  async function loadBoard(): Promise<boolean> {
    const requestId = ++boardRequest;
    boardLivenessRequest += 1;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    const requestedSlug = activeProjectSlug;
    if (!requestedSlug) {
      boardLoading = false;
      return true;
    }
    const project = projects.find((item) => item.slug === requestedSlug);
    if (!project) {
      boardLoading = false;
      return true;
    }
    boardLoading = true;
    boardError = '';
    try {
      const [columnResult, taskResult, labelResult] = await Promise.all([
        api.listAllColumns(project.id),
        listAllTasks(project.id, { limit: 200 }),
        api.listAllLabels(project.id)
      ]);
      if (
        requestId !== boardRequest
        || activeProjectSlug !== requestedSlug
        || sessionGeneration !== requestedSession
        || !user
      ) return false;
      columns = columnResult.data;
      tasks = mergeAuthoritativeTaskList(
        tasks,
        taskResult.data,
        mutationsForRequest('board', mutationSnapshot)
      );
      labels = labelResult.data;
      observeWorkTransitions(tasks);
      return true;
    } catch (error) {
      if (
        requestId === boardRequest
        && activeProjectSlug === requestedSlug
        && sessionGeneration === requestedSession
        && user
      ) {
        boardError = friendlyError(error, 'This board could not be loaded.');
      }
      return false;
    } finally {
      if (requestId === boardRequest && sessionGeneration === requestedSession) boardLoading = false;
    }
  }

  async function loadBoardTimeline(
    projectId = activeProject?.id,
    options: { older?: boolean; reset?: boolean } = {}
  ): Promise<boolean> {
    const { older = false, reset = false } = options;
    if (!projectId) return true;
    if (older && !boardTimelineNextCursor) return true;
    if (reset) {
      boardTimelineItems = [];
      boardTimelineNextCursor = '';
      boardTimelineError = '';
    }
    const requestId = ++boardTimelineRequest;
    const requestedSession = sessionGeneration;
    const requestedProjectId = projectId;
    const requestedFilter = boardTimelineFilter;
    const previousCursor = boardTimelineNextCursor;
    const hadItems = boardTimelineItems.length > 0;
    if (older) boardTimelineLoadingOlder = true;
    else boardTimelineLoading = true;
    boardTimelineError = '';
    try {
      const result = await api.listProjectTimeline(requestedProjectId, {
        before: older ? previousCursor : undefined,
        limit: 50,
        kind: requestedFilter === 'all' ? undefined : requestedFilter
      });
      if (
        requestId !== boardTimelineRequest
        || requestedSession !== sessionGeneration
        || !user
        || view !== 'timeline'
        || activeProject?.id !== requestedProjectId
        || boardTimelineFilter !== requestedFilter
      ) return false;
      const merged = new Map<string, TaskTimelineItem>();
      const rows = older ? [...boardTimelineItems, ...result.data] : [...result.data, ...(reset ? [] : boardTimelineItems)];
      rows.forEach((item) => merged.set(item.id, item));
      boardTimelineItems = [...merged.values()].sort((a, b) => {
        const time = Date.parse(b.created_at) - Date.parse(a.created_at);
        return time || b.cursor.localeCompare(a.cursor) || b.id.localeCompare(a.id);
      });
      boardTimelineProjectId = requestedProjectId;
      boardTimelineNextCursor = older || !hadItems || reset || !previousCursor ? (result.next_cursor || '') : previousCursor;
      return true;
    } catch (error) {
      if (
        requestId === boardTimelineRequest
        && requestedSession === sessionGeneration
        && view === 'timeline'
        && activeProject?.id === requestedProjectId
      ) boardTimelineError = friendlyError(error, 'This board timeline could not be loaded.');
      return false;
    } finally {
      if (requestId === boardTimelineRequest) {
        if (older) boardTimelineLoadingOlder = false;
        else boardTimelineLoading = false;
      }
    }
  }

  function setBoardTimelineFilter(next: TaskTimelineFilter) {
    if (!activeProject || boardTimelineFilter === next) return;
    boardTimelineFilter = next;
    void loadBoardTimeline(activeProject.id, { reset: true });
  }

  function loadOlderBoardTimeline() {
    if (activeProject && boardTimelineNextCursor) void loadBoardTimeline(activeProject.id, { older: true });
  }

  /** Load bug-capable tasks from every accessible project for the Issues view. */
  async function loadIssues(): Promise<boolean> {
    const requestId = ++issueRequest;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    issuesLoading = true;
    issuesError = '';
    try {
      const [taskResult, columnResults] = await Promise.all([
        listAllIssues({ limit: 200 }),
        Promise.all(projects.map(async (project) => api.listAllColumns(project.id)))
      ]);
      if (
        requestId !== issueRequest
        || view !== 'issues'
        || sessionGeneration !== requestedSession
        || !user
      ) return false;
      issueTasks = mergeAuthoritativeTaskList(
        issueTasks,
        taskResult.data,
        mutationsForRequest('issues', mutationSnapshot)
      );
      issueColumns = columnResults.flatMap((result) => result.data);
      return true;
    } catch (error) {
      if (requestId === issueRequest && view === 'issues' && sessionGeneration === requestedSession && user) {
        issuesError = friendlyError(error, 'Issues could not be loaded.');
      }
      return false;
    } finally {
      if (requestId === issueRequest && sessionGeneration === requestedSession) issuesLoading = false;
    }
  }

  async function loadMyWork(): Promise<boolean> {
    const requestId = ++myWorkRequest;
    myWorkLivenessRequest += 1;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    const requestedView = myWorkView;
    myWorkLoading = true;
    myWorkColumnsLoading = true;
    myWorkError = '';
    const projectSnapshot = [...projects];
    try {
      const [workResult, columnResults] = await Promise.all([
        api.allMyWork({ view: requestedView }),
        Promise.all(projectSnapshot.map(async (project) => ({
          projectId: project.id,
          columns: (await api.listAllColumns(project.id)).data
        })))
      ]);
      if (
        requestId !== myWorkRequest
        || myWorkView !== requestedView
        || sessionGeneration !== requestedSession
        || !user
      ) return false;
      myWorkTasks = mergeAuthoritativeTaskList(
        myWorkTasks,
        workResult.data,
        mutationsForRequest(myWorkMutationScope(requestedView), mutationSnapshot)
      );
      myWorkColumnsByProject = Object.fromEntries(columnResults.map((result) => [result.projectId, result.columns]));
      observeWorkTransitions(myWorkTasks);
      return true;
    } catch (error) {
      if (requestId === myWorkRequest && sessionGeneration === requestedSession && user) {
        myWorkError = friendlyError(error, 'Live work could not be loaded.');
      }
      return false;
    } finally {
      if (requestId === myWorkRequest && sessionGeneration === requestedSession) {
        myWorkLoading = false;
        myWorkColumnsLoading = false;
      }
    }
  }

  function selectMyWorkView(next: MyWorkView) {
    if (myWorkView === next) return;
    myWorkView = next;
    myWorkFilter = 'all';
    void loadMyWork();
  }

  async function loadRoadmap(projectId = roadmapProjectId): Promise<boolean> {
    const requestId = ++roadmapRequest;
    const requestedProjectId = projectId;
    roadmapLoading = true;
    roadmapError = '';
    try {
      const result = await api.roadmap(projectId);
      if (requestId !== roadmapRequest || view !== 'roadmap' || roadmapProjectId !== requestedProjectId) return false;
      // Keep the UI aliases compatible with both the contract names and the
      // compact names emitted by the current server implementation.
      roadmap = {
        ...result,
        completed_count: result.completed_count ?? result.completed,
        overdue_count: result.overdue_count ?? result.overdue,
        due_soon_count: result.due_soon_count ?? result.due_soon,
        upcoming_tasks: result.upcoming_tasks ?? result.upcoming
      };
      if (!projectId && result.projects?.length) {
        projects = projects.map((project) => {
          const summary = result.projects?.find((row) => row.project.id === project.id);
          return summary
            ? { ...project, task_count: summary.total_tasks, completed_task_count: summary.completed_tasks }
            : project;
          });
      } else if (!projectId && projects.length) {
        // The v1 response may omit per-project rows. Fill the progress list
        // from the documented project-scoped roadmap endpoint when needed.
        const summaries = await Promise.all(
          projects.map(async (project) => {
            try {
              return { projectId: project.id, summary: await api.roadmap(project.slug) };
            } catch {
              return null;
            }
          })
        );
        if (requestId !== roadmapRequest || view !== 'roadmap' || roadmapProjectId !== requestedProjectId) return false;
        projects = projects.map((project) => {
          const row = summaries.find((summary) => summary?.projectId === project.id)?.summary;
          return row
            ? { ...project, task_count: row.task_total ?? row.total_tasks ?? project.task_count, completed_task_count: row.completed ?? row.completed_count ?? project.completed_task_count }
            : project;
        });
      }
      // These are intentionally separate reads from the aggregate summary:
      // live work is task-scoped and activity task metadata is not embedded in
      // the append-only event records. Both reads are guarded independently so
      // a route change cannot paint another scope over the current Roadmap.
      void loadRoadmapLiveWork(projectId, requestId);
      void loadRoadmapActivityTasks(result.recent_activity || [], projectId, requestId);
      void loadRoadmapActors(requestId);
      return true;
    } catch (error) {
      if (requestId === roadmapRequest && view === 'roadmap' && roadmapProjectId === requestedProjectId) {
        roadmapError = friendlyError(error, 'Roadmap progress could not be loaded.');
      }
      return false;
    } finally {
      if (requestId === roadmapRequest) roadmapLoading = false;
    }
  }

  async function loadRoadmapLiveWork(projectId: string | undefined, parentRequestId: number): Promise<void> {
    const requestId = ++roadmapLiveRequest;
    const requestedSession = sessionGeneration;
    const scopeProjects = projectId
      ? projects.filter((project) => project.id === projectId)
      : [...projects];
    roadmapLiveLoading = true;
    roadmapLiveError = '';
    try {
      const [workResult, columnResults] = await Promise.all([
        api.allMyWork({ view: 'live', ...(projectId ? { project: projectId } : {}) }),
        Promise.all(scopeProjects.map(async (project) => ({
          projectId: project.id,
          columns: (await api.listAllColumns(project.id)).data
        })))
      ]);
      if (
        requestId !== roadmapLiveRequest
        || parentRequestId !== roadmapRequest
        || requestedSession !== sessionGeneration
        || view !== 'roadmap'
        || roadmapProjectId !== projectId
        || !user
      ) return;
      roadmapLiveTasks = workResult.data;
      roadmapLiveColumnsByProject = Object.fromEntries(columnResults.map((result) => [result.projectId, result.columns]));
      observeWorkTransitions(roadmapLiveTasks);
    } catch (error) {
      if (
        requestId === roadmapLiveRequest
        && parentRequestId === roadmapRequest
        && requestedSession === sessionGeneration
        && view === 'roadmap'
        && roadmapProjectId === projectId
        && user
      ) {
        roadmapLiveError = friendlyError(error, 'Live agent work could not be loaded.');
      }
    } finally {
      if (requestId === roadmapLiveRequest) roadmapLiveLoading = false;
    }
  }

  function knownRoadmapActivityTasks(): Record<string, Task> {
    const known: Record<string, Task> = { ...roadmapActivityTasks };
    [...tasks, ...myWorkTasks, ...roadmapLiveTasks, ...roadmapUpcoming].forEach((task) => {
      known[task.id] = task;
    });
    return known;
  }

  async function loadRoadmapActivityTasks(
    activity: ActivityEvent[],
    projectId: string | undefined,
    parentRequestId: number
  ): Promise<void> {
    const requestId = ++roadmapActivityRequest;
    const requestedSession = sessionGeneration;
    const known = knownRoadmapActivityTasks();
    const taskIds = Array.from(new Set(activity.map((event) => event.task_id).filter((id): id is string => Boolean(id))));
    const missing = taskIds.filter((taskId) => !known[taskId]);
    roadmapActivityLoading = missing.length > 0;
    roadmapActivityError = '';
    if (!missing.length) {
      if (
        requestId === roadmapActivityRequest
        && parentRequestId === roadmapRequest
        && requestedSession === sessionGeneration
        && view === 'roadmap'
        && roadmapProjectId === projectId
      ) roadmapActivityTasks = known;
      roadmapActivityLoading = false;
      return;
    }
    const fetched = await Promise.all(missing.map(async (taskId) => {
      try {
        return await api.getTask(taskId);
      } catch {
        return null;
      }
    }));
    if (
      requestId !== roadmapActivityRequest
      || parentRequestId !== roadmapRequest
      || requestedSession !== sessionGeneration
      || view !== 'roadmap'
      || roadmapProjectId !== projectId
    ) return;
    fetched.filter((task): task is Task => Boolean(task)).forEach((task) => {
      if (!projectId || task.project_id === projectId) known[task.id] = task;
    });
    roadmapActivityTasks = known;
    roadmapActivityLoading = false;
  }

  function seedRoadmapActors() {
    const next = { ...roadmapActors };
    if (user) next[user.id] = { id: user.id, kind: user.kind, name: user.name };
    agents.forEach((agent) => {
      next[agent.id] = { id: agent.id, kind: agent.kind, name: agent.name };
    });
    roadmapActors = next;
  }

  async function loadRoadmapActors(parentRequestId: number): Promise<void> {
    seedRoadmapActors();
    if (roadmapActorsLoaded || roadmapActorsLoading) return;
    roadmapActorsLoading = true;
    const requestedSession = sessionGeneration;
    try {
      const result = await api.listAllAgents();
      if (requestedSession !== sessionGeneration || !user) return;
      result.data.forEach((agent) => {
        roadmapActors[agent.id] = { id: agent.id, kind: agent.kind, name: agent.name };
      });
      seedRoadmapActors();
      roadmapActorsLoaded = true;
    } catch {
      // A non-admin can still use Roadmap; opaque actor IDs remain explicit
      // fallbacks when the agent directory is not readable.
    } finally {
      if (requestedSession === sessionGeneration && parentRequestId <= roadmapRequest) roadmapActorsLoading = false;
    }
  }

  async function loadAgents() {
    agentsLoading = true;
    agentsError = '';
    try {
      const result = await api.listAllAgents();
      agents = result.data;
      if (!selectedAgentId && agents[0]) selectedAgentId = agents[0].id;
    } catch (error) {
      agentsError = friendlyError(error, 'Agent settings could not be loaded.');
    } finally {
      agentsLoading = false;
    }
  }

  function startPolling() {
    if (pollTimer) window.clearInterval(pollTimer);
    if (pulseTimer) window.clearInterval(pulseTimer);
    if (livenessRefreshTimer) window.clearInterval(livenessRefreshTimer);
    pollTimer = window.setInterval(() => void pollEvents(), 15000);
    pulseTimer = window.setInterval(() => {
      pulseClock = Date.now();
      observeWorkTransitions([...tasks, ...myWorkTasks]);
    }, 30000);
    livenessRefreshTimer = window.setInterval(() => void refreshLiveness(), livenessRefreshIntervalMs);
    pulseClock = Date.now();
    void pollEvents();
  }

  async function refreshLiveness(): Promise<void> {
    if (!user) return;
    const refreshes: Promise<boolean>[] = [];
    if (view === 'board' || view === 'timeline') refreshes.push(refreshBoardTasks());
    if (view === 'my-work') refreshes.push(refreshMyWorkTasks());
    if (view === 'audits') auditRefreshToken += 1;
    if (drawerTask) refreshes.push(refreshDrawerTask(drawerTask.id));
    await Promise.all(refreshes).catch(() => undefined);
  }

  async function refreshBoardTasks(): Promise<boolean> {
    if (!user || (view !== 'board' && view !== 'timeline') || !activeProject || boardLoading) return true;
    if (boardLivenessInFlight) return boardLivenessInFlight;

    const requestId = ++boardLivenessRequest;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    const requestedSlug = activeProjectSlug;
    const requestedProjectId = activeProject.id;
    const normalRequestId = boardRequest;
    const refresh = (async () => {
      try {
        const result = await listAllTasks(requestedProjectId, { limit: 200 });
        if (
          !user
          || sessionGeneration !== requestedSession
          || (view !== 'board' && view !== 'timeline')
          || activeProjectSlug !== requestedSlug
          || boardRequest !== normalRequestId
          || boardLivenessRequest !== requestId
        ) return false;
        tasks = mergeAuthoritativeTaskList(
          tasks,
          result.data,
          mutationsForRequest('board', mutationSnapshot)
        );
        const updatedDrawer = drawerTask?.id === undefined
          ? undefined
          : result.data.find((task) => task.id === drawerTask?.id);
        if (updatedDrawer && drawerTask?.id === updatedDrawer.id) {
          drawerTask = mergeAuthoritativeTask(drawerTask, updatedDrawer);
        }
        observeWorkTransitions(tasks);
        return true;
      } catch {
        // Liveness refreshes are best effort and must not surface a transient
        // failure as a board error or loading state.
        return false;
      }
    })();
    boardLivenessInFlight = refresh;
    try {
      return await refresh;
    } finally {
      if (boardLivenessInFlight === refresh) boardLivenessInFlight = null;
    }
  }

  async function refreshMyWorkTasks(): Promise<boolean> {
    if (!user || view !== 'my-work' || myWorkLoading || myWorkColumnsLoading) return true;
    if (myWorkLivenessInFlight) return myWorkLivenessInFlight;

    const requestId = ++myWorkLivenessRequest;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    const requestedView = myWorkView;
    const normalRequestId = myWorkRequest;
    const refresh = (async () => {
      try {
        const result = await api.allMyWork({ view: requestedView });
        if (
          !user
          || sessionGeneration !== requestedSession
          || view !== 'my-work'
          || myWorkView !== requestedView
          || myWorkRequest !== normalRequestId
          || myWorkLivenessRequest !== requestId
        ) return false;
        myWorkTasks = mergeAuthoritativeTaskList(
          myWorkTasks,
          result.data,
          mutationsForRequest(myWorkMutationScope(requestedView), mutationSnapshot)
        );
        const updatedDrawer = drawerTask?.id === undefined
          ? undefined
          : result.data.find((task) => task.id === drawerTask?.id);
        if (updatedDrawer && drawerTask?.id === updatedDrawer.id) {
          drawerTask = mergeAuthoritativeTask(drawerTask, updatedDrawer);
        }
        observeWorkTransitions(myWorkTasks);
        return true;
      } catch {
        // Liveness refreshes are best effort and must not surface a transient
        // failure as a My Work error or loading state.
        return false;
      }
    })();
    myWorkLivenessInFlight = refresh;
    try {
      return await refresh;
    } finally {
      if (myWorkLivenessInFlight === refresh) myWorkLivenessInFlight = null;
    }
  }

  async function pollEvents() {
    if (!user || pollInFlight) return pollInFlight || undefined;
    const requestedSession = sessionGeneration;
    const requestedCursor = eventsCursor;
    let poll: Promise<void>;
    poll = (async () => {
      try {
        const result = await api.listEvents({ after: requestedCursor });
        const isCurrentPoll = () => Boolean(
          user
          && sessionGeneration === requestedSession
          && pollInFlight === poll
        );
        if (!isCurrentPoll()) return;
        // The project timeline has its own newest-first cursor. Refresh its
        // first page on every poll so a quiet or globally backlogged event
        // feed cannot delay visible work from the current board.
        if (view === 'timeline' && activeProject?.id) {
          if (!(await loadBoardTimeline(activeProject.id)) || !isCurrentPoll()) return;
        }
        if (!result.data.length) return;
        const mergedEvents = new Map<string, ActivityEvent>();
        [...events, ...result.data].forEach((event) => mergedEvents.set(event.id || String(event.cursor), event));
        events = [...mergedEvents.values()].sort((a, b) => b.cursor - a.cursor).slice(0, 100);
        const nextEventsCursor = Math.max(...result.data.map((event) => event.cursor));
        const currentProjectId = activeProject?.id;
        const currentView = view;
        const boardChanged = result.data.some((event) => !event.project_id || event.project_id === currentProjectId);
        const affectedTaskIds = new Set(result.data.map((event) => event.task_id).filter((id): id is string => Boolean(id)));
        let reloadSucceeded = true;

        if (boardChanged && (currentView === 'board' || currentView === 'timeline')) reloadSucceeded = (await loadBoard()) && reloadSucceeded;
        if (!isCurrentPoll()) return;
        if (boardChanged && currentView === 'issues') reloadSucceeded = (await loadIssues()) && reloadSucceeded;
        if (!isCurrentPoll()) return;
        if (currentView === 'my-work') reloadSucceeded = (await loadMyWork()) && reloadSucceeded;
        if (!isCurrentPoll()) return;
        if (currentView === 'roadmap') reloadSucceeded = (await loadRoadmap()) && reloadSucceeded;
        if (!isCurrentPoll()) return;

        if (drawerTask && affectedTaskIds.has(drawerTask.id)) {
          const drawerTaskId = drawerTask.id;
          const dependencyChanged = result.data.some((event) =>
            event.task_id === drawerTaskId
            && ['task.dependency_added', 'task.dependency_removed', 'task.dependency_state_changed'].includes(event.type)
          );
          reloadSucceeded = (await refreshDrawerTask(drawerTaskId)) && reloadSucceeded;
          if (dependencyChanged && drawerTask?.id === drawerTaskId) {
            if (drawerView === 'details' && drawerDependencyPanel) {
              reloadSucceeded = (await drawerDependencyPanel.refreshRelationships()) && reloadSucceeded;
            } else {
              drawerDependencyRefresh += 1;
            }
          }
          if (drawerView === 'activity') {
            reloadSucceeded = (await loadDrawerTimeline(drawerTaskId)) && reloadSucceeded;
          }
        }
        // Leave the cursor where it was when any dependent read failed. The
        // next poll will replay the event and retry the authoritative refresh.
        if (reloadSucceeded && isCurrentPoll()) eventsCursor = nextEventsCursor;
      } catch {
        // Polling is best effort; the visible board remains usable during a blip.
      }
    })().finally(() => {
      // A logout/re-login may have installed a different poll while this
      // response was settling. Only the owner may clear the in-flight slot.
      if (pollInFlight === poll) pollInFlight = null;
    });
    pollInFlight = poll;
    return poll;
  }

  function getProjectSlugFromLocation(): string {
    const match = window.location.pathname.match(/^\/p\/([^/]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  function getTaskRouteFromLocation() {
    return parseTaskRoute(window.location.pathname, window.location.search, window.location.hash);
  }

  function isTaskLocation(): boolean {
    return Boolean(getTaskRouteFromLocation());
  }

  function isProjectRoadmapLocation(): boolean {
    return /^\/p\/[^/]+\/roadmap\/?$/.test(window.location.pathname);
  }

  function isProjectTimelineLocation(): boolean {
    return /^\/p\/[^/]+\/timeline\/?$/.test(window.location.pathname);
  }

  function isProjectAuditLocation(): boolean {
    return /^\/p\/[^/]+\/audits(?:\/[^/]+)?\/?$/.test(window.location.pathname);
  }

  function getAuditIdFromLocation(): string {
    const match = window.location.pathname.match(/^\/p\/[^/]+\/audits\/([^/]+)\/?$/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  async function openTaskFromRoute(
    route: NonNullable<ReturnType<typeof parseTaskRoute>>,
    routeProject = projects.find((project) => project.slug === route.projectSlug)
  ): Promise<void> {
    if (!routeProject) return;
    const candidate = [...tasks, ...myWorkTasks, ...roadmapLiveTasks, ...issueTasks]
      .find((task) => task.project_id === routeProject.id && (task.key === route.taskReference || task.id === route.taskReference || String(task.number) === route.taskReference));
    let task = candidate;
    if (!task) {
      try {
        const loaded = await api.getTask(route.taskReference);
        if (loaded.project_id === routeProject.id) task = loaded;
      } catch {
        // The route remains stable even if a deleted or inaccessible task is
        // opened; the board error gives the person an actionable explanation.
      }
    }
    if (!task) {
      boardError = `Task ${route.taskReference} could not be found in ${routeProject.name}.`;
      return;
    }
    taskRouteIntent = route.intent;
    taskRouteOrigin = `/p/${encodeURIComponent(routeProject.slug)}`;
    await openTask(task, route.intent);
  }

  function navigate(path: string, replace = false) {
    if (window.location.pathname !== path) {
      window.history[replace ? 'replaceState' : 'pushState']({}, '', path);
    }
  }

  async function selectProject(project: Project, push = true) {
    projectSwitchVersion += 1;
    boardTimelineRequest += 1;
    if (drawerTask) closeDrawer();
    activeProjectSlug = project.slug;
    auditIdFromRoute = '';
    roadmapProjectId = undefined;
    columns = [];
    tasks = [];
    labels = [];
    boardError = '';
    boardTimelineItems = [];
    boardTimelineNextCursor = '';
    boardTimelineError = '';
    boardTimelineProjectId = '';
    recentProjectIds = rememberProject(project.id, localStorage);
    localStorage.setItem('roadmap.last-project', project.slug);
    projectSwitcherOpen = false;
    closeCommandPalette();
    view = 'board';
    if (push) navigate(`/p/${encodeURIComponent(project.slug)}`);
    await loadBoard();
  }

  async function setView(next: View, push = true) {
    view = next;
    projectSwitcherOpen = false;
    closeCommandPalette();
    if (next === 'my-work') {
      if (push) navigate('/my-work');
      await loadMyWork();
    } else if (next === 'issues') {
      if (push) navigate('/issues');
      syncIssueViewURL(issueFilters, issueProjectFilter);
      await loadIssues();
    } else if (next === 'roadmap') {
      roadmapProjectId = undefined;
      if (push) navigate('/roadmap');
      await loadRoadmap();
    } else if (next === 'timeline' && activeProject) {
      roadmapProjectId = undefined;
      if (push) navigate(`/p/${encodeURIComponent(activeProject.slug)}/timeline`);
      if (!columns.length || boardTimelineProjectId !== activeProject.id) await loadBoard();
      await loadBoardTimeline(activeProject.id, { reset: boardTimelineProjectId !== activeProject.id });
    } else if (next === 'audits' && activeProject) {
      roadmapProjectId = undefined;
      auditIdFromRoute = '';
      if (push) navigate(`/p/${encodeURIComponent(activeProject.slug)}/audits`);
      // Audit apply needs the current task/column snapshot. Board data is
      // already warm in the common path; this guarded read fills it for a
      // direct project-audit navigation as well.
      if (!columns.length || !tasks.length) await loadBoard();
    } else if (next === 'settings') {
      if (push) navigate('/settings');
      await loadAgents();
    } else if (activeProject) {
      if (push) navigate(`/p/${encodeURIComponent(activeProject.slug)}`);
      await loadBoard();
    }
  }

  function handlePopState() {
    const slug = getProjectSlugFromLocation();
    const taskRoute = getTaskRouteFromLocation();
    if (slug) {
      const project = projects.find((item) => item.slug === slug);
      if (project && taskRoute) {
        const projectChanged = activeProjectSlug !== project.slug;
        projectSwitchVersion += 1;
        activeProjectSlug = project.slug;
        roadmapProjectId = undefined;
        view = 'board';
        if (projectChanged || !columns.length || !tasks.length) {
          columns = [];
          tasks = [];
          labels = [];
          void loadBoard().then(() => openTaskFromRoute(taskRoute, project));
        } else {
          void openTaskFromRoute(taskRoute, project);
        }
      } else if (project && isProjectTimelineLocation()) {
        const projectChanged = activeProjectSlug !== project.slug;
        projectSwitchVersion += 1;
        activeProjectSlug = project.slug;
        roadmapProjectId = undefined;
        view = 'timeline';
        if (projectChanged || !columns.length || !tasks.length) {
          columns = [];
          tasks = [];
          labels = [];
          void loadBoard().then(() => loadBoardTimeline(project.id, { reset: true }));
        } else {
          void loadBoardTimeline(project.id, { reset: boardTimelineProjectId !== project.id });
        }
      } else if (project && isProjectAuditLocation()) {
        const projectChanged = activeProjectSlug !== project.slug;
        projectSwitchVersion += 1;
        activeProjectSlug = project.slug;
        auditIdFromRoute = getAuditIdFromLocation();
        roadmapProjectId = undefined;
        view = 'audits';
        if (projectChanged || !columns.length || !tasks.length) {
          columns = [];
          tasks = [];
          labels = [];
          void loadBoard();
        }
      } else if (project && isProjectRoadmapLocation()) {
        projectSwitchVersion += 1;
        activeProjectSlug = project.slug;
        roadmapProjectId = project.id;
        view = 'roadmap';
        void loadRoadmap(project.id);
      } else if (project) void selectProject(project, false);
      return;
    }
    if (/^\/my-work\/?$/.test(window.location.pathname)) void setView('my-work', false);
    else if (/^\/issues\/?$/.test(window.location.pathname)) {
      applyIssueRouteFilters(new URL(window.location.href).searchParams);
      void setView('issues', false);
    }
    else if (/^\/roadmap\/?$/.test(window.location.pathname)) void setView('roadmap', false);
    else if (/^\/settings\/?$/.test(window.location.pathname)) void setView('settings', false);
  }

  function rememberDialogFocus(fallbackSelector = '') {
    if (typeof document !== 'undefined') {
      const active = document.activeElement instanceof HTMLElement && document.activeElement !== document.body
        ? document.activeElement
        : null;
      dialogReturnFocus = {
        element: active && isFocusableVisible(active) ? active : null,
        fallbackSelector
      };
    }
  }

  function restoreDialogFocus() {
    const record = dialogReturnFocus;
    dialogReturnFocus = null;
    if (!record) return;
    void tick().then(() => {
      const target = record.element && record.element !== document.body && isFocusableVisible(record.element)
        ? record.element
        : record.fallbackSelector
          ? Array.from(document.querySelectorAll<HTMLElement>(record.fallbackSelector)).find(isFocusableVisible)
          : null;
      target?.focus();
    });
  }

  function closeProjectModal() {
    showProjectModal = false;
    restoreDialogFocus();
  }

  function openCommandPalette() {
    rememberDialogFocus('[data-command-trigger]');
    commandOpen = true;
    projectSwitcherOpen = false;
    commandQuery = '';
    commandIndex = 0;
    if (!issueTasks.length) {
      void listAllIssues({ limit: 200 }).then((result) => {
        issueTasks = result.data;
      }).catch(() => undefined);
    }
    void tick().then(() => commandInput?.focus());
  }

  function closeCommandPalette() {
    if (!commandOpen) return;
    commandOpen = false;
    restoreDialogFocus();
  }

  function closeTaskModal() {
    showTaskModal = false;
    taskModalLoading = false;
    taskModalColumnsRequest += 1;
    restoreDialogFocus();
  }

  function closeBugModal() {
    showBugModal = false;
    bugModalLoading = false;
    restoreDialogFocus();
  }

  function closeTokenReveal() {
    revealedToken = null;
    restoreDialogFocus();
  }

  function handleKeydown(event: KeyboardEvent) {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      openCommandPalette();
    } else if (event.key === 'Escape') {
      if (commandOpen) closeCommandPalette();
      else if (projectSwitcherOpen) projectSwitcherOpen = false;
      else if (showProjectModal) closeProjectModal();
      else if (showTaskModal) closeTaskModal();
      else if (showBugModal) closeBugModal();
      else if (revealedToken) closeTokenReveal();
      else if (drawerTask) closeDrawer();
    }
  }

  function buildCommandChoices(query: string): CommandChoice[] {
    const normalized = query.trim().toLowerCase();
    const choices: CommandChoice[] = [
      { kind: 'view', id: 'issues', view: 'issues', label: 'Issues', hint: 'Track and triage reported bugs' },
      { kind: 'view', id: 'my-work', view: 'my-work', label: 'My work', hint: 'Assigned and claimed tasks' },
      { kind: 'view', id: 'roadmap', view: 'roadmap', label: 'Roadmap overview', hint: 'Progress across every project' },
      { kind: 'view', id: 'settings', view: 'settings', label: 'Settings', hint: 'Agents, tokens, and appearance' },
      ...projects.map((project) => ({ kind: 'project' as const, id: project.id, project, label: project.name, hint: project.key })),
      ...issueTasks.map((task) => ({ kind: 'issue' as const, id: task.id, task, label: task.title, hint: `${task.key} · ${task.bug?.severity?.toUpperCase() || 'Untriaged'}` }))
    ];
    return normalized
      ? choices.filter((choice) => `${choice.label} ${choice.hint}`.toLowerCase().includes(normalized))
      : choices;
  }

  function commandKeydown(event: KeyboardEvent) {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      commandIndex = Math.min(commandIndex + 1, Math.max(0, commandChoices.length - 1));
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      commandIndex = Math.max(commandIndex - 1, 0);
    } else if (event.key === 'Enter') {
      event.preventDefault();
      const choice = commandChoices[commandIndex];
      if (choice) void selectCommand(choice);
    }
  }

  async function selectCommand(choice: CommandChoice) {
    if (choice.kind === 'project' && choice.project) await selectProject(choice.project);
    else if (choice.kind === 'issue' && choice.task) {
      commandOpen = false;
      await setView('issues');
      await openWorkTask(issueTasks.find((task) => task.id === choice.task?.id) || choice.task);
    }
    else if (choice.view) await setView(choice.view);
  }

  async function openProjectRoadmap() {
    if (!activeProject) return;
    roadmapProjectId = activeProject.id;
    view = 'roadmap';
    navigate(`/p/${encodeURIComponent(activeProject.slug)}/roadmap`);
    await loadRoadmap(activeProject.id);
  }

  async function openProjectTimeline() {
    await setView('timeline');
  }

  function boardViewKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next: View = event.key === 'ArrowLeft' || event.key === 'Home' ? 'board' : 'timeline';
    void setView(next).then(() => requestAnimationFrame(() => document.getElementById(`board-view-${next}`)?.focus()));
  }

  async function openProjectAudits() {
    if (!activeProject) return;
    auditIdFromRoute = '';
    roadmapProjectId = undefined;
    view = 'audits';
    navigate(`/p/${encodeURIComponent(activeProject.slug)}/audits`);
    if (!columns.length || !tasks.length) await loadBoard();
  }

  function navigateAudit(path: string) {
    const match = path.match(/^\/p\/[^/]+\/audits(?:\/([^/]+))?\/?$/);
    auditIdFromRoute = match?.[1] ? decodeURIComponent(match[1]) : '';
    view = 'audits';
    navigate(path);
  }

  function openProjectModal() {
    rememberDialogFocus('[data-project-modal-trigger], [data-project-picker-trigger]');
    projectKeyDraft = '';
    projectNameDraft = '';
    projectDescriptionDraft = '';
    projectColorDraft = '#6d5efc';
    projectFormError = '';
    showProjectModal = true;
    projectSwitcherOpen = false;
  }

  async function createProject() {
    projectCreating = true;
    projectFormError = '';
    try {
      const key = projectKeyDraft.trim().toUpperCase();
      if (!/^[A-Z][A-Z0-9_-]{0,15}$/.test(key)) throw new Error('Use 1–16 uppercase letters or numbers for the project key.');
      if (!projectNameDraft.trim()) throw new Error('Give the project a name.');
      const project = await api.createProject({
        key,
        name: projectNameDraft.trim(),
        description: projectDescriptionDraft.trim(),
        color: projectColorDraft,
        favorite: true
      });
      projects = [...projects, project];
      closeProjectModal();
      toast('success', `${project.name} is ready to plan.`);
      await selectProject(project);
    } catch (error) {
      projectFormError = friendlyError(error, 'The project could not be created.');
    } finally {
      projectCreating = false;
    }
  }

  async function openTaskModal() {
    if (!projects.length) {
      openProjectModal();
      return;
    }
    taskModalProjectId = activeProject?.id || projects[0].id;
    taskModalColumnId = '';
    taskModalColumns = activeProject?.id === taskModalProjectId ? [...columns] : [];
    taskModalTitle = '';
    taskModalDescription = '';
    taskModalPriority = 'normal';
    taskModalDueDate = '';
    taskModalAssignee = '';
    taskModalError = '';
    rememberDialogFocus('[data-task-modal-trigger]');
    showTaskModal = true;
    projectSwitcherOpen = false;
    if (!taskModalColumns.length) await loadTaskModalColumns(taskModalProjectId);
    else taskModalColumnId = taskModalColumns.find((column) => column.semantic_state === 'ready')?.id || taskModalColumns[0]?.id || '';
  }

  async function loadTaskModalColumns(projectId: string) {
    if (!projectId) return;
    const requestId = ++taskModalColumnsRequest;
    const requestedProjectId = projectId;
    taskModalLoading = true;
    try {
      const nextColumns = projectId === activeProject?.id ? [...columns] : (await api.listAllColumns(projectId)).data;
      if (requestId !== taskModalColumnsRequest || !showTaskModal || taskModalProjectId !== requestedProjectId) return;
      taskModalColumns = nextColumns;
      taskModalColumnId = taskModalColumns.find((column) => column.semantic_state === 'ready')?.id || taskModalColumns[0]?.id || '';
    } catch (error) {
      if (requestId === taskModalColumnsRequest && showTaskModal && taskModalProjectId === requestedProjectId) {
        taskModalError = friendlyError(error, 'This project’s columns could not be loaded.');
      }
    } finally {
      if (requestId === taskModalColumnsRequest) taskModalLoading = false;
    }
  }

  async function createGlobalTask() {
    if (!taskModalProjectId || !taskModalTitle.trim()) {
      taskModalError = 'Choose a project and add a title.';
      return;
    }
    taskModalCreating = true;
    taskModalError = '';
    try {
      const projectTasks = taskModalProjectId === activeProject?.id ? tasks : [];
      const created = await api.createTask(taskModalProjectId, {
        title: taskModalTitle.trim(),
        description: taskModalDescription.trim(),
        priority: taskModalPriority,
        column_id: taskModalColumnId || undefined,
        position: taskModalColumnId ? nextPosition(projectTasks, taskModalColumnId) : undefined,
        due_at: dateToIso(taskModalDueDate),
        assignee: taskModalAssignee.trim() || null
      });
      recordTaskMutation(created.id, 'upsert', ['board']);
      if (taskModalProjectId === activeProject?.id) tasks = [...tasks, created];
      closeTaskModal();
      toast('success', `${created.key} created in ${taskModalProject?.name || 'your project'}.`);
    } catch (error) {
      taskModalError = friendlyError(error, 'The task could not be created.');
    } finally {
      taskModalCreating = false;
    }
  }

  async function openBugModal() {
    if (!projects.length) {
      openProjectModal();
      return;
    }
    bugModalProjectId = activeProject?.id || projects[0].id;
    bugModalColumnId = '';
    bugModalColumns = activeProject?.id === bugModalProjectId ? [...columns] : [];
    bugModalTitle = '';
    bugModalDescription = '';
    bugModalActual = '';
    bugModalExpected = '';
    bugModalReproduction = '';
    bugModalEnvironment = '';
    bugModalVersion = '';
    bugModalLabels = '';
    bugModalSeverity = '';
    bugModalPriority = 'normal';
    bugModalError = '';
    rememberDialogFocus('[data-report-bug-trigger]');
    showBugModal = true;
    projectSwitcherOpen = false;
    if (!bugModalColumns.length) {
      bugModalLoading = true;
      try {
        bugModalColumns = bugModalProjectId === activeProject?.id
          ? [...columns]
          : (await api.listAllColumns(bugModalProjectId)).data;
        bugModalColumnId = bugModalColumns.find((column) => column.semantic_state === 'ready')?.id || bugModalColumns[0]?.id || '';
      } catch (error) {
        bugModalError = friendlyError(error, 'This project’s columns could not be loaded.');
      } finally {
        bugModalLoading = false;
      }
    } else {
      bugModalColumnId = bugModalColumns.find((column) => column.semantic_state === 'ready')?.id || bugModalColumns[0]?.id || '';
    }
  }

  async function reportBug() {
    if (!bugModalProjectId || !bugModalTitle.trim() || !bugModalActual.trim()) {
      bugModalError = 'Choose a project, add a title, and describe what actually happened.';
      return;
    }
    bugModalCreating = true;
    bugModalError = '';
    try {
      const labelNames = bugModalLabels.split(',').map((value) => value.trim()).filter(Boolean);
      const labelIds = await resolveTaskLabels(bugModalProjectId, labelNames);
      const projectTasks = bugModalProjectId === activeProject?.id ? tasks : [];
      const created = await api.createTask(bugModalProjectId, {
        title: bugModalTitle.trim(),
        description: bugModalDescription.trim(),
        kind: 'bug',
        priority: bugModalPriority,
        column_id: bugModalColumnId || undefined,
        position: bugModalColumnId ? nextPosition(projectTasks, bugModalColumnId) : undefined,
        labels: labelIds,
        bug: {
          actual_behavior: bugModalActual.trim(),
          expected_behavior: bugModalExpected.trim(),
          reproduction_steps: bugModalReproduction.trim(),
          environment: bugModalEnvironment.trim(),
          affected_version: bugModalVersion.trim(),
          ...(bugModalSeverity ? { severity: bugModalSeverity } : {})
        }
      });
      const createdScopes: TaskMutationScope[] = ['issues'];
      if (bugModalProjectId === activeProject?.id) createdScopes.push('board');
      recordTaskMutation(created.id, 'upsert', createdScopes);
      issueTasks = [...issueTasks.filter((task) => task.id !== created.id), created];
      if (bugModalProjectId === activeProject?.id) tasks = [...tasks, created];
      closeBugModal();
      toast('success', `${created.key} reported.`);
    } catch (error) {
      bugModalError = friendlyError(error, 'The bug could not be reported.');
    } finally {
      bugModalCreating = false;
    }
  }

  async function changeBugModalProject() {
    bugModalColumns = [];
    bugModalColumnId = '';
    bugModalError = '';
    if (!bugModalProjectId) return;
    bugModalLoading = true;
    try {
      bugModalColumns = bugModalProjectId === activeProject?.id
        ? [...columns]
        : (await api.listAllColumns(bugModalProjectId)).data;
      bugModalColumnId = bugModalColumns.find((column) => column.semantic_state === 'ready')?.id || bugModalColumns[0]?.id || '';
    } catch (error) {
      bugModalError = friendlyError(error, 'This project’s columns could not be loaded.');
    } finally {
      bugModalLoading = false;
    }
  }

  async function toggleFavorite(event: MouseEvent, project: Project) {
    event.stopPropagation();
    try {
      const updated = await api.patchProject(project.id, { favorite: !project.favorite });
      projects = projects.map((item) => (item.id === project.id ? { ...item, ...updated } : item));
    } catch (error) {
      toast('error', friendlyError(error, 'Favorite status could not be saved.'));
    }
  }

  function updateFilter(name: keyof BoardFilters, value: string) {
    filters = { ...filters, [name]: value };
  }

  function clearFilters() {
    filters = { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all' };
    boardWorkFilter = 'all';
  }

  function clearIssueFilters() {
    issueFilters = {
      query: '',
      priority: 'all',
      label: 'all',
      assignee: 'all',
      state: 'all',
      kind: 'bug',
      severity: 'all',
      reporter: 'all',
      resolution: 'all'
    };
    issueProjectFilter = 'all';
  }

  function applyIssueRouteFilters(params: URLSearchParams) {
    const value = (name: string) => params.get(name)?.trim() || '';
    const priority = value('priority');
    const state = value('state');
    const severity = value('severity');
    const resolution = value('resolution');
    issueFilters = {
      ...issueFilters,
      query: value('q'),
      priority: ['low', 'normal', 'high', 'urgent'].includes(priority) ? priority : 'all',
      state: ['backlog', 'ready', 'active', 'blocked', 'completed'].includes(state) ? state : 'all',
      assignee: value('assignee') || 'all',
      severity: ['s1', 's2', 's3', 's4', 'untriaged'].includes(severity) ? severity : 'all',
      reporter: value('reporter') || 'all',
      resolution: ['fixed', 'duplicate', 'not_planned', 'cannot_reproduce', 'works_as_designed', 'open'].includes(resolution) ? resolution : 'all'
    };
    issueProjectFilter = value('project') || 'all';
  }

  function syncIssueViewURL(current: BoardFilters, project: string) {
    if (typeof window === 'undefined' || window.location.pathname !== '/issues') return;
    const params = new URLSearchParams();
    if (current.query) params.set('q', current.query);
    if (current.priority !== 'all') params.set('priority', current.priority);
    if (current.state !== 'all') params.set('state', current.state);
    if (current.assignee !== 'all') params.set('assignee', current.assignee);
    if (current.severity && current.severity !== 'all') params.set('severity', current.severity);
    if (current.reporter && current.reporter !== 'all') params.set('reporter', current.reporter);
    if (current.resolution && current.resolution !== 'all') params.set('resolution', current.resolution);
    if (project !== 'all') params.set('project', project);
    const query = params.toString();
    const target = `/issues${query ? `?${query}` : ''}`;
    if (`${window.location.pathname}${window.location.search}` !== target) window.history.replaceState({}, '', target);
  }

  function columnColor(column: Column): string {
    return ({ backlog: '#a4aab8', ready: '#4b9cf5', active: '#6d5efc', blocked: '#ec6b75', completed: '#35b88a' } as Record<string, string>)[column.semantic_state] || '#a4aab8';
  }

  function dragStart(event: DragEvent, task: Task) {
    draggingTaskId = task.id;
    event.dataTransfer?.setData('text/plain', task.id);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move';
  }

  async function dropTask(event: DragEvent, destinationColumnId: string) {
    event.preventDefault();
    const taskId = event.dataTransfer?.getData('text/plain') || draggingTaskId;
    draggingTaskId = '';
    const task = tasks.find((item) => item.id === taskId);
    if (!task || task.column_id === destinationColumnId || taskActionLoading) return;
    await moveTask(task, destinationColumnId);
  }

  async function moveTask(task: Task, destinationColumnId: string) {
    taskActionLoading = task.id;
    try {
      const updated = await api.patchTask(task.id, { column_id: destinationColumnId, position: nextPosition(tasks, destinationColumnId) }, task.version);
      replaceTask(updated, true);
      toast('success', `${task.key} moved to ${columns.find((column) => column.id === destinationColumnId)?.name || 'another column'}.`);
    } catch (error) {
      toast('error', friendlyError(error, 'The task could not be moved. Refresh and try again.'));
    } finally {
      taskActionLoading = '';
    }
  }

  function keyboardMove(event: KeyboardEvent, task: Task) {
    if (!(event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight'))) return;
    event.preventDefault();
    const index = sortedColumns.findIndex((column) => column.id === task.column_id);
    const destination = sortedColumns[index + (event.key === 'ArrowLeft' ? -1 : 1)];
    if (destination) void moveTask(task, destination.id);
  }

  function replaceTask(updated: Task, localMutation = false) {
    const issueHasTask = issueTasks.some((task) => task.id === updated.id);
    const existing = [
      drawerTask?.id === updated.id ? drawerTask : undefined,
      tasks.find((task) => task.id === updated.id),
      issueTasks.find((task) => task.id === updated.id),
      myWorkTasks.find((task) => task.id === updated.id)
    ].filter((task): task is Task => Boolean(task));
    const previous = existing.reduce<Task | undefined>(
      (current, task) => !current || task.version > current.version ? task : current,
      undefined
    );
    const nextTask = mergeAuthoritativeTask(previous, updated);
    tasks = tasks.some((task) => task.id === updated.id)
      ? tasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task))
      : [nextTask, ...tasks];
    if (issueTasks.some((task) => task.id === updated.id)) {
      issueTasks = issueTasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task));
    }
    if (drawerTask?.id === updated.id) {
      drawerTask = mergeAuthoritativeTask(drawerTask, nextTask);
    }
    const myWorkIndex = myWorkTasks.findIndex((task) => task.id === updated.id);
    const belongsToUser = actorId(nextTask.assignee) === user?.id
      || (actorId(nextTask.claimed_by) === user?.id && claimIsActive(nextTask, pulseClock));
    const keepInMyWork = myWorkView === 'live'
      ? !nextTask.completed_at && Boolean(nextTask.agent_work)
      : belongsToUser;
    if (localMutation) {
      const mutations: Partial<Record<TaskMutationScope, TaskMutationKind>> = { board: 'upsert' };
      if (issueHasTask) mutations.issues = 'upsert';
      if (myWorkIndex >= 0 || keepInMyWork) {
        mutations[myWorkMutationScope()] = myWorkIndex >= 0 && !keepInMyWork ? 'remove' : 'upsert';
      }
      recordScopedTaskMutation(updated.id, mutations);
    }
    if (myWorkIndex >= 0) {
      myWorkTasks = keepInMyWork
        ? myWorkTasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task))
        : myWorkTasks.filter((task) => task.id !== updated.id);
    } else if (keepInMyWork) {
      myWorkTasks = [...myWorkTasks, nextTask];
    }
    const previousTransition = previous ? workTransitionKey(previous) : '';
    const nextTransition = workTransitionKey(nextTask);
    if (previousTransition && nextTransition && previousTransition !== nextTransition) {
      const status = isWorkStale(nextTask) && workState(nextTask) !== 'waiting' ? 'stale' : workState(nextTask) || 'updated';
      announce(`${nextTask.key} is now ${status}${isActionNeeded(nextTask) ? ' · action needed' : ''}.`);
    }
  }

  async function submitQuickAdd(columnId: string) {
    const title = (quickAddTitle[columnId] || '').trim();
    if (!title || !activeProject) return;
    taskActionLoading = `create-${columnId}`;
    try {
      const created = await api.createTask(activeProject.id, {
        title,
        column_id: columnId,
        position: nextPosition(tasks, columnId),
        priority: 'normal'
      });
      recordTaskMutation(created.id, 'upsert', ['board']);
      tasks = [...tasks, created];
      quickAddTitle = { ...quickAddTitle, [columnId]: '' };
      quickAddColumn = '';
      toast('success', `${created.key} added to ${columns.find((column) => column.id === columnId)?.name || 'the board'}.`);
    } catch (error) {
      toast('error', friendlyError(error, 'The task could not be created.'));
    } finally {
      taskActionLoading = '';
    }
  }

  function timelineKindForFilter(filter: TaskTimelineFilter): TaskTimelineKind | undefined {
    return filter === 'all' ? undefined : filter;
  }

  function mergeDrawerTimeline(existing: TaskTimelineItem[], incoming: TaskTimelineItem[]): TaskTimelineItem[] {
    const merged = new Map<string, TaskTimelineItem>();
    [...existing, ...incoming].forEach((item) => merged.set(item.id, item));
    return [...merged.values()].sort((a, b) => {
      const aTime = Date.parse(a.created_at) || 0;
      const bTime = Date.parse(b.created_at) || 0;
      // Modern browsers guarantee stable Array#sort, so equal timestamps
      // retain the API's deterministic cursor order.
      return bTime - aTime;
    });
  }

  async function loadDrawerTimeline(
    taskId = drawerTask?.id,
    options: { older?: boolean } = {}
  ): Promise<boolean> {
    if (!taskId) return false;
    const older = Boolean(options.older);
    if (drawerTimelineLoading || drawerTimelineLoadingOlder) return true;
    const requestId = ++drawerTimelineRequest;
    const requestedSession = sessionGeneration;
    const requestedFilter = drawerTimelineFilter;
    const requestedTaskId = taskId;
    const previousItems = drawerTimelineItems;
    const previousCursor = drawerTimelineNextCursor;
    const hadItems = previousItems.length > 0 && drawerTimelineTaskId === taskId;
    if (older) drawerTimelineLoadingOlder = true;
    else drawerTimelineLoading = true;
    drawerTimelineTaskId = taskId;
    drawerTimelineError = '';
    try {
      const result = await api.listTaskTimeline(taskId, {
        before: older ? previousCursor || undefined : undefined,
        limit: 50,
        kind: timelineKindForFilter(requestedFilter)
      });
      if (
        requestId !== drawerTimelineRequest
        || requestedSession !== sessionGeneration
        || drawerTask?.id !== requestedTaskId
        || drawerTimelineTaskId !== requestedTaskId
        || drawerTimelineFilter !== requestedFilter
        || !user
      ) return false;
      drawerTimelineItems = mergeDrawerTimeline(older ? previousItems : hadItems ? previousItems : [], result.data || []);
      // A refresh of a list that already includes older pages must retain the
      // existing keyset boundary; the first page's cursor would otherwise
      // replay rows the user has already loaded.
      drawerTimelineNextCursor = older || !hadItems ? (result.next_cursor || '') : previousCursor;
      return true;
    } catch (error) {
      if (
        requestId === drawerTimelineRequest
        && requestedSession === sessionGeneration
        && drawerTask?.id === requestedTaskId
        && drawerTimelineFilter === requestedFilter
      ) drawerTimelineError = friendlyError(error, 'Task activity could not be loaded.');
      return false;
    } finally {
      if (requestId === drawerTimelineRequest) {
        if (older) drawerTimelineLoadingOlder = false;
        else drawerTimelineLoading = false;
      }
    }
  }

  function setDrawerView(next: DrawerView) {
    if (!drawerTask) return;
    drawerView = next;
    taskRouteIntent = next;
    const route = getTaskRouteFromLocation();
    if (route) {
      const path = taskDeepLink(route.projectSlug, route.taskReference, next);
      if (`${window.location.pathname}${window.location.search}` !== path) window.history.replaceState({}, '', path);
    }
    if (
      next === 'activity'
      && drawerTimelineTaskId === drawerTask.id
      && drawerTimelineError
    ) void loadDrawerTimeline(drawerTask.id);
    else if (next === 'activity' && drawerTimelineTaskId !== drawerTask.id) void loadDrawerTimeline(drawerTask.id);
  }

  function drawerTabKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const next: DrawerView = event.key === 'ArrowLeft' || event.key === 'Home' ? 'details' : 'activity';
    setDrawerView(next);
    requestAnimationFrame(() => document.getElementById(`drawer-${next}-tab`)?.focus());
  }

  function setDrawerTimelineFilter(next: TaskTimelineFilter) {
    if (!drawerTask || drawerTimelineFilter === next) return;
    drawerTimelineFilter = next;
    drawerTimelineRequest += 1;
    drawerTimelineItems = [];
    drawerTimelineNextCursor = '';
    drawerTimelineTaskId = drawerTask.id;
    drawerTimelineError = '';
    drawerTimelineLoading = false;
    drawerTimelineLoadingOlder = false;
    void loadDrawerTimeline(drawerTask.id);
  }

  function loadOlderDrawerActivity() {
    if (drawerTimelineNextCursor) void loadDrawerTimeline(drawerTask?.id, { older: true });
  }

  async function openTask(task: Task, intent: TaskRouteIntent = taskRouteIntent) {
    const requestId = ++taskDetailRequest;
    rememberDialogFocus('[data-task-trigger], .work-row');
    taskRouteIntent = intent;
    drawerView = intent === 'activity' ? 'activity' : 'details';
    drawerTask = task;
    drawerError = '';
    commentBody = '';
    drawerTimelineItems = [];
    drawerTimelineNextCursor = '';
    drawerTimelineTaskId = task.id;
    drawerTimelineError = '';
    drawerTimelineFilter = 'all';
    drawerTimelineRequest += 1;
    drawerTimelineLoading = false;
    drawerTimelineLoadingOlder = false;
    blockReasonDraft = '';
    blockReasonOpen = false;
    syncDraft(task);
    drawerLoading = true;
    void loadDrawerTimeline(task.id);
    try {
      const detail = await api.getTask(task.id);
      if (requestId !== taskDetailRequest || drawerTask?.id !== task.id) return;
      replaceTask(detail);
      syncDraft(detail);
    } catch (error) {
      if (requestId === taskDetailRequest && drawerTask?.id === task.id) {
        drawerError = friendlyError(error, 'Some task details could not be loaded.');
      }
    } finally {
      if (requestId === taskDetailRequest && drawerTask?.id === task.id) drawerLoading = false;
    }
  }

  async function refreshDrawerTask(taskId: string): Promise<boolean> {
    if (!drawerTask || drawerTask.id !== taskId) return true;
    if (drawerLivenessInFlight?.taskId === taskId && drawerLivenessInFlight.requestId === drawerLivenessRequest) {
      return drawerLivenessInFlight.promise;
    }
    const requestId = ++drawerLivenessRequest;
    const requestedSession = sessionGeneration;
    const refresh = (async () => {
      try {
        const updated = await api.getTask(taskId);
        if (
          drawerLivenessRequest !== requestId
          || drawerTask?.id !== taskId
          || sessionGeneration !== requestedSession
          || !user
        ) return false;
        // Authoritative event refreshes update the task model only. Draft fields
        // remain untouched so a background pulse cannot erase in-progress edits.
        replaceTask(updated);
        return true;
      } catch {
        // The next event or the drawer's explicit refresh can retry this read.
        return false;
      }
    })();
    drawerLivenessInFlight = { taskId, requestId, promise: refresh };
    try {
      return await refresh;
    } finally {
      if (drawerLivenessInFlight?.promise === refresh) drawerLivenessInFlight = null;
    }
  }

  function handleDependencyTaskUpdated(updated: Task) {
    // Dependency mutations are committed independently from the drawer form.
    // Merge the new version and summary without replacing unsaved draft fields.
    replaceTask(updated, true);
  }

  async function refreshDependencyTask(): Promise<void> {
    if (drawerTask) await refreshDrawerTask(drawerTask.id);
  }

  async function openDependencyTask(reference: TaskReference): Promise<void> {
    const sourceTask = drawerTask;
    if (!sourceTask) return;
    const project = projectForTask(sourceTask);
    if (!project) {
      drawerError = 'The linked task project could not be found.';
      return;
    }
    try {
      const related = await api.getTask(reference.id);
      if (related.project_id !== sourceTask.project_id) {
        throw new Error('Linked tasks must belong to the same project.');
      }
      if (!taskRouteOrigin) taskRouteOrigin = `${window.location.pathname}${window.location.search}`;
      navigate(taskDeepLink(project.slug, related.key, 'details'));
      await openTask(related, 'details');
    } catch (error) {
      if (drawerTask?.id === sourceTask.id) {
        drawerError = friendlyError(error, 'The linked task could not be opened.');
      }
    }
  }

  function syncDraft(task: Task) {
    draftTitle = task.title;
    draftDescription = task.description || '';
    draftPriority = task.priority;
    draftDueDate = toInputDate(task.due_at);
    draftAssignee = actorId(task.assignee);
    draftLabels = (task.labels || []).map((label) => label.name).join(', ');
    draftBugActual = task.bug?.actual_behavior || '';
    draftBugExpected = task.bug?.expected_behavior || '';
    draftBugReproduction = task.bug?.reproduction_steps || '';
    draftBugEnvironment = task.bug?.environment || '';
    draftBugVersion = task.bug?.affected_version || '';
    triageSeverityDraft = task.bug?.severity || 's3';
    resolutionDraft = task.bug?.resolution || 'fixed';
    duplicateOfDraft = task.bug?.duplicate_of || '';
    resolutionNoteDraft = '';
    reopenReasonDraft = '';
  }

  function findProjectLabel(projectId: string, value: string): Label | undefined {
    const normalized = value.trim().toLowerCase();
    return labels.find(
      (label) =>
        label.project_id === projectId &&
        (label.id === value.trim() || label.name.trim().toLowerCase() === normalized)
    );
  }

  function mergeProjectLabels(projectId: string, next: Label[]) {
    const projectLabels = new Map(labels.filter((label) => label.project_id === projectId).map((label) => [label.id, label]));
    next.filter((label) => label.project_id === projectId).forEach((label) => projectLabels.set(label.id, label));
    labels = [...labels.filter((label) => label.project_id !== projectId), ...projectLabels.values()];
  }

  async function reloadProjectLabels(projectId: string): Promise<Label[]> {
    const refreshed = (await api.listAllLabels(projectId)).data;
    labels = [...labels.filter((label) => label.project_id !== projectId), ...refreshed];
    return refreshed;
  }

  function unresolvedLabelError(names: string[]): Error {
    const quoted = names.map((name) => `"${name}"`).join(', ');
    return new Error(`Could not resolve label${names.length === 1 ? '' : 's'}: ${quoted}. Check the names and try again.`);
  }

  async function resolveTaskLabels(projectId: string, names: string[]): Promise<string[]> {
    const uniqueNames = Array.from(
      new Map(names.map((name) => [name.trim().toLowerCase(), name.trim()])).values()
    );
    const resolved: Label[] = [];
    const unresolved: string[] = [];

    for (const name of uniqueNames) {
      let label = findProjectLabel(projectId, name);
      if (!label) {
        try {
          const projectLabelCount = labels.filter((item) => item.project_id === projectId).length;
          const created = await api.createLabel(projectId, {
            name,
            color: labelPalette[projectLabelCount % labelPalette.length]
          });
          mergeProjectLabels(projectId, [created]);
          label = findProjectLabel(projectId, name);
        } catch {
          // A concurrent actor may have created this label between our local
          // lookup and POST. Refresh the authoritative list before failing.
          try {
            await reloadProjectLabels(projectId);
          } catch {
            // The unresolved-label error below remains actionable even when
            // the recovery request itself is unavailable.
          }
          label = findProjectLabel(projectId, name);
        }
      }
      if (label) resolved.push(label);
      else unresolved.push(name);
    }

    if (unresolved.length) throw unresolvedLabelError(unresolved);

    return resolved.map((label) => label.id);
  }

  function closeDrawer() {
    const routeOrigin = taskRouteOrigin;
    taskDetailRequest += 1;
    drawerLivenessRequest += 1;
    drawerTask = null;
    drawerDependencyPanel = null;
    taskRouteOrigin = '';
    taskRouteIntent = 'details';
    drawerView = 'details';
    drawerError = '';
    drawerTimelineRequest += 1;
    drawerTimelineItems = [];
    drawerTimelineNextCursor = '';
    drawerTimelineTaskId = '';
    drawerTimelineError = '';
    drawerTimelineLoading = false;
    drawerTimelineLoadingOlder = false;
    blockReasonDraft = '';
    blockReasonOpen = false;
    if (isTaskLocation()) navigate(routeOrigin || `/p/${encodeURIComponent(activeProjectSlug)}`);
    restoreDialogFocus();
  }

  async function deleteProjectLabel(label: Label) {
    if (!window.confirm(`Delete ${label.name}? It will be removed from tasks.`)) return;
    labelDeleting = label.id;
    try {
      await api.deleteLabel(label.id);
      labels = labels.filter((item) => item.id !== label.id);
      if (filters.label === label.id) filters = { ...filters, label: 'all' };
      tasks = tasks.map((task) => task.labels?.some((item) => item.id === label.id)
        ? { ...task, labels: task.labels.filter((item) => item.id !== label.id) }
        : task);
      if (drawerTask?.project_id === label.project_id) {
        drawerTask = { ...drawerTask, labels: (drawerTask.labels || []).filter((item) => item.id !== label.id) };
        draftLabels = draftLabels
          .split(',')
          .map((value) => value.trim())
          .filter((value) => value && value.toLowerCase() !== label.name.toLowerCase())
          .join(', ');
      }
      toast('success', `${label.name} deleted.`);
    } catch (error) {
      toast('error', friendlyError(error, 'The label could not be deleted.'));
    } finally {
      labelDeleting = '';
    }
  }

  async function saveTask() {
    if (!drawerTask || !draftTitle.trim()) {
      drawerError = 'A task needs a title.';
      return;
    }
    if (drawerTask.kind === 'bug' && !draftBugActual.trim()) {
      drawerError = 'A bug report needs actual behavior.';
      return;
    }
    drawerSaving = true;
    drawerError = '';
    try {
      const labelNames = draftLabels.split(',').map((value) => value.trim()).filter(Boolean);
      const labelIds = await resolveTaskLabels(drawerTask.project_id, labelNames);
      const updated = await api.patchTask(
        drawerTask.id,
        {
          title: draftTitle.trim(),
          description: draftDescription,
          priority: draftPriority,
          due_at: dateToIso(draftDueDate),
          assignee: draftAssignee.trim() || null,
          labels: labelIds,
          label_ids: labelIds,
          ...(drawerTask.kind === 'bug'
            ? {
                bug: {
                  actual_behavior: draftBugActual.trim(),
                  expected_behavior: draftBugExpected.trim(),
                  reproduction_steps: draftBugReproduction.trim(),
                  environment: draftBugEnvironment.trim(),
                  affected_version: draftBugVersion.trim()
                }
              }
            : {})
        },
        drawerTask.version
      );
      replaceTask(updated, true);
      syncDraft(updated);
      toast('success', `${updated.key} saved.`);
    } catch (error) {
      drawerError = friendlyError(error, 'The task changed elsewhere. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) {
        const current = error.details.current as Task;
        replaceTask(current);
        drawerError = 'This task changed in another session. Your draft was not overwritten.';
      }
    } finally {
      drawerSaving = false;
    }
  }

  async function deleteDrawerTask() {
    const task = drawerTask;
    if (!task || !window.confirm(`Delete ${task.key}? This cannot be undone.`)) return;
    taskActionLoading = task.id;
    drawerError = '';
    try {
      await api.deleteTask(task.id, task.version);
      recordTaskMutation(task.id, 'remove', ['board', 'issues', 'my-work-live', 'my-work-assigned']);
      tasks = tasks.filter((item) => item.id !== task.id);
      issueTasks = issueTasks.filter((item) => item.id !== task.id);
      myWorkTasks = myWorkTasks.filter((item) => item.id !== task.id);
      closeDrawer();
      toast('success', `${task.key} deleted.`);
    } catch (error) {
      drawerError = friendlyError(error, 'The task could not be deleted. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) {
        replaceTask(error.details.current as Task);
        drawerError = 'This task changed in another session. Refresh and try again.';
      }
    } finally {
      taskActionLoading = '';
    }
  }

  async function runTaskAction(action: 'claim' | 'renew' | 'release' | 'complete' | 'block', reason = '') {
    if (!drawerTask) return;
    if (action === 'block' && !reason.trim()) {
      drawerError = 'Add a reason before blocking this task.';
      blockReasonOpen = true;
      return;
    }
    if (action === 'claim' && claimConflict(drawerTask, pulseClock)) {
      drawerError = `Claim conflict: ${claimOwnerLabel(drawerTask)} holds this task until ${claimExpiryExact(drawerTask.claim_expires_at)} (${claimCountdown(drawerTask, pulseClock)}).`;
      return;
    }
    taskActionLoading = drawerTask.id;
    drawerError = '';
    try {
      let updated: Task;
      if (action === 'claim') updated = await api.claimTask(drawerTask.id, drawerTask.version);
      else if (action === 'renew') updated = await api.renewTask(drawerTask.id, drawerTask.version);
      else if (action === 'release') updated = await api.releaseTask(drawerTask.id, drawerTask.version);
      else if (action === 'complete') updated = await api.completeTask(drawerTask.id, drawerTask.version);
      else updated = await api.blockTask(drawerTask.id, drawerTask.version, reason.trim());
      replaceTask(updated, true);
      if (action === 'block') {
        blockReasonOpen = false;
        blockReasonDraft = '';
      }
      const actionLabel = action === 'renew' ? 'claim renewed' : action === 'release' ? 'released' : action === 'complete' ? 'completed' : `${action}ed`;
      toast('success', `${updated.key} ${actionLabel}.`);
    } catch (error) {
      drawerError = friendlyError(error, 'That task action could not be completed.');
      if (error instanceof ApiError && error.details.current) {
        replaceTask(error.details.current as Task);
        drawerError = action === 'claim'
          ? 'Claim conflict: this task changed or is held by another actor. Review the current lease below.'
          : 'This task changed in another session. Your draft was not overwritten.';
      }
    } finally {
      taskActionLoading = '';
    }
  }

  function openBlockReason() {
    blockReasonOpen = true;
    blockReasonDraft = '';
    drawerError = '';
  }

  async function triageBug() {
    if (!drawerTask?.bug) return;
    taskActionLoading = drawerTask.id;
    drawerError = '';
    try {
      const updated = await api.triageTask(drawerTask.id, drawerTask.version, {
        severity: triageSeverityDraft,
        priority: draftPriority,
        assignee: draftAssignee.trim() || null
      });
      replaceTask(updated, true);
      syncDraft(updated);
      toast('success', `${updated.key} triaged as ${triageSeverityDraft.toUpperCase()}.`);
    } catch (error) {
      drawerError = friendlyError(error, 'The issue could not be triaged. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) replaceTask(error.details.current as Task);
    } finally {
      taskActionLoading = '';
    }
  }

  async function resolveBug() {
    if (!drawerTask?.bug) return;
    if (resolutionDraft === 'duplicate' && !duplicateOfDraft.trim()) {
      drawerError = 'Add the task key or ID this issue duplicates.';
      return;
    }
    taskActionLoading = drawerTask.id;
    drawerError = '';
    try {
      const updated = await api.resolveTask(drawerTask.id, drawerTask.version, {
        resolution: resolutionDraft,
        duplicate_of: duplicateOfDraft.trim() || null,
        note: resolutionNoteDraft.trim() || undefined
      });
      replaceTask(updated, true);
      syncDraft(updated);
      toast('success', `${updated.key} resolved.`);
    } catch (error) {
      drawerError = friendlyError(error, 'The issue could not be resolved. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) replaceTask(error.details.current as Task);
    } finally {
      taskActionLoading = '';
    }
  }

  async function reopenBug() {
    if (!drawerTask?.bug) return;
    if (!reopenReasonDraft.trim()) {
      drawerError = 'Explain why this issue is being reopened.';
      return;
    }
    taskActionLoading = drawerTask.id;
    drawerError = '';
    try {
      const updated = await api.reopenTask(drawerTask.id, drawerTask.version, { reason: reopenReasonDraft.trim() });
      replaceTask(updated, true);
      syncDraft(updated);
      toast('success', `${updated.key} reopened.`);
    } catch (error) {
      drawerError = friendlyError(error, 'The issue could not be reopened. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) replaceTask(error.details.current as Task);
    } finally {
      taskActionLoading = '';
    }
  }

  async function postComment() {
    if (!drawerTask || !commentBody.trim()) return;
    commentSending = true;
    const taskId = drawerTask.id;
    try {
      await api.postComment(taskId, commentBody.trim());
      commentBody = '';
      // The POST response is a comment resource without a timeline cursor.
      // Refresh one newest page so the canonical timeline item is merged once
      // and generated progress rows cannot be duplicated in the drawer.
      drawerTimelineRequest += 1;
      drawerTimelineLoading = false;
      drawerTimelineLoadingOlder = false;
      await loadDrawerTimeline(taskId);
      toast('success', 'Comment added.');
    } catch (error) {
      drawerError = friendlyError(error, 'Your comment could not be added.');
    } finally {
      commentSending = false;
    }
  }

  function projectForTask(task: Task): Project | undefined {
    return projects.find((project) => project.id === task.project_id);
  }

  async function openWorkTask(task: Task) {
    const project = projectForTask(task);
    if (project) {
      activeProjectSlug = project.slug;
      recentProjectIds = rememberProject(project.id, localStorage);
      await loadBoard();
    }
    await openTask(task);
  }

  async function openRoadmapTask(task: Task): Promise<void> {
    const project = projectForTask(task) || projects.find((item) => item.id === task.project_id);
    if (!project) {
      await openTask(task, 'activity');
      return;
    }
    taskRouteOrigin = window.location.pathname + window.location.search;
    activeProjectSlug = project.slug;
    roadmapProjectId = undefined;
    view = 'board';
    recentProjectIds = rememberProject(project.id, localStorage);
    localStorage.setItem('roadmap.last-project', project.slug);
    navigate(taskDeepLink(project.slug, task.key, 'activity'));
    await loadBoard();
    await openTask(task, 'activity');
  }

  async function openBoardTimelineTask(task: Task): Promise<void> {
    const project = activeProject && activeProject.id === task.project_id ? activeProject : projectForTask(task);
    if (!project) return;
    taskRouteOrigin = `/p/${encodeURIComponent(project.slug)}/timeline`;
    navigate(taskDeepLink(project.slug, task.key, 'activity'));
    await openTask(task, 'activity');
  }

  async function openRoadmapActivity(event: ActivityEvent): Promise<void> {
    const task = event.task_id ? roadmapActivityTasks[event.task_id] : undefined;
    if (task) {
      await openRoadmapTask(task);
      return;
    }
    if (!event.task_id) return;
    try {
      const loaded = await api.getTask(event.task_id);
      if (roadmapProjectId && loaded.project_id !== roadmapProjectId) return;
      roadmapActivityTasks = { ...roadmapActivityTasks, [loaded.id]: loaded };
      await openRoadmapTask(loaded);
    } catch {
      roadmapActivityError = 'This activity points to a task that is no longer available.';
    }
  }

  function taskDueClass(task: Task): string {
    if (isOverdue(task.due_at)) return 'overdue';
    if (isDueSoon(task.due_at)) return 'due-soon';
    return '';
  }

  function claimAction(task: Task | null, now = pulseClock): 'claim' | 'renew' {
    return task?.claimed_by && actorId(task.claimed_by) === user?.id && claimIsActive(task, now) ? 'renew' : 'claim';
  }

  function claimConflict(task: Task | null, now = pulseClock): boolean {
    return Boolean(task?.claimed_by && actorId(task.claimed_by) !== user?.id && claimIsActive(task, now));
  }

  function claimOwnerLabel(task: Task | null): string {
    if (!task?.claimed_by) return '';
    return actorName(task.claimed_by) || actorId(task.claimed_by) || 'another actor';
  }

  function toast(kind: ToastKind, message: string) {
    const id = ++toastSequence;
    toasts = [...toasts, { id, kind, message }];
    window.setTimeout(() => {
      toasts = toasts.filter((item) => item.id !== id);
    }, 4200);
  }

  function friendlyError(error: unknown, fallback: string): string {
    if (error instanceof ApiError) return error.message;
    if (error instanceof Error) return error.message;
    return fallback;
  }

  async function createAgent() {
    if (!agentNameDraft.trim()) return;
    try {
      const agent = await api.createAgent({ name: agentNameDraft.trim(), description: agentDescriptionDraft.trim() });
      agents = [...agents, agent];
      selectedAgentId = agent.id;
      agentNameDraft = '';
      agentDescriptionDraft = '';
      showAgentForm = false;
      toast('success', `${agent.name} was created.`);
    } catch (error) {
      agentsError = friendlyError(error, 'The agent could not be created.');
    }
  }

  function toggleScope(scope: string) {
    tokenScopes = tokenScopes.includes(scope) ? tokenScopes.filter((item) => item !== scope) : [...tokenScopes, scope];
  }

  function toggleTokenProject(projectId: string) {
    tokenProjectIds = tokenProjectIds.includes(projectId) ? tokenProjectIds.filter((id) => id !== projectId) : [...tokenProjectIds, projectId];
  }

  async function createToken() {
    if (!selectedAgentId || !tokenNameDraft.trim() || !tokenScopes.length) return;
    tokenCreating = true;
    try {
      const token = await api.createToken(selectedAgentId, {
        name: tokenNameDraft.trim(),
        scopes: tokenScopes,
        project_ids: tokenProjectIds.length ? tokenProjectIds : undefined
      });
      rememberDialogFocus('[data-token-trigger]');
      revealedToken = token;
      tokenNameDraft = '';
      showTokenForm = false;
      await loadAgents();
      toast('success', 'Token created. Copy it now — it will not be shown again.');
    } catch (error) {
      agentsError = friendlyError(error, 'The token could not be created.');
    } finally {
      tokenCreating = false;
    }
  }

  async function deleteToken(token: ApiToken) {
    if (!window.confirm(`Revoke ${token.name}? This cannot be undone.`)) return;
    try {
      await api.deleteToken(token.id);
      agents = agents.map((agent) => ({ ...agent, tokens: agent.tokens?.filter((item) => item.id !== token.id) }));
      toast('success', `${token.name} was revoked.`);
    } catch (error) {
      toast('error', friendlyError(error, 'The token could not be revoked.'));
    }
  }

  async function copyRevealedToken() {
    const value = revealedToken?.plaintext || revealedToken?.token || '';
    if (!value || !navigator.clipboard) {
      toast('error', 'Token could not be copied. Select it manually.');
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      toast('success', 'Token copied to clipboard.');
    } catch {
      toast('error', 'Token could not be copied. Select it manually.');
    }
  }

  function agentForToken(agentId: string): Agent | undefined {
    return agents.find((agent) => agent.id === agentId);
  }
</script>

<svelte:window />

{#if booting}
  <div class="splash" aria-live="polite">
    <div class="brand-mark brand-mark-large">R</div>
    <div class="splash-copy">
      <strong>Roadmap</strong>
      <span>Getting your workspace ready…</span>
    </div>
    <span class="spinner" aria-label="Loading"></span>
  </div>
{:else if !user}
  <main class="auth-page">
    <section class="auth-panel" aria-labelledby="auth-heading">
      <div class="auth-intro">
        <div class="brand-mark">R</div>
        <span class="eyebrow">Agent-first planning</span>
        <h1 id="auth-heading">Make progress visible.</h1>
        <p>Roadmap keeps projects focused, tasks accountable, and every handoff easy to follow.</p>
        <div class="auth-proof">
          <span class="proof-icon">✦</span>
          <span>One calm place for humans and agents to move work forward.</span>
        </div>
      </div>
      <form class="auth-form" on:submit|preventDefault={submitAuth}>
        <div class="form-heading">
          <h2>{authView === 'setup' ? 'Create your workspace' : 'Welcome back'}</h2>
          <p>{authView === 'setup' ? 'Set up the first administrator account.' : 'Sign in to pick up where you left off.'}</p>
        </div>
        {#if authError}
          <div class="inline-alert error" role="alert"><span>!</span>{authError}</div>
        {/if}
        {#if authView === 'setup'}
          <label>Full name<input bind:value={setupName} autocomplete="name" placeholder="Alex Morgan" /></label>
          <label>Email<input type="email" bind:value={setupEmail} autocomplete="email" placeholder="alex@company.com" /></label>
          <label>Password<input type="password" bind:value={setupPassword} minlength="12" autocomplete="new-password" placeholder="At least 12 characters" /></label>
        {:else}
          <label>Email<input type="email" bind:value={loginEmail} autocomplete="email" placeholder="you@company.com" /></label>
          <label>Password<input type="password" bind:value={loginPassword} autocomplete="current-password" placeholder="Your password" /></label>
        {/if}
        <button class="button primary button-large" type="submit" disabled={authSubmitting}>
          {#if authSubmitting}<span class="button-spinner"></span>{/if}
          {authView === 'setup' ? 'Create workspace' : 'Sign in'}
        </button>
        {#if authStatus?.mode === 'local'}
          <button class="text-button auth-switch" type="button" on:click={() => { authView = authView === 'setup' ? 'login' : 'setup'; authError = ''; }}>
            {authView === 'setup' ? 'Already have an account? Sign in' : 'First time here? Set up your workspace'}
          </button>
        {/if}
      </form>
    </section>
  </main>
{:else}
  <div class="app-shell" class:dark-mode={theme === 'dark'}>
    <nav class="sidebar" aria-label="Primary navigation">
      <div class="sidebar-top">
        <button class="brand-lockup" type="button" aria-label="Go to current project" on:click={() => activeProject && setView('board')}>
          <span class="brand-mark">R</span>
          <span><strong>Roadmap</strong><small>Stay in motion</small></span>
        </button>
        <button class="button new-project-button" type="button" data-project-modal-trigger on:click={openProjectModal}><span aria-hidden="true">＋</span> New project</button>
      </div>

      <nav class="nav-links" aria-label="Workspace views">
        <button class:active={view === 'issues'} type="button" aria-label="Issues" on:click={() => setView('issues')}><span class="nav-icon">⚠</span><span>Issues</span>{#if issueTasks.length}<span class="nav-count">{issueTasks.length}</span>{/if}</button>
        <button class:active={view === 'my-work'} type="button" aria-label="My work" on:click={() => setView('my-work')}><span class="nav-icon">◌</span><span>My work</span>{#if myWorkTasks.length}<span class="nav-count">{myWorkTasks.length}</span>{/if}</button>
        <button class:active={view === 'roadmap'} type="button" aria-label="Roadmap" on:click={() => setView('roadmap')}><span class="nav-icon">◒</span><span>Roadmap</span></button>
      </nav>

      <div class="project-nav">
        <div class="section-label"><span>Projects</span><button class="icon-button tiny" type="button" aria-label="Create project" data-project-modal-trigger on:click={openProjectModal}>＋</button></div>
        {#if favoriteProjects.length}
          <div class="project-subsection"><span class="subsection-label">Favorites</span>
            {#each favoriteProjects as project}
              <button class="project-link" class:active={activeProjectSlug === project.slug} type="button" aria-label={project.name} on:click={() => selectProject(project)}>
                <span class="project-dot" style={`--project-color: ${project.color || '#6d5efc'}`}>{projectInitials(project)}</span><span class="project-link-name">{project.name}</span><span class="favorite-star" aria-label="Favorite">★</span>
              </button>
            {/each}
          </div>
        {/if}
        {#if recentProjects.length}
          <div class="project-subsection"><span class="subsection-label">Recent</span>
            {#each recentProjects.filter((project) => !project.favorite) as project}
              <button class="project-link" class:active={activeProjectSlug === project.slug} type="button" aria-label={project.name} on:click={() => selectProject(project)}>
                <span class="project-dot" style={`--project-color: ${project.color || '#6d5efc'}`}>{projectInitials(project)}</span><span class="project-link-name">{project.name}</span>
              </button>
            {/each}
          </div>
        {/if}
        {#if projectsLoading}
          <div class="nav-skeleton"></div><div class="nav-skeleton short"></div>
        {:else if projectsError}
          <button class="nav-error" type="button" on:click={loadProjects}>Couldn’t load projects · Retry</button>
        {:else if !projects.length}
          <div class="nav-empty">No projects yet</div>
        {:else}
          <div class="project-subsection all-projects"><span class="subsection-label">All projects</span>
            {#each projects.filter((project) => !project.favorite && !recentProjectIds.includes(project.id)) as project}
              <button class="project-link" class:active={activeProjectSlug === project.slug} type="button" aria-label={project.name} on:click={() => selectProject(project)}>
                <span class="project-dot" style={`--project-color: ${project.color || '#6d5efc'}`}>{projectInitials(project)}</span><span class="project-link-name">{project.name}</span>
              </button>
            {/each}
          </div>
        {/if}
      </div>

      <div class="sidebar-bottom">
        <button class:active={view === 'settings'} class="settings-link" type="button" aria-label="Settings" on:click={() => setView('settings')}><span class="nav-icon">⚙</span><span>Settings</span></button>
        <div class="user-chip"><span class="avatar" class:agent={user.kind === 'agent'}>{projectInitials({ name: user.name, key: user.name })}</span><span class="user-copy"><strong>{user.name}</strong><small>{user.email || (user.kind === 'agent' ? 'Agent' : 'Workspace member')}</small></span><button class="icon-button tiny" type="button" aria-label="Sign out" on:click={logout}>↪</button></div>
      </div>
    </nav>

    <div class="main-shell">
      <header class="topbar">
        <div class="mobile-brand"><span class="brand-mark">R</span><strong>Roadmap</strong></div>
        <div class="topbar-project">
          {#if activeProject}
            <button class="project-picker" type="button" data-project-picker-trigger aria-label={`Switch project, current ${activeProject.name}`} aria-expanded={projectSwitcherOpen} on:click={() => { projectSwitcherOpen = !projectSwitcherOpen; closeCommandPalette(); }}>
              <span class="project-dot large" style={`--project-color: ${activeProject.color || '#6d5efc'}`}>{projectInitials(activeProject)}</span><span>{activeProject.name}</span><span class="picker-chevron">⌄</span>
            </button>
          {:else}<span class="muted">Workspace</span>{/if}
          {#if projectSwitcherOpen}
            <div class="popover project-popover">
              <div class="popover-search"><span aria-hidden="true">⌕</span><input bind:value={projectSwitcherQuery} placeholder="Find a project…" /></div>
              <div class="popover-list">
                {#if filteredSwitcherProjects.length}
                  {#each filteredSwitcherProjects as project}
                    <button class="popover-project" type="button" aria-label={project.name} on:click={() => selectProject(project)}><span class="project-dot" style={`--project-color: ${project.color || '#6d5efc'}`}>{projectInitials(project)}</span><span><strong>{project.name}</strong><small>{project.key}</small></span>{#if project.favorite}<span class="favorite-star">★</span>{/if}</button>
                  {/each}
                {:else}<div class="popover-empty">No matching projects</div>{/if}
              </div>
              <button class="popover-create" type="button" data-project-modal-trigger on:click={openProjectModal}>＋ Create a project</button>
            </div>
          {/if}
        </div>
        <div class="topbar-actions">
          <button class="command-trigger" type="button" aria-label="Search anything" data-command-trigger on:click={openCommandPalette}><span>⌕</span><span class="command-trigger-label">Search anything</span><kbd>⌘ K</kbd></button>
          <button class="icon-button" type="button" aria-label={theme === 'dark' ? 'Use light theme' : 'Use dark theme'} on:click={toggleTheme}>{theme === 'dark' ? '☼' : '◐'}</button>
          <button class="avatar top-avatar" type="button" aria-label="Open settings" on:click={() => setView('settings')}>{projectInitials({ name: user.name, key: user.name })}</button>
        </div>
      </header>

      <nav class="mobile-nav" aria-label="Primary navigation">
        <button class:active={view === 'board' || view === 'timeline'} type="button" aria-label="Board" aria-current={view === 'board' || view === 'timeline' ? 'page' : undefined} on:click={() => setView('board')}><span class="mobile-nav-icon" aria-hidden="true">▦</span><span>Board</span></button>
        <button class:active={view === 'issues'} type="button" aria-label="Issues" aria-current={view === 'issues' ? 'page' : undefined} on:click={() => setView('issues')}><span class="mobile-nav-icon" aria-hidden="true">⚠</span><span>Issues</span></button>
        <button class:active={view === 'my-work'} type="button" aria-label="My work" aria-current={view === 'my-work' ? 'page' : undefined} on:click={() => setView('my-work')}><span class="mobile-nav-icon" aria-hidden="true">◌</span><span>My Work</span></button>
        <button class:active={view === 'roadmap'} type="button" aria-label="Roadmap" aria-current={view === 'roadmap' ? 'page' : undefined} on:click={() => setView('roadmap')}><span class="mobile-nav-icon" aria-hidden="true">◒</span><span>Roadmap</span></button>
        <button class:active={view === 'settings'} type="button" aria-label="Settings" aria-current={view === 'settings' ? 'page' : undefined} on:click={() => setView('settings')}><span class="mobile-nav-icon" aria-hidden="true">⚙</span><span>Settings</span></button>
      </nav>

      <main class="content" class:my-work-live={view === 'my-work' && myWorkView === 'live'}>
        {#if view === 'board' || view === 'timeline'}
          {#if activeProject}
            <section class="page-heading board-heading">
              <div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{activeProject.key}</span></div><div class="heading-title-row"><span class="heading-project-dot" style={`--project-color: ${activeProject.color || '#6d5efc'}`}></span><h1>{activeProject.name}</h1><button class="icon-button favorite-heading" class:starred={activeProject.favorite} type="button" aria-label={activeProject.favorite ? 'Remove from favorites' : 'Add to favorites'} on:click={(event) => toggleFavorite(event, activeProject)}>{activeProject.favorite ? '★' : '☆'}</button></div><p>{view === 'timeline' ? 'Everything recently worked on in this board, in one chronological view.' : activeProject.description || 'A focused space for turning ideas into shipped work.'}</p></div>
              <div class="heading-actions"><button class="button quiet-button" type="button" on:click={openProjectRoadmap}><span aria-hidden="true">◒</span> Progress</button><button class="button quiet-button" type="button" on:click={openProjectAudits}><span aria-hidden="true">◎</span> Audits</button><button class="button quiet-button" type="button" data-report-bug-trigger on:click={openBugModal}><span aria-hidden="true">⚠</span> Report bug</button><button class="button primary" type="button" data-task-modal-trigger on:click={openTaskModal}><span aria-hidden="true">＋</span> New task</button></div>
            </section>

            <div class="board-view-switch" role="tablist" aria-label="Board view">
              <button id="board-view-board" class:active={view === 'board'} type="button" role="tab" aria-selected={view === 'board'} tabindex={view === 'board' ? 0 : -1} on:click={() => setView('board')} on:keydown={boardViewKeydown}><span aria-hidden="true">▦</span> Board</button>
              <button id="board-view-timeline" class:active={view === 'timeline'} type="button" role="tab" aria-selected={view === 'timeline'} tabindex={view === 'timeline' ? 0 : -1} on:click={openProjectTimeline} on:keydown={boardViewKeydown}><span aria-hidden="true">◷</span> Timeline</button>
            </div>

            {#if view === 'board'}
            <section class="board-toolbar" aria-label="Board filters">
              <div class="filter-search"><span aria-hidden="true">⌕</span><input aria-label="Search tasks" bind:value={filters.query} placeholder="Search tasks…" /><kbd>/</kbd></div>
              <div class="filter-group"><select aria-label="Filter by state" bind:value={filters.state}><option value="all">All states</option>{#each sortedColumns as column}<option value={column.semantic_state}>{stateLabels[column.semantic_state] || column.name}</option>{/each}</select><select aria-label="Filter by priority" bind:value={filters.priority}><option value="all">All priorities</option><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select><select aria-label="Filter by agent work" bind:value={boardWorkFilter}><option value="all">All agent work</option><option value="action-needed">Action needed{boardWorkCounts.actionNeeded ? ` · ${boardWorkCounts.actionNeeded}` : ''}</option><option value="missing">Missing{boardWorkCounts.missing ? ` · ${boardWorkCounts.missing}` : ''}</option><option value="stale">Stale{boardWorkCounts.stale ? ` · ${boardWorkCounts.stale}` : ''}</option><option value="waiting">Waiting{boardWorkCounts.waiting ? ` · ${boardWorkCounts.waiting}` : ''}</option><option value="handoff">Handoff{boardWorkCounts.handoff ? ` · ${boardWorkCounts.handoff}` : ''}</option><option value="working">Working{boardWorkCounts.working ? ` · ${boardWorkCounts.working}` : ''}</option><option value="verifying">Verifying{boardWorkCounts.verifying ? ` · ${boardWorkCounts.verifying}` : ''}</option></select><select aria-label="Filter by label" bind:value={filters.label}><option value="all">All labels</option>{#each labels as label}<option value={label.id}>{label.name}</option>{/each}</select><select aria-label="Filter by assignee" bind:value={filters.assignee}><option value="all">All assignees</option>{#each Array.from(new Map(tasks.map((task) => [actorId(task.assignee), task.assignee])).entries()).filter(([id]) => id) as pair}<option value={pair[0]}>{actorName(pair[1]) || pair[0]}</option>{/each}</select></div>
              {#if filters.query || filters.priority !== 'all' || filters.label !== 'all' || filters.assignee !== 'all' || filters.state !== 'all' || boardWorkFilter !== 'all'}<button class="clear-filters" type="button" on:click={clearFilters}>Clear filters</button>{/if}
              <span class="toolbar-spacer"></span><span class="task-total">{visibleTasks.length} {visibleTasks.length === 1 ? 'task' : 'tasks'}</span><button class="icon-button" type="button" aria-label="Refresh board" on:click={loadBoard}>↻</button>
            </section>

            {#if boardError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{boardError}</span><button class="text-button" type="button" on:click={loadBoard}>Retry</button></div>{/if}
            {#if boardLoading && !tasks.length}
              <div class="board board-loading" aria-label="Loading board">{#each [1, 2, 3, 4] as item}<div class="column-skeleton"><div></div><div></div><div></div></div>{/each}</div>
            {:else if !sortedColumns.length}
              <div class="empty-state board-empty"><div class="empty-icon">◇</div><h2>Your board is almost ready</h2><p>Columns will appear here once this project has been initialized.</p><button class="button primary" type="button" on:click={loadBoard}>Refresh board</button></div>
            {:else}
              <section class="board" aria-label={`${activeProject.name} board`}>
                {#each sortedColumns as column}
                  <article class="board-column" on:dragover|preventDefault={(event) => { if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'; }} on:drop={(event) => dropTask(event, column.id)}>
                    <header class="column-header"><div class="column-name"><span class="column-dot" style={`--column-color: ${columnColor(column)}`}></span><h2>{column.name}</h2><span class="column-count">{tasksByColumn[column.id].length}</span></div></header>
                    <div class="column-progress"><span style={`width: ${Math.min(100, tasksByColumn[column.id].length * 4)}%; --column-color: ${columnColor(column)}`}></span></div>
                    <div class="column-cards">
                      {#if !tasksByColumn[column.id].length}
                        <div class="column-empty"><span>Nothing here yet</span><button class="text-button" type="button" on:click={() => quickAddColumn = column.id}>Add the first task</button></div>
                      {:else}
                        {#each tasksByColumn[column.id] as task (task.id)}
                          <article class="task-card" class:dragging={draggingTaskId === task.id} draggable="true" on:dragstart={(event) => dragStart(event, task)} on:dragend={() => draggingTaskId = ''}>
                            <button class="task-main" type="button" data-task-trigger on:click={() => openTask(task)} on:keydown={(event) => keyboardMove(event, task)}>
                              <span class="task-card-top"><span class="task-key">{task.key}</span>{#if task.kind === 'bug'}<span class="issue-kind-badge">Bug</span>{#if task.bug?.severity}<span class="severity-badge">{task.bug.severity.toUpperCase()}</span>{/if}{/if}<span class={`priority-dot priority-${task.priority}`} title={`${priorityLabels[task.priority]} priority`}></span>{#if task.claimed_by}<span class="claim-mini" title={`Claimed by ${actorName(task.claimed_by) || 'another actor'}`}>●</span>{/if}</span>
                              <strong class="task-title">{task.title}</strong>
                              {#if task.description}<span class="task-excerpt">{task.description.replace(/[#*_`]/g, '').slice(0, 92)}{task.description.length > 92 ? '…' : ''}</span>{/if}
                              {#if task.labels?.length}<span class="task-labels">{#each task.labels.slice(0, 3) as label}<span class="label-chip" style={`--label-color: ${label.color || '#8b7cf6'}`}>{label.name}</span>{/each}{#if task.labels.length > 3}<span class="label-more">+{task.labels.length - 3}</span>{/if}</span>{/if}
                            </button>
                            {#if showAgentPulse(task)}<AgentPulse {task} now={pulseClock} actorLabel={agentLabelForTask(task)} />{/if}
                            <div class="task-card-footer"><span class={`due-date ${taskDueClass(task)}`}>{#if task.due_at}<span aria-hidden="true">◷</span>{formatDate(task.due_at)}{/if}</span><span class="card-footer-spacer"></span>{#if task.assignee}<span class="mini-avatar" title={`Assigned to ${actorName(task.assignee) || actorId(task.assignee)}`}>{(actorName(task.assignee) || actorId(task.assignee)).slice(0, 1).toUpperCase()}</span>{/if}{#if task.comment_count}<span class="comment-count" title={`${task.comment_count} comments`}>◌ {task.comment_count}</span>{/if}<button class="icon-button card-move" type="button" aria-label={`Move ${task.key} to previous column`} disabled={sortedColumns.findIndex((item) => item.id === task.column_id) === 0 || taskActionLoading === task.id} on:click={() => { const index = sortedColumns.findIndex((item) => item.id === task.column_id); if (index > 0) void moveTask(task, sortedColumns[index - 1].id); }}>←</button><button class="icon-button card-move" type="button" aria-label={`Move ${task.key} to next column`} disabled={sortedColumns.findIndex((item) => item.id === task.column_id) === sortedColumns.length - 1 || taskActionLoading === task.id} on:click={() => { const index = sortedColumns.findIndex((item) => item.id === task.column_id); if (index < sortedColumns.length - 1) void moveTask(task, sortedColumns[index + 1].id); }}>→</button></div>
                          </article>
                        {/each}
                      {/if}
                    </div>
                    <div class="quick-add-wrap">
                      {#if quickAddColumn === column.id}
                        <form class="quick-add-form" on:submit|preventDefault={() => submitQuickAdd(column.id)}><input bind:value={quickAddTitle[column.id]} aria-label={`New task in ${column.name}`} placeholder="What needs doing?" /><div><button class="text-button" type="button" on:click={() => quickAddColumn = ''}>Cancel</button><button class="button primary compact-button" type="submit" disabled={!quickAddTitle[column.id]?.trim() || taskActionLoading === `create-${column.id}`}>Add task</button></div></form>
                      {:else}<button class="quick-add-trigger" type="button" on:click={() => quickAddColumn = column.id}><span>＋</span> Add task</button>{/if}
                    </div>
                  </article>
                {/each}
              </section>
            {/if}
            {:else}
              <BoardTimeline
                items={boardTimelineItems}
                {tasks}
                filter={boardTimelineFilter}
                loading={boardTimelineLoading}
                loadingOlder={boardTimelineLoadingOlder}
                error={boardTimelineError}
                hasOlder={Boolean(boardTimelineNextCursor)}
                onFilterChange={setBoardTimelineFilter}
                onLoadOlder={loadOlderBoardTimeline}
                onRetry={() => { if (activeProject) void loadBoardTimeline(activeProject.id, { reset: !boardTimelineItems.length }); }}
                onOpen={openBoardTimelineTask}
              />
            {/if}
          {:else}
            <div class="empty-state welcome-state"><div class="welcome-orbit"><span>R</span></div><span class="eyebrow">Your workspace is ready</span><h1>Start with a project.</h1><p>Projects give your ideas a home. Create one, invite your agents, and keep the next step clear.</p><button class="button primary button-large" type="button" on:click={openProjectModal}>＋ Create your first project</button></div>
          {/if}
        {:else if view === 'audits'}
          {#if activeProject}
            <AuditReview
              project={activeProject}
              {columns}
              {tasks}
              initialAuditId={auditIdFromRoute}
              {sessionGeneration}
              refreshToken={auditRefreshToken}
              onNavigate={navigateAudit}
              onNotice={toast}
              onTaskUpdated={(updated) => replaceTask(updated, true)}
            />
          {:else}
            <div class="empty-state welcome-state"><div class="welcome-orbit"><span>R</span></div><span class="eyebrow">Choose a project</span><h1>Audits live with a board.</h1><p>Select a project first, then review its captured board audits.</p><button class="button primary button-large" type="button" on:click={() => setView('board')}>Browse projects</button></div>
          {/if}
        {:else if view === 'issues'}
          <section class="page-heading issues-heading">
            <div>
              <div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Issues</span></div>
              <h1>Issues</h1>
              <p>Report, triage, and resolve bugs across the projects you can access.</p>
            </div>
            <div class="heading-actions">
              <button class="button quiet-button" type="button" on:click={loadIssues}>↻ Refresh</button>
              <button class="button primary" type="button" data-report-bug-trigger on:click={openBugModal}><span aria-hidden="true">＋</span> Report bug</button>
            </div>
          </section>
          <section class="issues-toolbar" aria-label="Issue filters">
            <div class="filter-search"><span aria-hidden="true">⌕</span><input aria-label="Search issues" bind:value={issueFilters.query} placeholder="Search issues…" /><kbd>/</kbd></div>
            <div class="filter-group">
              <select aria-label="Filter by issue type" bind:value={issueFilters.kind}><option value="bug">Bugs</option><option value="task">Tasks</option><option value="all">All kinds</option></select>
              <select aria-label="Filter by severity" bind:value={issueFilters.severity}><option value="all">All severities</option><option value="untriaged">Untriaged</option>{#each Object.entries(severityLabels) as pair}<option value={pair[0]}>{pair[1]}</option>{/each}</select>
              <select aria-label="Filter by resolution" bind:value={issueFilters.resolution}><option value="all">All resolutions</option><option value="open">Open</option>{#each resolutionOptions as resolution}<option value={resolution}>{resolutionLabels[resolution]}</option>{/each}</select>
              <select aria-label="Filter issues by project" bind:value={issueProjectFilter}><option value="all">All projects</option>{#each projects as project}<option value={project.id}>{project.key} · {project.name}</option>{/each}</select>
              <select aria-label="Filter by reporter" bind:value={issueFilters.reporter}><option value="all">All reporters</option>{#each issueReporterOptions as reporter}<option value={reporter}>{reporter}</option>{/each}</select>
            </div>
            {#if issueFilters.query || issueFilters.kind !== 'bug' || issueFilters.severity !== 'all' || issueFilters.resolution !== 'all' || issueFilters.reporter !== 'all' || issueProjectFilter !== 'all'}<button class="clear-filters" type="button" on:click={clearIssueFilters}>Clear filters</button>{/if}
            <span class="toolbar-spacer"></span><span class="task-total">{visibleIssues.length} {visibleIssues.length === 1 ? 'issue' : 'issues'}</span>
          </section>
          <section class="issue-metrics" aria-label="Issue health">
            <article><span>Open</span><strong>{issueMetrics.open}</strong></article>
            <article><span>Untriaged</span><strong>{issueMetrics.untriaged}</strong></article>
            <article><span>S1 / S2 open</span><strong>{issueMetrics.severe}</strong></article>
            <article><span>Resolved · 7d</span><strong>{issueMetrics.recentlyResolved}</strong></article>
            <article><span>Reopened · recent activity</span><strong>{issueMetrics.reopened}</strong></article>
          </section>
          {#if issuesError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{issuesError}</span><button class="text-button" type="button" on:click={loadIssues}>Retry</button></div>{/if}
          {#if issuesLoading}<div class="list-skeleton" aria-label="Loading issues">{#each [1, 2, 3] as item}<div></div>{/each}</div>
          {:else if !visibleIssues.length}
            <div class="empty-state issues-empty"><div class="empty-icon">⚠</div><h2>No issues match these filters</h2><p>Report a bug when something behaves differently than expected, then keep its status visible here.</p><button class="button primary" type="button" data-report-bug-trigger on:click={openBugModal}>＋ Report a bug</button></div>
          {:else}
            <section class="issues-list" aria-label="Reported issues">
              {#each visibleIssues as issue (issue.id)}
                <button class="issue-row" type="button" on:click={() => openWorkTask(issue)}>
                  <span class="issue-kind-badge" class:task-kind={issue.kind !== 'bug'}>{issue.kind === 'bug' ? 'Bug' : 'Task'}</span>
                  <span class="issue-main"><span class="issue-row-top"><span class="task-key">{issue.key}</span><span class={`priority-pill priority-${issue.priority}`}>{priorityLabels[issue.priority]}</span></span><strong>{issue.title}</strong><span class="issue-project-name">{projectForTask(issue)?.name || 'Project'}{#if issue.bug?.reporter_id}<span> · Reported by {issue.bug.reporter_id}</span>{/if}</span></span>
                  <span class="issue-status"><span class:untriaged={!issue.bug?.severity} class="severity-badge">{issue.bug?.severity ? severityLabels[issue.bug.severity] : 'Untriaged'}</span>{#if issue.bug?.resolution}<span class="resolution-badge">{resolutionLabels[issue.bug.resolution] || issue.bug.resolution}</span>{:else}<span class="resolution-badge open">Open</span>{/if}</span>
                  <span class="issue-column">{issueColumns.find((column) => column.project_id === issue.project_id && column.id === issue.column_id)?.name || '—'}</span><span class="row-arrow">→</span>
                </button>
              {/each}
            </section>
          {/if}
        {:else if view === 'my-work'}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Personal</span></div><h1>My work</h1><p>{myWorkView === 'live' ? 'Live agent work across every project, grouped by the attention it needs.' : 'Everything assigned or claimed by you, across projects.'}</p></div><div class="heading-actions"><div class="work-view-toggle" role="group" aria-label="My work source"><button class:active={myWorkView === 'live'} class="button quiet-button" type="button" aria-pressed={myWorkView === 'live'} on:click={() => selectMyWorkView('live')}>Live</button><button class:active={myWorkView === 'assigned'} class="button quiet-button" type="button" aria-pressed={myWorkView === 'assigned'} on:click={() => selectMyWorkView('assigned')}>Assigned</button></div><button class="button quiet-button" type="button" on:click={loadMyWork}>↻ Refresh</button></div></section>
          {#if myWorkError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{myWorkError}<button class="text-button" type="button" on:click={loadMyWork}>Retry</button></div>{/if}
          {#if myWorkLoading}<div class="list-skeleton">{#each [1, 2, 3] as item}<div></div>{/each}</div>{:else if !myWorkTasks.length}<div class="empty-state"><div class="empty-icon">◌</div><h2>{myWorkView === 'live' ? 'No live work yet' : 'No work assigned yet'}</h2><p>{myWorkView === 'live' ? 'Agent updates and attention items will appear here as work moves across projects.' : 'Tasks claimed or assigned to you will show up here.'}</p><button class="button primary" type="button" on:click={() => activeProject && setView('board')}>Browse the board</button></div>{:else if myWorkView === 'assigned'}<section class="work-list">{#each myWorkTasks as task (task.id)}<button class="work-row" type="button" on:click={() => openWorkTask(task)}><span class="work-project-dot" style={`--project-color: ${projectForTask(task)?.color || '#6d5efc'}`}></span><span class="work-main"><span class="work-row-top"><span class="task-key">{task.key}</span><span class={`priority-pill priority-${task.priority}`}>{priorityLabels[task.priority]}</span></span><strong>{task.title}</strong><span class="work-project-name">{projectForTask(task)?.name || 'Project'}</span></span><span class="work-column">{myWorkColumnsByProject[task.project_id]?.find((column) => column.id === task.column_id)?.name || 'In progress'}</span><span class={`work-due ${taskDueClass(task)}`}>{task.due_at ? `${isOverdue(task.due_at) ? 'Overdue · ' : ''}${formatDate(task.due_at)}` : 'No due date'}</span><span class="row-arrow">→</span></button>{/each}</section>{:else}
            <section class="live-work" class:action-filter={myWorkFilter === 'action-needed'} aria-labelledby="live-work-heading">
              <div class="live-work-toolbar">
                <div><span class="eyebrow">Cross-project coordination</span><h2 id="live-work-heading">{myWorkView === 'live' ? 'Live work' : 'Assigned work'}</h2><p>{myWorkView === 'live' ? `${myWorkTasks.length} live assignment${myWorkTasks.length === 1 ? '' : 's'} across projects.` : `${myWorkTasks.length} assignment${myWorkTasks.length === 1 ? '' : 's'} currently assigned or claimed by you.`}</p></div>
                <div class="live-work-filters" role="group" aria-label="Filter live work">
                  {#each [{ value: 'all', label: 'All' }, { value: 'action-needed', label: 'Action needed' }, { value: 'stale', label: 'Stale' }, { value: 'waiting', label: 'Waiting' }, { value: 'handoff', label: 'Handoff' }, { value: 'verifying', label: 'Verifying' }, { value: 'working', label: 'Working' }] as option}
                    <button class:active={myWorkFilter === option.value} class="live-work-filter" type="button" aria-pressed={myWorkFilter === option.value} on:click={() => myWorkFilter = option.value as WorkFilter}>{option.label}{#if option.value === 'action-needed' && myWorkTasks.filter((task) => isActionNeeded(task, pulseClock)).length}<span>{myWorkTasks.filter((task) => isActionNeeded(task, pulseClock)).length}</span>{/if}</button>
                  {/each}
                </div>
              </div>
              {#if myWorkColumnsLoading}<div class="live-work-note"><span class="spinner"></span> Loading project columns…</div>{/if}
              {#if myWorkFilter === 'all' || myWorkFilter === 'action-needed'}
                <section class="live-work-group action-needed-group" aria-labelledby="action-needed-heading"><div class="live-work-group-heading"><div><h3 id="action-needed-heading">Action needed</h3><p>Waiting, handoff, stale, or explicitly blocked by an agent.</p></div><strong>{myWorkActionRows.length}</strong></div>{#if myWorkActionRows.length}{#each myWorkActionRows as row (row.task.id)}<button class="work-row live-work-row" type="button" on:click={() => openWorkTask(row.task)}><span class="work-project-dot" style={`--project-color: ${row.project?.color || '#6d5efc'}`}></span><span class="work-main"><span class="work-row-top"><span class="task-key">{row.task.key}</span><span class={`priority-pill priority-${row.task.priority}`}>{priorityLabels[row.task.priority]}</span></span><strong>{row.task.title}</strong><span class="work-project-name">{row.project?.name || 'Project'} · {row.column?.name || stateLabels[semanticStateForTask(row.task, myWorkColumnsByProject[row.task.project_id] || [])] || 'In progress'}</span></span><span class="live-work-state"><AgentPulse task={row.task} now={pulseClock} actorLabel={agentLabelForTask(row.task)} /></span><span class="row-arrow">→</span></button>{/each}{:else}<p class="live-work-empty">Nothing needs your attention right now.</p>{/if}</section>
              {/if}
                  {#if myWorkFilter === 'all' || myWorkFilter === 'stale'}
                <section class="live-work-group stale-group" aria-labelledby="stale-heading"><div class="live-work-group-heading"><div><h3 id="stale-heading">Stale</h3><p>No update has arrived for at least 15 minutes.</p></div><strong>{myWorkStaleRows.length}</strong></div>{#if myWorkStaleRows.length}{#each myWorkStaleRows as row (row.task.id)}<LiveWorkRow task={row.task} project={row.project} column={row.column} now={pulseClock} actorLabel={agentLabelForTask(row.task)} onOpen={openWorkTask} />{/each}{:else}<p class="live-work-empty">No stale agent updates.</p>{/if}</section>
              {/if}
                  {#if myWorkFilter === 'all' || myWorkFilter === 'waiting'}
                <section class="live-work-group waiting-group" aria-labelledby="waiting-heading"><div class="live-work-group-heading"><div><h3 id="waiting-heading">Waiting</h3><p>An agent is waiting for a dependency, answer, or decision.</p></div><strong>{myWorkWaitingRows.length}</strong></div>{#if myWorkWaitingRows.length}{#each myWorkWaitingRows as row (row.task.id)}<LiveWorkRow task={row.task} project={row.project} column={row.column} now={pulseClock} actorLabel={agentLabelForTask(row.task)} onOpen={openWorkTask} />{/each}{:else}<p class="live-work-empty">No agent work is waiting.</p>{/if}</section>
              {/if}
                  {#if myWorkFilter === 'all' || myWorkFilter === 'handoff'}
                <section class="live-work-group handoff-group" aria-labelledby="handoff-heading"><div class="live-work-group-heading"><div><h3 id="handoff-heading">Handoff</h3><p>Agent context is ready for a human or another agent.</p></div><strong>{myWorkHandoffRows.length}</strong></div>{#if myWorkHandoffRows.length}{#each myWorkHandoffRows as row (row.task.id)}<LiveWorkRow task={row.task} project={row.project} column={row.column} now={pulseClock} actorLabel={agentLabelForTask(row.task)} onOpen={openWorkTask} />{/each}{:else}<p class="live-work-empty">No handoffs are waiting.</p>{/if}</section>
              {/if}
              {#if myWorkFilter === 'all' || myWorkFilter === 'verifying'}
                <section class="live-work-group" aria-labelledby="verifying-heading"><div class="live-work-group-heading"><div><h3 id="verifying-heading">Verifying</h3><p>Agent work waiting for a final check.</p></div><strong>{myWorkVerifyingRows.length}</strong></div>{#if myWorkVerifyingRows.length}{#each myWorkVerifyingRows as row (row.task.id)}<button class="work-row live-work-row" type="button" on:click={() => openWorkTask(row.task)}><span class="work-project-dot" style={`--project-color: ${row.project?.color || '#6d5efc'}`}></span><span class="work-main"><span class="work-row-top"><span class="task-key">{row.task.key}</span><span class={`priority-pill priority-${row.task.priority}`}>{priorityLabels[row.task.priority]}</span></span><strong>{row.task.title}</strong><span class="work-project-name">{row.project?.name || 'Project'} · {row.column?.name || 'In progress'}</span></span><span class="live-work-state"><AgentPulse task={row.task} now={pulseClock} actorLabel={agentLabelForTask(row.task)} /></span><span class="row-arrow">→</span></button>{/each}{:else}<p class="live-work-empty">No work is waiting for verification.</p>{/if}</section>
              {/if}
              {#if myWorkFilter === 'all' || myWorkFilter === 'working'}
                <section class="live-work-group" aria-labelledby="working-heading"><div class="live-work-group-heading"><div><h3 id="working-heading">Working</h3><p>Agents actively moving tasks forward.</p></div><strong>{myWorkWorkingRows.length}</strong></div>{#if myWorkWorkingRows.length}{#each myWorkWorkingRows as row (row.task.id)}<button class="work-row live-work-row" type="button" on:click={() => openWorkTask(row.task)}><span class="work-project-dot" style={`--project-color: ${row.project?.color || '#6d5efc'}`}></span><span class="work-main"><span class="work-row-top"><span class="task-key">{row.task.key}</span><span class={`priority-pill priority-${row.task.priority}`}>{priorityLabels[row.task.priority]}</span></span><strong>{row.task.title}</strong><span class="work-project-name">{row.project?.name || 'Project'} · {row.column?.name || 'In progress'}</span></span><span class="live-work-state"><AgentPulse task={row.task} now={pulseClock} actorLabel={agentLabelForTask(row.task)} /></span><span class="row-arrow">→</span></button>{/each}{:else}<p class="live-work-empty">No agents are actively working right now.</p>{/if}</section>
              {/if}
            </section>
          {/if}
        {:else if view === 'roadmap'}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{roadmapProject ? roadmapProject.key : 'Overview'}</span></div><h1>{roadmapProject ? `${roadmapProject.name} progress` : 'Roadmap overview'}</h1><p>{roadmapProject ? 'A focused view of delivery, deadlines, and recent activity for this project.' : 'A high-level pulse on every project and what needs attention next.'}</p></div><div class="heading-actions">{#if roadmapProject}<button class="button quiet-button" type="button" on:click={() => setView('roadmap')}>All projects</button>{/if}<button class="button quiet-button" type="button" on:click={() => loadRoadmap(roadmapProjectId)}>↻ Refresh</button></div></section>
          {#if roadmapError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{roadmapError}<button class="text-button" type="button" on:click={() => loadRoadmap(roadmapProjectId)}>Retry</button></div>{/if}
          {#if roadmapLoading}<div class="roadmap-skeleton"><div></div><div></div><div></div></div>{:else}<section class="roadmap-content"><div class="roadmap-hero"><div class="hero-copy"><span class="eyebrow">Workspace pulse</span><h2>Momentum, at a glance.</h2><p>Progress is calculated from each project's semantic board state.</p></div><div class="hero-progress"><div class="progress-ring" style={`--progress: ${roadmapCompletion}%`}><span>{Math.round(roadmapCompletion)}<small>%</small></span></div><div><strong>{roadmapTotal} total tasks</strong><span>{roadmap?.completed_count ?? Math.round(roadmapTotal * roadmapCompletion / 100)} completed</span></div></div></div><div class="metric-grid"><div class="metric-card"><span class="metric-icon purple">◒</span><span class="metric-label">Completion</span><strong>{Math.round(roadmapCompletion)}%</strong><span class="metric-note">Across all projects</span></div><div class="metric-card"><span class="metric-icon red">!</span><span class="metric-label">Overdue</span><strong>{roadmap?.overdue_count ?? 0}</strong><span class="metric-note">Need attention</span></div><div class="metric-card"><span class="metric-icon amber">◷</span><span class="metric-label">Due soon</span><strong>{roadmap?.due_soon_count ?? 0}</strong><span class="metric-note">Next 7 days</span></div><div class="metric-card"><span class="metric-icon green">✓</span><span class="metric-label">Completed</span><strong>{roadmap?.completed_count ?? 0}</strong><span class="metric-note">Shipped so far</span></div></div><RoadmapLiveWork tasks={roadmapLiveTasks} {projects} columnsByProject={roadmapLiveColumnsByProject} actors={roadmapActors} now={pulseClock} loading={roadmapLiveLoading} error={roadmapLiveError} onOpen={openRoadmapTask} onViewAll={() => setView('my-work')} /><div class="roadmap-columns"><section class="roadmap-panel project-progress-panel"><div class="panel-heading"><div><h2>Project progress</h2><p>Where each project stands today.</p></div><button class="icon-button" type="button" aria-label="Refresh progress" on:click={() => loadRoadmap(roadmapProjectId)}>↻</button></div>{#if roadmapProjectRows.length}{#each roadmapProjectRows as row}<button class="project-progress-row" type="button" on:click={() => selectProject(row.project)}><span class="project-dot" style={`--project-color: ${row.project.color || '#6d5efc'}`}>{projectInitials(row.project)}</span><span class="project-progress-name"><strong>{row.project.name}</strong><small>{row.project.key}</small></span><span class="progress-track"><span style={`width: ${row.total_tasks ? (row.completed_tasks / row.total_tasks) * 100 : 0}%; --project-color: ${row.project.color || '#6d5efc'}`}></span></span><span class="progress-number">{row.total_tasks ? Math.round((row.completed_tasks / row.total_tasks) * 100) : 0}%</span><span>→</span></button>{/each}{:else}<div class="panel-empty">Create a project to see progress here.</div>{/if}</section><section class="roadmap-panel upcoming-panel"><div class="panel-heading"><div><h2>Coming up</h2><p>Tasks with the nearest due dates.</p></div></div>{#if roadmap?.upcoming_tasks?.length}{#each roadmap.upcoming_tasks.slice(0, 5) as task}<button class="upcoming-row" type="button" on:click={() => openWorkTask(task)}><span class="upcoming-key">{task.key}</span><span class="upcoming-title">{task.title}</span><span class={`upcoming-date ${taskDueClass(task)}`}>{formatDate(task.due_at)}</span></button>{/each}{:else}<div class="panel-empty">No upcoming deadlines. Nice breathing room.</div>{/if}</section></div><RoadmapActivity events={roadmapActivityEvents} tasksById={roadmapActivityTasks} {projects} actors={roadmapActors} filter={roadmapActivityFilter} loading={roadmapActivityLoading} error={roadmapActivityError} onFilterChange={(next) => roadmapActivityFilter = next} onOpen={openRoadmapActivity} /></section>{/if}
        {:else}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Preferences</span></div><h1>Settings</h1><p>Manage the agents and tokens that help your workspace move.</p></div></section>
          {#if agentsError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{agentsError}<button class="text-button" type="button" on:click={loadAgents}>Retry</button></div>{/if}
          <section class="settings-layout"><div class="settings-main"><div class="settings-section"><div class="settings-section-heading"><div><span class="eyebrow">Coordination</span><h2>Agents &amp; tokens</h2><p>Give software agents scoped access without sharing a human login.</p></div><button class="button primary" type="button" on:click={() => showAgentForm = !showAgentForm}>＋ Add agent</button></div>{#if showAgentForm}<div class="settings-form"><label>Agent name<input bind:value={agentNameDraft} placeholder="Release assistant" /></label><label>Description <span class="optional">Optional</span><textarea rows="2" bind:value={agentDescriptionDraft} placeholder="What is this agent responsible for?"></textarea></label><div class="form-actions"><button class="text-button" type="button" on:click={() => showAgentForm = false}>Cancel</button><button class="button primary" type="button" disabled={!agentNameDraft.trim()} on:click={createAgent}>Create agent</button></div></div>{/if}{#if agentsLoading}<div class="list-skeleton">{#each [1, 2] as item}<div></div>{/each}</div>{:else if !agents.length}<div class="empty-state compact-empty"><div class="empty-icon">✦</div><h3>No agents yet</h3><p>Create a scoped identity for the tools that collaborate with you.</p><button class="button quiet-button" type="button" on:click={() => showAgentForm = true}>Create your first agent</button></div>{:else}<div class="agent-list">{#each agents as agent}<article class="agent-card"><div class="agent-card-header"><span class="agent-avatar">✦</span><div><h3>{agent.name}</h3><p>{agent.description || 'No description'}</p></div><button class="button quiet-button compact-button" type="button" data-token-trigger on:click={() => { selectedAgentId = agent.id; showTokenForm = selectedAgentId === agent.id && !showTokenForm; }}>＋ Token</button></div>{#if agent.tokens?.length}<div class="token-list">{#each agent.tokens as token}<div class="token-row"><span class="token-icon">⌘</span><span class="token-info"><strong>{token.name}</strong><small>{token.scopes.join(' · ')}</small></span><span class="token-date">{token.expires_at ? `Expires ${formatDate(token.expires_at)}` : 'No expiry'}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Revoke ${token.name}`} on:click={() => deleteToken(token)}>×</button></div>{/each}</div>{:else}<div class="agent-no-tokens">No active tokens</div>{/if}{#if showTokenForm && selectedAgentId === agent.id}<div class="token-form"><div class="settings-form"><label>Token name<input bind:value={tokenNameDraft} placeholder="CI deployment token" /></label><fieldset><legend>Scopes</legend><div class="scope-grid">{#each scopeOptions as scope}<label class="check-label"><input type="checkbox" checked={tokenScopes.includes(scope)} on:change={() => toggleScope(scope)} /><span>{scope}</span></label>{/each}</div></fieldset><fieldset><legend>Limit to projects <span class="optional">Optional</span></legend><div class="scope-grid project-checks">{#each projects as project}<label class="check-label"><input type="checkbox" checked={tokenProjectIds.includes(project.id)} on:change={() => toggleTokenProject(project.id)} /><span>{project.name}</span></label>{/each}</div></fieldset><div class="form-actions"><button class="text-button" type="button" on:click={() => showTokenForm = false}>Cancel</button><button class="button primary" type="button" disabled={!tokenNameDraft.trim() || !tokenScopes.length || tokenCreating} on:click={createToken}>{#if tokenCreating}<span class="button-spinner"></span>{/if}Create token</button></div></div></div>{/if}</article>{/each}</div>{/if}</div><div class="settings-section appearance-section"><div class="settings-section-heading"><div><span class="eyebrow">Workspace</span><h2>Appearance</h2><p>Choose how Roadmap feels on this device.</p></div></div><div class="theme-options"><button class:chosen={theme === 'light'} type="button" on:click={() => { theme = 'light'; localStorage.setItem('roadmap.theme', theme); applyTheme(); }}><span class="theme-preview light-preview">☼</span><span><strong>Light</strong><small>Clear and airy</small></span>{#if theme === 'light'}<span class="theme-check">✓</span>{/if}</button><button class:chosen={theme === 'dark'} type="button" on:click={() => { theme = 'dark'; localStorage.setItem('roadmap.theme', theme); applyTheme(); }}><span class="theme-preview dark-preview">☾</span><span><strong>Dark</strong><small>Focused and low-glare</small></span>{#if theme === 'dark'}<span class="theme-check">✓</span>{/if}</button></div></div></div><aside class="settings-aside"><div class="settings-aside-card"><span class="aside-icon">◎</span><h3>Built for safe handoffs</h3><p>Every mutation records its actor. Scoped agent tokens and optimistic versions keep collaboration predictable.</p><span class="aside-rule"></span><span class="aside-caption">Roadmap v1 · API-connected</span></div></aside></section>
        {/if}
      </main>
    </div>

    {#if drawerTask}
      <div class="drawer-backdrop" role="presentation" on:click={closeDrawer}></div>
      <div class="task-drawer" role="dialog" aria-modal="true" aria-label={`${drawerTask.key}: ${drawerTask.title}`} use:focusTrap>
        <div class="drawer-header"><div><span class="drawer-key">{drawerTask.key}</span><span class="issue-kind-badge" class:task-kind={drawerTask.kind !== 'bug'}>{drawerTask.kind === 'bug' ? 'Bug' : 'Task'}</span>{#if drawerTask.kind === 'bug'}<span class:untriaged={!drawerTask.bug?.severity} class="severity-badge">{drawerTask.bug?.severity ? severityLabels[drawerTask.bug.severity] : 'Untriaged'}</span>{/if}<span class={`priority-pill priority-${drawerTask.priority}`}>{priorityLabels[drawerTask.priority]}</span></div><button class="icon-button" type="button" aria-label="Close task details" on:click={closeDrawer}>×</button></div>
        {#if drawerLoading}<div class="drawer-loading"><span class="spinner"></span><span>Loading task details…</span></div>{/if}
        {#if drawerError}<div class="inline-alert error drawer-alert" role="alert"><span>!</span>{drawerError}</div>{/if}
        <div class="drawer-tabs" role="tablist" aria-label="Task views">
          <button class:active={drawerView === 'details'} id="drawer-details-tab" class="drawer-tab" type="button" role="tab" aria-selected={drawerView === 'details'} aria-controls="drawer-details-panel" tabindex={drawerView === 'details' ? 0 : -1} on:click={() => setDrawerView('details')} on:keydown={drawerTabKeydown}>Details</button>
          <button class:active={drawerView === 'activity'} id="drawer-activity-tab" class="drawer-tab" type="button" role="tab" aria-selected={drawerView === 'activity'} aria-controls="drawer-activity-panel" tabindex={drawerView === 'activity' ? 0 : -1} on:click={() => setDrawerView('activity')} on:keydown={drawerTabKeydown}>Activity</button>
        </div>
        {#if drawerView === 'details'}
        <div id="drawer-details-panel" class="drawer-details-panel" role="tabpanel" aria-labelledby="drawer-details-tab">
        {#if drawerTask.kind === 'bug'}
          <div class="drawer-bug-controls">
            <section class="drawer-section bug-details-section" aria-labelledby="bug-details-heading"><div class="section-heading-inline"><h2 id="bug-details-heading">Bug report</h2><span class="optional">Reporter: {drawerTask.bug?.reporter_id || 'Unknown'}</span></div><label>Actual behavior<textarea rows="3" bind:value={draftBugActual} placeholder="What happened?"></textarea></label><label>Expected behavior<textarea rows="3" bind:value={draftBugExpected} placeholder="What should have happened?"></textarea></label><label>Reproduction steps<textarea rows="3" bind:value={draftBugReproduction} placeholder="1. Open…&#10;2. Click…"></textarea></label><div class="drawer-field-grid"><label>Environment<input bind:value={draftBugEnvironment} placeholder="Browser, OS, device" /></label><label>Affected version<input bind:value={draftBugVersion} placeholder="e.g. 1.4.0" /></label></div></section>
            <section class="drawer-section bug-triage-section" aria-labelledby="bug-triage-heading"><div class="section-heading-inline"><h2 id="bug-triage-heading">Triage</h2><span class="optional">Set severity and ownership</span></div><div class="drawer-field-grid"><label>Severity<select aria-label="Bug severity" bind:value={triageSeverityDraft}><option value="s1">{severityLabels.s1}</option><option value="s2">{severityLabels.s2}</option><option value="s3">{severityLabels.s3}</option><option value="s4">{severityLabels.s4}</option></select></label><label>Priority<select aria-label="Triage priority" bind:value={draftPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label></div><label>Assignee<input aria-label="Triage assignee" bind:value={draftAssignee} placeholder="Actor ID (optional)" /></label><button class="button primary" type="button" disabled={taskActionLoading === drawerTask.id} on:click={triageBug}>{#if taskActionLoading === drawerTask.id}<span class="button-spinner"></span>{/if}{drawerTask.bug?.severity ? 'Update triage' : 'Triage issue'}</button></section>
            {#if drawerTask.bug?.resolution}
              <section class="drawer-section bug-resolution-section" aria-labelledby="bug-resolution-heading"><div class="section-heading-inline"><h2 id="bug-resolution-heading">Resolved as {resolutionLabels[drawerTask.bug.resolution] || drawerTask.bug.resolution}</h2><span class="optional">Reopen if the issue persists</span></div><label>Reopen reason<textarea rows="2" bind:value={reopenReasonDraft} placeholder="Why does this need another look?"></textarea></label><button class="button quiet-button" type="button" disabled={taskActionLoading === drawerTask.id || !reopenReasonDraft.trim()} on:click={reopenBug}>Reopen issue</button></section>
            {:else}
              <section class="drawer-section bug-resolution-section" aria-labelledby="bug-resolution-heading"><div class="section-heading-inline"><h2 id="bug-resolution-heading">Resolve</h2><span class="optional">Close the loop for reporters</span></div><label>Resolution<select aria-label="Bug resolution" bind:value={resolutionDraft}>{#each resolutionOptions as resolution}<option value={resolution}>{resolutionLabels[resolution]}</option>{/each}</select></label>{#if resolutionDraft === 'duplicate'}<label>Duplicate of<input aria-label="Duplicate issue" bind:value={duplicateOfDraft} placeholder="Task key or ID" /></label>{/if}<label>Resolution note <span class="optional">Optional</span><textarea rows="2" bind:value={resolutionNoteDraft} placeholder="What changed or why was this closed?"></textarea></label><button class="button complete-button" type="button" disabled={taskActionLoading === drawerTask.id} on:click={resolveBug}>Resolve issue</button></section>
            {/if}
          </div>
        {/if}
        {#if drawerTask.claimed_by}
          <div class="claim-lease" class:expired={!claimIsActive(drawerTask, pulseClock)} class:conflict={claimConflict(drawerTask, pulseClock)} role={claimConflict(drawerTask, pulseClock) ? 'alert' : undefined}>
            <span class="claim-lease-icon" aria-hidden="true">⚑</span>
            <span><strong>{claimConflict(drawerTask, pulseClock) ? `Claim conflict · ${claimOwnerLabel(drawerTask)}` : `Claimed by ${claimOwnerLabel(drawerTask)}`}</strong>{#if drawerTask.claim_expires_at}<small><time datetime={drawerTask.claim_expires_at}>{claimIsActive(drawerTask, pulseClock) ? `Expires ${claimExpiryExact(drawerTask.claim_expires_at)}` : `Expired ${claimExpiryExact(drawerTask.claim_expires_at)}`}</time> · {claimCountdown(drawerTask, pulseClock)}</small>{:else}<small>No expiry reported</small>{/if}</span>
          </div>
        {/if}
        <button class="button quiet-button block-button" type="button" disabled={Boolean(drawerTask.completed_at) || taskActionLoading === drawerTask.id} on:click={openBlockReason}>■ Block</button>
        {#if blockReasonOpen}
          <section class="block-reason-form" aria-labelledby="block-reason-heading"><label id="block-reason-heading">Why is this task blocked?<textarea rows="3" bind:value={blockReasonDraft} placeholder="Describe the dependency or decision needed." required></textarea></label><div class="form-actions"><button class="text-button" type="button" on:click={() => { blockReasonOpen = false; blockReasonDraft = ''; }}>Cancel</button><button class="button danger-button" type="button" disabled={!blockReasonDraft.trim() || taskActionLoading === drawerTask.id} on:click={() => runTaskAction('block', blockReasonDraft)}>Block task</button></div></section>
        {/if}
              <div class="drawer-scroll"><label class="drawer-title-label"><span class="sr-only">Task title</span><input id="drawer-title" class="drawer-title-input" data-dialog-initial-focus bind:value={draftTitle} /></label><div class="drawer-meta"><span class="task-project-marker" style={`--project-color: ${projectForTask(drawerTask)?.color || '#6d5efc'}`}></span><span>{projectForTask(drawerTask)?.name || 'Project'}</span><span>·</span><span>Updated {formatRelative(drawerTask?.updated_at)}</span></div><div class="drawer-actions"><button class="button quiet-button" type="button" disabled={taskActionLoading === drawerTask?.id} on:click={() => runTaskAction(claimAction(drawerTask))}>{drawerTask.claimed_by && actorId(drawerTask.claimed_by) === user?.id && claimIsActive(drawerTask, pulseClock) ? '↻ Renew claim' : drawerTask.claimed_by && claimConflict(drawerTask, pulseClock) ? `Claimed by ${actorName(drawerTask.claimed_by) || 'agent'}` : '⚑ Claim task'}</button>{#if drawerTask.claimed_by && actorId(drawerTask.claimed_by) === user?.id && claimIsActive(drawerTask, pulseClock)}<button class="button quiet-button" type="button" disabled={taskActionLoading === drawerTask?.id} on:click={() => runTaskAction('release')}>Release</button>{/if}<button class="button complete-button" type="button" disabled={Boolean(drawerTask.completed_at) || taskActionLoading === drawerTask.id} on:click={() => runTaskAction('complete')}>{drawerTask.completed_at ? '✓ Completed' : '✓ Complete'}</button></div>{#if showAgentPulse(drawerTask)}<AgentWorkPanel task={drawerTask} now={pulseClock} actorLabel={agentLabelForTask(drawerTask)} />{/if}<section class="drawer-section"><div class="drawer-field-grid"><label>Priority<select bind:value={draftPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label><label>Due date<input type="date" bind:value={draftDueDate} /></label></div><label>Assignee<input bind:value={draftAssignee} placeholder="Actor ID (optional)" /></label><label>Labels <span class="optional">Comma separated</span><input bind:value={draftLabels} placeholder="frontend, design" /></label>{#if labels.filter((label) => label.project_id === drawerTask?.project_id).length}<div class="drawer-label-picker"><span class="optional">Project labels</span><div class="drawer-label-options">{#each labels.filter((label) => label.project_id === drawerTask?.project_id) as label (label.id)}<span class="drawer-label-option" style={`--label-color: ${label.color || '#8b7cf6'}`}><span>{label.name}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Delete label ${label.name}`} disabled={labelDeleting === label.id} on:click|stopPropagation={() => deleteProjectLabel(label)}>×</button></span>{/each}</div></div>{/if}</section>
                <TaskDependencies
                  bind:this={drawerDependencyPanel}
                  task={drawerTask}
                  refreshToken={drawerDependencyRefresh}
                  onTaskUpdated={handleDependencyTaskUpdated}
                  onNavigate={openDependencyTask}
                  onRefreshTask={refreshDependencyTask}
                />
                <section class="drawer-section description-section"><div class="section-heading-inline"><h2>Description</h2><span class="markdown-hint">Markdown supported</span></div><textarea class="description-input" rows="7" bind:value={draftDescription} placeholder="What does success look like?"></textarea></section><button class="button primary save-task-button" type="button" disabled={drawerSaving || !draftTitle.trim()} on:click={saveTask}>{#if drawerSaving}<span class="button-spinner"></span>{/if}Save changes</button></div>
        </div>
        {:else}
          <div id="drawer-activity-panel" class="drawer-scroll drawer-activity-scroll" role="tabpanel" aria-labelledby="drawer-activity-tab">
            {#if showAgentPulse(drawerTask)}<AgentWorkPanel task={drawerTask} now={pulseClock} actorLabel={agentLabelForTask(drawerTask)} />{/if}
            <section class="drawer-section activity-section drawer-timeline-section">
              <form class="comment-form" on:submit|preventDefault={postComment}>
                <span class="avatar mini-user-avatar">{projectInitials({ name: user.name, key: user.name })}</span>
                <textarea rows="2" bind:value={commentBody} placeholder="Leave a note for your team…" aria-label="Comment"></textarea>
                <button class="icon-button comment-send" type="submit" disabled={!commentBody.trim() || commentSending} aria-label="Add comment">↑</button>
              </form>
              <TaskActivityTimeline
                items={drawerTimelineItems}
                filter={drawerTimelineFilter}
                loading={drawerTimelineLoading}
                loadingOlder={drawerTimelineLoadingOlder}
                error={drawerTimelineError}
                hasOlder={Boolean(drawerTimelineNextCursor)}
                onFilterChange={setDrawerTimelineFilter}
                onLoadOlder={loadOlderDrawerActivity}
                onRetry={() => { void loadDrawerTimeline(drawerTask?.id); }}
              />
            </section>
          </div>
        {/if}
        <div class="drawer-delete-wrap"><button class="button danger-button" type="button" disabled={drawerSaving || taskActionLoading === drawerTask?.id} on:click={deleteDrawerTask}>Delete task</button></div>
      </div>
    {/if}

    {#if commandOpen}
      <div class="modal-backdrop command-backdrop" role="presentation" on:click={closeCommandPalette}></div>
      <div class="command-menu" role="dialog" aria-modal="true" aria-label="Search Roadmap" use:focusTrap>
        <div class="command-input-wrap"><span aria-hidden="true">⌕</span><input bind:this={commandInput} data-dialog-initial-focus bind:value={commandQuery} on:keydown={commandKeydown} placeholder="Jump to a project or view…" aria-label="Search projects and views" /><kbd>ESC</kbd></div>
        <div class="command-results">{#if commandChoices.length}{#each commandChoices as choice, index}<button class:selected={index === commandIndex} class="command-row" type="button" on:mouseenter={() => commandIndex = index} on:click={() => selectCommand(choice)}><span class={`command-icon ${choice.kind}`}>{choice.kind === 'project' ? (choice.project ? projectInitials(choice.project) : 'P') : choice.kind === 'issue' ? '⚠' : choice.view === 'issues' ? '⚠' : choice.view === 'my-work' ? '◌' : choice.view === 'roadmap' ? '◒' : '⚙'}</span><span><strong>{choice.label}</strong><small>{choice.hint}</small></span><span class="command-enter">↵</span></button>{/each}{:else}<div class="command-empty">No projects, issues, or views match “{commandQuery}”</div>{/if}</div><div class="command-footer"><span><kbd>↑</kbd><kbd>↓</kbd> Navigate</span><span><kbd>↵</kbd> Open</span><span><kbd>ESC</kbd> Close</span></div>
      </div>
    {/if}

    {#if showTaskModal}
      <div class="modal-backdrop" role="presentation" on:click={closeTaskModal}></div>
      <div class="modal task-create-modal" role="dialog" aria-modal="true" aria-labelledby="task-modal-title" use:focusTrap>
        <div class="modal-header"><div><span class="eyebrow">Capture an idea</span><h2 id="task-modal-title">Create a task</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeTaskModal}>×</button></div>
        {#if taskModalError}<div class="inline-alert error" role="alert"><span>!</span>{taskModalError}</div>{/if}
        <form on:submit|preventDefault={createGlobalTask}>
          <div class="form-row task-destination-row"><label>Project<select bind:value={taskModalProjectId} on:change={() => loadTaskModalColumns(taskModalProjectId)}>{#each projects as project}<option value={project.id}>{project.key} · {project.name}</option>{/each}</select></label><label>Column<select bind:value={taskModalColumnId} disabled={taskModalLoading || !taskModalColumns.length}>{#each taskModalColumns as column}<option value={column.id}>{column.name}</option>{/each}</select></label></div>
          <label>Task title<input data-dialog-initial-focus bind:value={taskModalTitle} placeholder="What should move forward?" /></label>
          <label>Description <span class="optional">Optional · Markdown supported</span><textarea rows="3" bind:value={taskModalDescription} placeholder="Add the context your future self will need."></textarea></label>
          <div class="form-row"><label>Priority<select bind:value={taskModalPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label><label>Due date <span class="optional">Optional</span><input type="date" bind:value={taskModalDueDate} /></label></div>
          <label>Assignee <span class="optional">Optional</span><input bind:value={taskModalAssignee} placeholder="Actor ID" /></label>
          <div class="modal-actions"><button class="text-button" type="button" on:click={closeTaskModal}>Cancel</button><button class="button primary" type="submit" disabled={taskModalCreating || taskModalLoading || !taskModalTitle.trim()}>{#if taskModalCreating}<span class="button-spinner"></span>{/if}Create task</button></div>
        </form>
      </div>
    {/if}

    {#if showBugModal}
      <div class="modal-backdrop" role="presentation" on:click={closeBugModal}></div>
      <div class="modal bug-create-modal" role="dialog" aria-modal="true" aria-labelledby="bug-modal-title" use:focusTrap>
        <div class="modal-header"><div><span class="eyebrow">Capture a regression</span><h2 id="bug-modal-title">Report a bug</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeBugModal}>×</button></div>
        {#if bugModalError}<div class="inline-alert error" role="alert"><span>!</span>{bugModalError}</div>{/if}
        <form on:submit|preventDefault={reportBug}>
          <div class="form-row task-destination-row"><label>Project<select bind:value={bugModalProjectId} on:change={() => void changeBugModalProject()}>{#each projects as project}<option value={project.id}>{project.key} · {project.name}</option>{/each}</select></label><label>Column<select bind:value={bugModalColumnId} disabled={bugModalLoading || !bugModalColumns.length}>{#each bugModalColumns as column}<option value={column.id}>{column.name}</option>{/each}</select></label></div>
          <label>Bug title<input data-dialog-initial-focus bind:value={bugModalTitle} placeholder="What went wrong?" /></label>
          <label>Actual behavior<textarea rows="3" bind:value={bugModalActual} placeholder="What happened?" required></textarea></label>
          <label>Expected behavior<textarea rows="2" bind:value={bugModalExpected} placeholder="What should have happened?"></textarea></label>
          <label>Reproduction steps <span class="optional">Optional</span><textarea rows="2" bind:value={bugModalReproduction} placeholder="1. Open…&#10;2. Click…"></textarea></label>
          <div class="form-row"><label>Environment <span class="optional">Optional</span><input bind:value={bugModalEnvironment} placeholder="Browser, OS, device" /></label><label>Affected version <span class="optional">Optional</span><input bind:value={bugModalVersion} placeholder="e.g. 1.4.0" /></label></div>
          <div class="form-row"><label>Severity <span class="optional">Optional · triage later</span><select aria-label="Initial bug severity" bind:value={bugModalSeverity}><option value="">Untriaged</option><option value="s1">{severityLabels.s1}</option><option value="s2">{severityLabels.s2}</option><option value="s3">{severityLabels.s3}</option><option value="s4">{severityLabels.s4}</option></select></label><label>Priority<select bind:value={bugModalPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label></div>
          <label>Description <span class="optional">Optional · Markdown supported</span><textarea rows="2" bind:value={bugModalDescription} placeholder="Add context beyond the reproduction details."></textarea></label>
          <label>Labels <span class="optional">Optional · comma separated</span><input bind:value={bugModalLabels} placeholder="frontend, regression" /></label>
          <div class="modal-actions"><button class="text-button" type="button" on:click={closeBugModal}>Cancel</button><button class="button primary" type="submit" disabled={bugModalCreating || bugModalLoading || !bugModalTitle.trim() || !bugModalActual.trim()}>{#if bugModalCreating}<span class="button-spinner"></span>{/if}Report bug</button></div>
        </form>
      </div>
    {/if}

    {#if showProjectModal}
      <div class="modal-backdrop" role="presentation" on:click={closeProjectModal}></div>
      <div class="modal project-modal" role="dialog" aria-modal="true" aria-labelledby="project-modal-title" use:focusTrap><div class="modal-header"><div><span class="eyebrow">New workspace</span><h2 id="project-modal-title">Create a project</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeProjectModal}>×</button></div>{#if projectFormError}<div class="inline-alert error" role="alert"><span>!</span>{projectFormError}</div>{/if}<form on:submit|preventDefault={createProject}><div class="project-form-title"><span class="project-dot huge" style={`--project-color: ${projectColorDraft}`}>{projectInitials({ name: projectNameDraft || 'New project', key: projectKeyDraft || 'NP' })}</span><div><label>Project name<input data-dialog-initial-focus bind:value={projectNameDraft} placeholder="Product launch" /></label></div></div><div class="form-row"><label>Project key<input maxlength="16" bind:value={projectKeyDraft} placeholder="PROD" /></label><label>Accent color<input class="color-input" type="color" bind:value={projectColorDraft} /></label></div><label>Description <span class="optional">Optional</span><textarea rows="3" bind:value={projectDescriptionDraft} placeholder="A short note about what this project is for."></textarea><span class="field-hint">Roadmap will add Backlog, Ready, In progress, Blocked, and Done columns automatically.</span></label><div class="modal-actions"><button class="text-button" type="button" on:click={closeProjectModal}>Cancel</button><button class="button primary" type="submit" disabled={projectCreating || !projectNameDraft.trim() || !projectKeyDraft.trim()}>{#if projectCreating}<span class="button-spinner"></span>{/if}Create project</button></div></form></div>
    {/if}

    {#if revealedToken}
      <div class="modal-backdrop" role="presentation"></div>
      <div class="modal token-reveal-modal" role="alertdialog" aria-modal="true" aria-labelledby="token-reveal-title" use:focusTrap><div class="token-reveal-icon">✓</div><span class="eyebrow">One-time secret</span><h2 id="token-reveal-title">Copy your token now</h2><p>For your security, the token will not be shown again after closing this dialog.</p><div class="token-value"><code>{revealedToken.plaintext || revealedToken.token || ''}</code><button class="icon-button" type="button" aria-label="Copy token" on:click={() => void copyRevealedToken()}>⧉</button></div><button class="button primary button-large" type="button" data-dialog-initial-focus on:click={closeTokenReveal}>I’ve copied it</button></div>
    {/if}

    <div class="sr-only" aria-live="polite" aria-atomic="true">{liveAnnouncement}</div>
    <div class="toast-stack" aria-live="polite" aria-atomic="true">{#each toasts as item (item.id)}<div class={`toast ${item.kind}`}><span>{item.kind === 'success' ? '✓' : item.kind === 'error' ? '!' : 'i'}</span>{item.message}<button class="icon-button tiny" type="button" aria-label="Dismiss notification" on:click={() => toasts = toasts.filter((toastItem) => toastItem.id !== item.id)}>×</button></div>{/each}</div>
  </div>
{/if}
