<script module lang="ts">
  export type BoardRefreshScope = 'full' | 'targeted';
  export type BoardMetadataErrorState = {
    full: string;
    targeted: Readonly<Record<string, string>>;
  };
  /** Full metadata success clears all uncertainty; targeted refreshes own their columns. */
  export function boardMetadataErrorAfterRefresh(
    current: BoardMetadataErrorState,
    scope: BoardRefreshScope,
    columnIds: readonly string[],
    nextError: string
  ): BoardMetadataErrorState {
    if (scope === 'full') return { full: nextError, targeted: {} };
    const targeted = { ...current.targeted };
    columnIds.forEach((columnId) => {
      if (nextError) targeted[columnId] = nextError;
      else delete targeted[columnId];
    });
    return { full: current.full, targeted };
  }

  export function boardMetadataErrorMessage(state: BoardMetadataErrorState): string {
    return state.full || Object.values(state.targeted)[0] || '';
  }

  /** Refresh ownership is per target column, not a global latest-request flag. */
  export function boardRefreshTargetsAreCurrent(
    refreshes: Readonly<Record<string, number>>,
    columnIds: readonly string[],
    refreshToken: number
  ): boolean {
    return columnIds.every((columnId) => refreshes[columnId] === refreshToken);
  }

  /** Merge only the columns owned by a targeted response; other responses cannot clobber them. */
  export function mergeOwnedBoardMetadata<T extends { id: string }>(
    current: readonly T[],
    refreshed: readonly T[],
    ownedColumnIds: readonly string[]
  ): T[] {
    const owned = new Set(ownedColumnIds);
    const refreshedById = new Map(refreshed.map((column) => [column.id, column]));
    return current.map((column) => owned.has(column.id) ? refreshedById.get(column.id) || column : column);
  }

  /** Success/reconciliation announcements require both live mutation context and reload success. */
  export function boardMutationReloadCanAnnounce(
    mutationIsCurrent: boolean,
    reloadSucceeded: boolean
  ): boolean {
    return mutationIsCurrent && reloadSucceeded;
  }

  /** A truncated global event page may hide a newer event for this board. */
  export function boardEventPageRequiresRefresh(
    boardChanged: boolean,
    nextCursor: string | null | undefined
  ): boolean {
    return boardChanged || Boolean(nextCursor);
  }
</script>

<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { flip } from 'svelte/animate';
  import { backOut } from 'svelte/easing';
  import { fade, fly, scale } from 'svelte/transition';
  import { API_PREFIX, api, listAllIssues, listAllTasks, portableImportReportFromError, unwrapActor, type TaskListParams } from './lib/api';
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
    dropIndexForPointer,
    filterTasks,
    formatDate,
    formatRelative,
    helmStorageKeys,
    isDueSoon,
    isAgentWorkStale as isWorkStale,
    isMissingAgentWorkCandidate,
    isOverdue,
    legacyRoadmapStorageKeys,
    loadRecentProjects,
    matchesAgentWorkFilter,
    projectInitials,
    readMigratedStorage,
    parseTaskRoute,
    rememberProject,
    taskOrderingAnchors,
    taskDeepLink,
    shouldShowAgentPulse,
    toInputDate,
    type BoardFilters
  } from './lib/state';
  import {
    dependencyActionExplanation,
    dependencyBlocked,
    dependencyEventTaskIds,
    dependencyMoveExplanation
  } from './lib/dependencies';
  import { hierarchyBadgeLabel, hierarchyEventTaskIds } from './lib/hierarchy';
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
    type CodexAccountStatus,
    type CodexDeviceLogin,
    type IssueMetrics,
    type Comment,
    type Label,
    type Project,
    type RoadmapActivityFilter,
    type RoadmapSummary,
    type SidebarCounts,
    type SavedView,
    type Task,
    type TaskDraftSuggestion,
    type TaskReference,
    type TaskHierarchyReference,
    type TaskTimelineFilter,
    type TaskTimelineKind,
    type TaskTimelineItem,
    type TaskRouteIntent,
    type Priority,
    type SemanticState,
    type PortableArchive,
    type PortableImportReport
  } from './lib/types';
  import AgentPulse from './lib/components/AgentPulse.svelte';
  import AgentWorkPanel from './lib/components/AgentWorkPanel.svelte';
  import AuditReview from './lib/components/AuditReview.svelte';
  import BoardTimeline from './lib/components/BoardTimeline.svelte';
  import ConfirmDialog from './lib/components/ConfirmDialog.svelte';
  import HelmMark from './lib/components/HelmMark.svelte';
  import LiveWorkRow from './lib/components/LiveWorkRow.svelte';
  import RoadmapActivity from './lib/components/RoadmapActivity.svelte';
  import RoadmapLiveWork from './lib/components/RoadmapLiveWork.svelte';
  import TaskActivityTimeline from './lib/components/TaskActivityTimeline.svelte';
  import TaskChecklist from './lib/components/TaskChecklist.svelte';
  import TaskDependencies from './lib/components/TaskDependencies.svelte';
  import TaskDependencyStatus from './lib/components/TaskDependencyStatus.svelte';
  import TaskHierarchy from './lib/components/TaskHierarchy.svelte';
  import {
    mergeAuthoritativeTask,
    mergeAuthoritativeTaskList,
    taskMutationsAfter,
    type TaskMutationKind,
    type TaskMutationRecord,
    type TaskMutationScope
  } from './lib/liveness';
  import {
    isEditableTarget,
    platformShortcut,
    themeFromMediaPreference,
    toastAccessibility,
    type ToastAccessibility
  } from './lib/ui';
  import {
    buildCommandChoices,
    commandChoiceId,
    filterCommandChoices,
    nextCommandIndex,
    type CommandChoice,
    type CommandView
  } from './lib/commandPalette';
  import { queueBoardTimelineLoad } from './lib/boardTimeline';
  import {
    mergeTimelineItems,
    reconcileTimelineComments,
    type TimelineCommentReconciliation,
    type TimelineCommentReconciliationResult
  } from './lib/timeline';
  import {
    boardColumnHasKnownGlobalBounds,
    boardColumnRequestIsCurrent,
    boardOrderingFiltersActive,
    boardOrderingRefreshReason,
    boardOrderingUsesPhysicalOrder,
    claimBoardColumnRequest,
    isRecoverableBoardCursorConflict,
    makeBoardOrderingGate,
    sortBoardTasks as sortBoardTaskList,
    type BoardOrderingGate,
    type BoardTaskSort
  } from './lib/boardOrdering';

  type View = CommandView;
  type AuthView = 'login' | 'setup';
  type ToastKind = 'success' | 'error' | 'info';
  type ToastAction = {
    label: string;
    run: () => void | Promise<void>;
    pending?: boolean;
  };
  type ToastItem = { id: number; kind: ToastKind; message: string; action?: ToastAction } & ToastAccessibility;
  type ConfirmRequest = {
    title: string;
    message: string;
    confirmLabel: string;
    fallbackSelector?: string;
    resolve: (confirmed: boolean) => void;
  };
  type WorkFilter = 'all' | 'action-needed' | 'working' | 'waiting' | 'verifying' | 'stale' | 'handoff' | 'missing';
  type MyWorkView = 'live' | 'assigned';
  type CountStatus = 'unknown' | 'loading' | 'known' | 'error';
  type DrawerView = 'details' | 'activity';
  type MyWorkRow = { task: Task; project?: Project; column?: Column };
  type TaskModalSuggestionField = 'title' | 'description' | 'priority';
  type TaskModalAppliedFields = Record<TaskModalSuggestionField, boolean>;
  type DialogReturnFocus = { element: HTMLElement | null; fallbackSelector: string };
  type OpenTaskOptions = {
    skipDiscardGuard?: boolean;
    returnFocus?: DialogReturnFocus | null;
  };
  type AdminConfirmation = {
    title: string;
    message: string;
    confirmLabel: string;
    action: () => Promise<void>;
  };
  type BoardSort = BoardTaskSort;
  type BoardColumnPage = {
    tasks: Task[];
    nextCursor: string;
    loading: boolean;
    loaded: boolean;
    error: string;
  };
  type BoardLoadOptions = { criteriaRevision?: number };
  type BoardColumnLoadOptions = {
    reset?: boolean;
    mutationSnapshot?: number;
    recoveryNotice?: boolean;
    announceChanges?: boolean;
  };
  type BoardMutationContext = {
    requestId: number;
    boardRequest: number;
    session: number;
    mutationRevision: number;
    projectId: string;
    projectSlug: string;
  };

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
  const claimWarningThresholdMs = 5 * 60 * 1000;

  let booting = true;
  let authStatus: AuthStatus | null = null;
  let user: Actor | null = null;
  let authView: AuthView = 'login';
  let authSubmitting = false;
  let authError = '';
  let authBootstrapFailed = false;
  let bootstrapController: AbortController | undefined;
  let bootstrapRequest = 0;
  let loginEmail = '';
  let loginPassword = '';
  let setupName = '';
  let setupEmail = '';
  let setupPassword = '';

  const accessBootstrapKey = 'helm.cloudflare-access-bootstrap';
  const legacyAccessBootstrapKey = 'roadmap.cloudflare-access-bootstrap';
  const bootstrapTimeoutMs = 12_000;

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
  let boardMetadataErrors: BoardMetadataErrorState = { full: '', targeted: {} };
  let boardPartial = false;
  let boardOffline = false;
  let boardReconciliationNotice = '';
  let boardPages: Record<string, BoardColumnPage> = {};
  let boardColumnGenerations: Record<string, number> = {};
  let boardColumnRefreshSequence = 0;
  let boardColumnRefreshes: Record<string, number> = {};
  let boardCriteriaRevision = 0;
  let boardCriteriaTransition = false;
  let boardMutationRequest = 0;
  let boardFilterTimer: number | undefined;
  let boardSort: BoardSort = 'position';
  let boardOrder: 'asc' | 'desc' = 'asc';
  const boardPageSize = 50;
  let issueTasks: Task[] = [];
  let issueColumns: Column[] = [];
  let issuesLoading = false;
  let issuesError = '';
  let issueRequest = 0;
  let issueMetricsData: IssueMetrics | null = null;
  let issueMetricsStatus: CountStatus = 'unknown';
  let issueMetricsRequest = 0;
  let filters: BoardFilters = { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all', dependency: 'all' };
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
  let projectPickerTrigger: HTMLButtonElement | null = null;
  let projectSwitcherPopover: HTMLDivElement | null = null;
  let commandOpen = false;
  let commandQuery = '';
  let commandIndex = 0;
  let commandInput: HTMLInputElement;
  let commandShortcut = platformShortcut('');
  let boardSearchInput: HTMLInputElement | null = null;
  let issueSearchInput: HTMLInputElement | null = null;
  let commandIssuesLoading = false;
  let commandIssuesError = '';
  let commandIssuesRequest = 0;
  let activeCommandOptionId = '';
  let commandSearchTasks: Task[] = [];
  let commandSearchProjects: Project[] = [];
  let commandSearchViews: SavedView[] = [];
  let commandSearchLoading = false;
  let commandSearchRequest = 0;
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
  let dialogReturnFocus: DialogReturnFocus | null = null;
  let confirmRequest: ConfirmRequest | null = null;

  let drawerTask: Task | null = null;
  let drawerDependencyPanel: { refreshRelationships: () => Promise<boolean> } | null = null;
  let drawerDependencyRefresh = 0;
  let drawerHierarchyPanel: { refreshRelationships: () => Promise<boolean> } | null = null;
  let drawerHierarchyRefresh = 0;
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
  let boardTimelineLoadQueue: Promise<boolean> = Promise.resolve(true);
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
  // Keep the drawer's saved baseline separate from the authoritative task
  // model. Background refreshes may update the latter without replacing the
  // fields a person is currently editing.
  let drawerSavedTaskDraftFingerprint = '';
  let drawerSavedActionDraftFingerprint = '';
  let drawerTaskDraftFingerprintValue = '';
  let drawerActionDraftFingerprintValue = '';
  let drawerTaskDraftDirty = false;
  let drawerDraftDirty = false;

  let draggingTaskId = '';
  let dragOverColumnId = '';
  let quickAddColumn = '';
  let quickAddTitle: Record<string, string> = {};
  let quickAddInput: HTMLInputElement | null = null;
  let quickAddReturnFocus: HTMLButtonElement | null = null;
  let taskActionLoading = '';
  let labelDeleting = '';

  let myWorkTasks: Task[] = [];
  let myWorkLoading = false;
  let myWorkError = '';
  let myWorkFilter: WorkFilter = 'all';
  let myWorkView: MyWorkView = 'live';
  let sidebarCounts: SidebarCounts | null = null;
  let sidebarCountsStatus: CountStatus = 'unknown';
  let sidebarCountsRequest = 0;
  let myWorkColumnsByProject: Record<string, Column[]> = {};
  let myWorkColumnsLoading = false;
  let searchTasks: Task[] = [];
  let searchColumnsByProject: Record<string, Column[]> = {};
  let searchSavedViews: SavedView[] = [];
  let searchViewId = '';
  let searchQuery = '';
  let searchPriority = 'all';
  let searchState = 'all';
  let searchSortField = 'updated_at';
  let searchSortDirection: 'asc' | 'desc' = 'desc';
  let searchNextCursor = '';
  let searchLoading = false;
  let searchError = '';
  let searchRequest = 0;
  let savedViewName = '';
  let savedViewShared = false;
  let savedViewSaving = false;
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
  let codexAccount: CodexAccountStatus | null = null;
  let codexLogin: CodexDeviceLogin | null = null;
  let codexStatusLoading = false;
  let codexLoading = false;
  let codexError = '';

  // Project administration intentionally lives alongside the existing
  // settings state. The board continues to consume only live columns while
  // this view keeps archived rows available for restore.
  let adminProjects: Project[] = [];
  let adminProjectId = '';
  let adminProject: Project | undefined;
  let adminProjectName = '';
  let adminProjectDescription = '';
  let adminProjectColor = '#6d5efc';
  let adminChecklistPolicy: 'warn' | 'require' = 'warn';
  let adminColumns: Column[] = [];
  let adminLiveColumns: Column[] = [];
  let adminLiveColumnIndexes = new Map<string, number>();
  let adminLoading = false;
  let adminSaving = false;
  let adminColumnsLoading = false;
  let adminColumnSaving = '';
  let adminError = '';
  let adminColumnName = '';
  let adminColumnState: SemanticState = 'backlog';
  let adminColumnCreating = false;
  let adminConfirmation: AdminConfirmation | null = null;
  let adminConfirmationBusy = false;

  let showProjectModal = false;
  let projectCreating = false;
  let projectFormError = '';
  let projectKeyDraft = '';
  let projectNameDraft = '';
  let projectDescriptionDraft = '';
  let projectColorDraft = '#6d5efc';
  let portableFileInput: HTMLInputElement;
  let portableBusy = false;
  let portablePreview: { archive: PortableArchive; fileName: string; report: PortableImportReport } | null = null;
  let portablePreviewError = '';
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
  let taskModalParentId: string | null = null;
  let taskModalIdea = '';
  let taskModalSuggestion: TaskDraftSuggestion | null = null;
  let taskModalAssisting = false;
  let taskModalAssistStage = '';
  let taskModalNeedsCodex = false;
  let taskModalAssistController: AbortController | null = null;
  let taskModalAppliedFields: TaskModalAppliedFields = { title: false, description: false, priority: false };
  let taskModalApplyNotice = '';
  let taskModalHasAppliedAll = false;
  let taskModalSuggestionCollapsed = false;
  let taskModalTitleApplied = false;
  let taskModalDescriptionApplied = false;
  let taskModalPriorityApplied = false;
  let taskModalAllFieldsApplied = false;

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
  let toasts: ToastItem[] = [];

  $: activeProject = projects.find((project) => project.slug === activeProjectSlug);
  $: adminProject = adminProjects.find((project) => project.id === adminProjectId);
  $: adminLiveColumns = adminColumns.filter((column) => !column.archived_at).sort((a, b) => a.position - b.position);
  $: adminLiveColumnIndexes = new Map(adminLiveColumns.map((column, index) => [column.id, index]));
  $: visibleTasks = filterTasks(tasks, columns, filters).filter((task) => matchesWorkFilter(task, boardWorkFilter, pulseClock));
  $: boardWorkCounts = agentWorkStatusCounts(tasks, pulseClock, (task) => semanticStateForTask(task));
  $: visibleIssues = filterTasks(
    issueTasks.filter((task) => issueProjectFilter === 'all' || task.project_id === issueProjectFilter),
    issueColumns,
    issueFilters
  );
  $: issueReporterOptions = Array.from(new Set(issueTasks.map((task) => bugReporterId(task)).filter(Boolean))).sort();
  $: issueHealthMetrics = {
    open: issueTasks.filter((task) => !task.bug?.resolution).length,
    untriaged: issueTasks.filter((task) => !task.bug?.severity && !task.bug?.resolution).length,
    severe: issueTasks.filter((task) => !task.bug?.resolution && (task.bug?.severity === 's1' || task.bug?.severity === 's2')).length,
    recentlyResolved: issueTasks.filter((task) => task.bug?.resolved_at && Date.now() - Date.parse(task.bug.resolved_at) <= 7 * 24 * 60 * 60 * 1000).length,
    reopened: issueMetricsStatus === 'known' ? issueMetricsData?.reopened ?? null : null
  };
  $: if (view === 'issues') syncIssueViewURL(issueFilters, issueProjectFilter);
  $: sortedColumns = [...columns].sort((a, b) => a.position - b.position);
  // Keep the board's column buckets as a reactive value. Calling a helper
  // from the template does not give Svelte a dependency on visibleTasks, so
  // mutations could otherwise leave cards/counts rendered in their old
  // column until an unrelated update occurred.
  $: tasksByColumn = sortedColumns.reduce<Record<string, Task[]>>((groups, column) => {
    groups[column.id] = sortBoardTasks(visibleTasks.filter((task) => task.column_id === column.id));
    return groups;
  }, {});
  $: favoriteProjects = projects.filter((project) => project.favorite);
  $: recentProjects = recentProjectIds
    .map((id) => projects.find((project) => project.id === id))
    .filter((project): project is Project => Boolean(project));
  $: filteredSwitcherProjects = projects.filter((project) =>
    `${project.name} ${project.key}`.toLowerCase().includes(projectSwitcherQuery.trim().toLowerCase())
  );
  $: commandChoices = filterCommandChoices(buildCommandChoices({
    projects,
    tasks,
    issueTasks,
    searchProjects: commandSearchProjects,
    searchTasks: commandSearchTasks,
    savedViews: commandSearchViews,
    theme
  }), commandQuery);
  $: if (commandIndex >= commandChoices.length) commandIndex = Math.max(0, commandChoices.length - 1);
  $: activeCommandOptionId = commandChoices[commandIndex] ? commandChoiceId(commandChoices[commandIndex]) : '';
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
  $: taskModalTitleApplied = Boolean(taskModalSuggestion && taskModalAppliedFields.title && taskModalTitle === taskModalSuggestion.title);
  $: taskModalDescriptionApplied = Boolean(taskModalSuggestion && taskModalAppliedFields.description && taskModalDescription === suggestionDescription(taskModalSuggestion));
  $: taskModalPriorityApplied = Boolean(taskModalSuggestion && taskModalAppliedFields.priority && taskModalPriority === taskModalSuggestion.priority);
  $: taskModalAllFieldsApplied = taskModalTitleApplied && taskModalDescriptionApplied && taskModalPriorityApplied;
  // Keep these reactive expressions explicit: Svelte tracks identifiers in a
  // statement, not values reached indirectly through a helper function.
  $: drawerTaskDraftFingerprintValue = JSON.stringify([
    draftTitle,
    draftDescription,
    draftPriority,
    draftDueDate,
    draftAssignee,
    draftLabels,
    draftBugActual,
    draftBugExpected,
    draftBugReproduction,
    draftBugEnvironment,
    draftBugVersion
  ]);
  $: drawerActionDraftFingerprintValue = JSON.stringify([
    triageSeverityDraft,
    resolutionDraft,
    duplicateOfDraft,
    resolutionNoteDraft,
    reopenReasonDraft,
    blockReasonDraft,
    commentBody
  ]);
  $: drawerTaskDraftDirty = Boolean(drawerTask && drawerTaskDraftFingerprintValue !== drawerSavedTaskDraftFingerprint);
  $: drawerDraftDirty = Boolean(
    drawerTask
    && (drawerTaskDraftDirty || drawerActionDraftFingerprintValue !== drawerSavedActionDraftFingerprint)
  );
  $: searchView = searchSavedViews.find((item) => item.id === searchViewId);

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
      // A dialog may deliberately place initial focus on a neutral, programmatic
      // target (tabindex=-1) instead of moving a user straight into a field.
      const target = initial && isFocusableVisible(initial) ? initial : focusable[0];
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

  function emptyBoardColumnPage(): BoardColumnPage {
    return { tasks: [], nextCursor: '', loading: false, loaded: false, error: '' };
  }

  function claimBoardColumnRequestGeneration(columnId: string): number {
    const claim = claimBoardColumnRequest(boardColumnGenerations, columnId);
    boardColumnGenerations = claim.generations;
    return claim.generation;
  }

  function invalidateBoardColumnRequests(columnIds: string[]) {
    let next = boardColumnGenerations;
    [...new Set(columnIds)].forEach((columnId) => {
      next = claimBoardColumnRequest(next, columnId).generations;
    });
    boardColumnGenerations = next;
  }

  function beginBoardColumnRefresh(columnIds: string[]): number {
    const refreshToken = ++boardColumnRefreshSequence;
    const next = { ...boardColumnRefreshes };
    [...new Set(columnIds)].forEach((columnId) => {
      next[columnId] = refreshToken;
    });
    boardColumnRefreshes = next;
    return refreshToken;
  }

  function endBoardColumnRefresh(columnIds: string[], refreshToken: number) {
    const next = { ...boardColumnRefreshes };
    [...new Set(columnIds)].forEach((columnId) => {
      if (next[columnId] === refreshToken) delete next[columnId];
    });
    boardColumnRefreshes = next;
  }

  function beginBoardMutation(): BoardMutationContext | null {
    if (!activeProject || !user) return null;
    clearAnnouncement();
    return {
      requestId: ++boardMutationRequest,
      boardRequest,
      session: sessionGeneration,
      mutationRevision: taskMutationRevision,
      projectId: activeProject.id,
      projectSlug: activeProjectSlug
    };
  }

  function boardMutationIsCurrent(context: BoardMutationContext): boolean {
    return context.requestId === boardMutationRequest
      && context.boardRequest === boardRequest
      && context.session === sessionGeneration
      && context.mutationRevision === taskMutationRevision
      && context.projectId === activeProject?.id
      && context.projectSlug === activeProjectSlug
      && Boolean(user);
  }

  function boardFiltersActive(): boolean {
    return Boolean(
      filters.query.trim()
      || filters.priority !== 'all'
      || filters.label !== 'all'
      || filters.assignee !== 'all'
      || filters.state !== 'all'
      || filters.dependency !== 'all'
      || boardWorkFilter !== 'all'
    );
  }

  /** Build one bounded, server-filtered request for one board column. */
  function boardTaskParams(columnId: string, cursor = ''): TaskListParams {
    const params: TaskListParams = {
      column: columnId,
      sort: boardSort,
      order: boardOrder,
      limit: boardPageSize
    };
    if (cursor) params.cursor = cursor;
    if (filters.state !== 'all') params.state = filters.state;
    if (filters.priority !== 'all') params.priority = filters.priority;
    if (filters.label !== 'all') params.label = filters.label;
    if (filters.assignee !== 'all') params.assignee = filters.assignee;
    if (filters.dependency !== 'all') params.dependency = filters.dependency;
    if (filters.query.trim()) params.q = filters.query.trim();
    if (boardWorkFilter === 'action-needed') {
      params.action_needed = true;
    } else if (boardWorkFilter !== 'all') {
      params.agent_state = boardWorkFilter;
    }
    return params;
  }

  function sortBoardTasks(items: Task[]): Task[] {
    return sortBoardTaskList(items, boardSort, boardOrder);
  }

  function flattenBoardPages(): Task[] {
    const seen = new Set<string>();
    const flattened: Task[] = [];
    sortedColumns.forEach((column) => {
      for (const task of boardPages[column.id]?.tasks || []) {
        if (seen.has(task.id)) continue;
        seen.add(task.id);
        flattened.push(task);
      }
    });
    return flattened;
  }

  function boardTaskMatches(task: Task, columnId = task.column_id): boolean {
    return Boolean(
      activeProject
      && task.project_id === activeProject.id
      && task.column_id === columnId
      && filterTasks([task], columns, filters).length
      && matchesWorkFilter(task, boardWorkFilter)
    );
  }

  /** Keep a just-mutated task visible only when its loaded page can represent it. */
  function syncBoardTaskPages(updated: Task, announceChanges = true) {
    if (!Object.keys(boardPages).length) return;
    let changed = false;
    const nextPages: Record<string, BoardColumnPage> = { ...boardPages };
    Object.entries(boardPages).forEach(([columnId, page]) => {
      const hadTask = page.tasks.some((task) => task.id === updated.id);
      const shouldInclude = boardTaskMatches(updated, columnId) && (hadTask || page.loaded);
      const retained = page.tasks.filter((task) => task.id !== updated.id);
      if (shouldInclude) retained.push(updated);
      const nextTasks = sortBoardTasks(retained);
      if (
        nextTasks.length !== page.tasks.length
        || nextTasks.some((task, index) => task.id !== page.tasks[index]?.id || task.version !== page.tasks[index]?.version)
      ) {
        changed = true;
        nextPages[columnId] = { ...page, tasks: nextTasks };
      }
      if (announceChanges && hadTask && !shouldInclude) {
        boardReconciliationNotice = `${updated.key} moved outside the loaded board page or current filters.`;
        announce(boardReconciliationNotice);
      }
    });
    if (changed) {
      boardPages = nextPages;
      tasks = flattenBoardPages();
      observeWorkTransitions(tasks, announceChanges);
    }
  }

  function removeTaskFromBoardPages(taskId: string) {
    let changed = false;
    const nextPages: Record<string, BoardColumnPage> = { ...boardPages };
    Object.entries(boardPages).forEach(([columnId, page]) => {
      if (!page.tasks.some((task) => task.id === taskId)) return;
      changed = true;
      nextPages[columnId] = { ...page, tasks: page.tasks.filter((task) => task.id !== taskId) };
    });
    if (changed) {
      boardPages = nextPages;
      tasks = flattenBoardPages();
    }
  }

  function scheduleBoardReload() {
    boardCriteriaRevision += 1;
    boardCriteriaTransition = true;
    const criteriaRevision = boardCriteriaRevision;
    if (boardFilterTimer) window.clearTimeout(boardFilterTimer);
    boardFilterTimer = window.setTimeout(() => {
      boardFilterTimer = undefined;
      if (criteriaRevision === boardCriteriaRevision && view === 'board' && activeProject) {
        void loadBoard({ criteriaRevision });
      } else if (criteriaRevision === boardCriteriaRevision) {
        boardCriteriaTransition = false;
      }
    }, 160);
  }

  function mergeBoardPageTasks(
    existing: Task[],
    fetched: Task[],
    protectedMutations: ReadonlyMap<string, TaskMutationKind>
  ): Task[] {
    const fetchedById = new Map(fetched.map((task) => [task.id, task]));
    const localById = new Map(existing.map((task) => [task.id, task]));
    const merged = new Map<string, Task>();
    existing.forEach((task) => merged.set(task.id, task));
    fetched.forEach((task) => {
      if (protectedMutations.get(task.id) === 'remove') return;
      const local = localById.get(task.id);
      merged.set(task.id, mergeAuthoritativeTask(local, task));
    });
    protectedMutations.forEach((kind, taskId) => {
      if (kind !== 'upsert' || fetchedById.has(taskId) || merged.has(taskId)) return;
      const local = tasks.find((task) => task.id === taskId);
      if (local) merged.set(taskId, local);
    });
    return [...merged.values()].filter((task) => boardTaskMatches(task));
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

  function claimExpiresSoon(task: Task, now = pulseClock): boolean {
    if (!task.claim_expires_at || !claimIsActive(task, now)) return false;
    const expires = Date.parse(task.claim_expires_at);
    return Number.isFinite(expires) && expires - now <= claimWarningThresholdMs;
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

  function observeWorkTransitions(nextTasks: Task[], announceTransitions = true) {
    const next = new Map(workTransitionSnapshot);
    nextTasks.forEach((task) => {
      const key = workTransitionKey(task);
      next.set(task.id, key);
      const previous = workTransitionSnapshot.get(task.id);
      if (announceTransitions && previous && key && previous !== key) {
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

  function clearAnnouncement() {
    liveAnnouncement = '';
    if (announcementTimer) {
      window.clearTimeout(announcementTimer);
      announcementTimer = undefined;
    }
    boardReconciliationNotice = '';
  }

  function browserPlatform(): string {
    if (typeof navigator === 'undefined') return '';
    const navigatorWithUserAgentData = navigator as Navigator & { userAgentData?: { platform?: string } };
    return navigatorWithUserAgentData.userAgentData?.platform || navigator.platform || navigator.userAgent || '';
  }

  function systemTheme(): 'light' | 'dark' {
    const prefersDark = typeof window !== 'undefined'
      && typeof window.matchMedia === 'function'
      && window.matchMedia('(prefers-color-scheme: dark)').matches;
    return themeFromMediaPreference(prefersDark);
  }

  onMount(() => {
    const storedTheme = readMigratedStorage(localStorage, helmStorageKeys.theme, legacyRoadmapStorageKeys.theme);
    theme = storedTheme === 'light' || storedTheme === 'dark' ? storedTheme : systemTheme();
    commandShortcut = platformShortcut(browserPlatform());
    applyTheme();
    recentProjectIds = loadRecentProjects(localStorage);
    boardOffline = !navigator.onLine;
    const onlineHandler = () => {
      boardOffline = false;
      if (activeProject && view === 'board') void loadBoard();
    };
    const offlineHandler = () => {
      boardOffline = true;
    };
    window.addEventListener('online', onlineHandler);
    window.addEventListener('offline', offlineHandler);
    const cleanup = () => {
      if (pollTimer) window.clearInterval(pollTimer);
      if (pulseTimer) window.clearInterval(pulseTimer);
      if (livenessRefreshTimer) window.clearInterval(livenessRefreshTimer);
      if (announcementTimer) window.clearTimeout(announcementTimer);
      if (boardFilterTimer) window.clearTimeout(boardFilterTimer);
      window.removeEventListener('online', onlineHandler);
      window.removeEventListener('offline', offlineHandler);
      bootstrapController?.abort();
    };
    void bootstrap();
    const keyHandler = (event: KeyboardEvent) => handleKeydown(event);
    window.addEventListener('keydown', keyHandler);
    window.addEventListener('pointerdown', handleProjectSwitcherPointerDown);
    window.addEventListener('popstate', handlePopState);
    return () => {
      cleanup();
      window.removeEventListener('keydown', keyHandler);
      window.removeEventListener('pointerdown', handleProjectSwitcherPointerDown);
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
    localStorage.setItem(helmStorageKeys.theme, theme);
    applyTheme();
  }

  async function bootstrap() {
    const requestId = ++bootstrapRequest;
    bootstrapController?.abort();
    const controller = new AbortController();
    bootstrapController = controller;
    const timeout = window.setTimeout(() => controller.abort(), bootstrapTimeoutMs);
    booting = true;
    authError = '';
    authBootstrapFailed = false;
    let authStatusLoaded = false;
    try {
      authStatus = await api.authStatus(controller.signal);
      if (requestId !== bootstrapRequest) return;
      authStatusLoaded = true;
      sessionStorage.removeItem(accessBootstrapKey);
      sessionStorage.removeItem(legacyAccessBootstrapKey);
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
        user = authStatus.user || authStatus.actor || (await api.authMe(controller.signal));
        if (requestId !== bootstrapRequest) return;
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
      if (requestId !== bootstrapRequest) return;
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
        readMigratedStorage(sessionStorage, accessBootstrapKey, legacyAccessBootstrapKey) !== window.location.origin
      ) {
        sessionStorage.setItem(accessBootstrapKey, window.location.origin);
        window.location.assign(`${API_PREFIX}/auth/status`);
        return;
      }
      authBootstrapFailed = true;
      authError = controller.signal.aborted
        ? 'Helm took too long to respond. Check your connection and try again.'
        : friendlyError(error, 'Helm could not connect to the server.');
    } finally {
      window.clearTimeout(timeout);
      if (requestId === bootstrapRequest) {
        bootstrapController = undefined;
        booting = false;
      }
    }
  }

  async function finishAuthentication() {
    // Every successful authentication starts a fresh client session. Any
    // request left behind by a previous session must fail its generation
    // check even when the browser logs back in as the same actor.
    sessionGeneration += 1;
    const requestedSession = sessionGeneration;
    // Authentication is enough to reveal the application chrome. Project and
    // board reads can be noticeably slower on a remote self-hosted instance;
    // render their in-context skeleton instead of holding the user on the
    // full-screen bootstrap splash until every request has completed.
    booting = false;
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
    clearAnnouncement();
    sessionGeneration += 1;
    user = null;
    boardMutationRequest += 1;
    taskActionLoading = '';
    projectListRequest += 1;
    boardRequest += 1;
    roadmapRequest += 1;
    roadmapLiveRequest += 1;
    roadmapActivityRequest += 1;
    drawerTimelineRequest += 1;
    boardTimelineRequest += 1;
    taskModalColumnsRequest += 1;
    issueRequest += 1;
    if (drawerTask) closeDrawer(true);
    activeProjectSlug = '';
    auditIdFromRoute = '';
    roadmapProjectId = undefined;
    projects = [];
    columns = [];
    tasks = [];
    labels = [];
    issueTasks = [];
    issueColumns = [];
    issueMetricsData = null;
    issueMetricsStatus = 'unknown';
    issueMetricsRequest += 1;
    myWorkTasks = [];
    myWorkColumnsByProject = {};
    sidebarCounts = null;
    sidebarCountsStatus = 'unknown';
    sidebarCountsRequest += 1;
    searchTasks = [];
    searchColumnsByProject = {};
    searchNextCursor = '';
    searchSavedViews = [];
    searchViewId = '';
    searchError = '';
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
    codexAccount = null;
    codexLogin = null;
    codexStatusLoading = false;
    codexLoading = false;
    codexError = '';
    adminProjects = [];
    adminProjectId = '';
    adminColumns = [];
    adminError = '';
    adminConfirmation = null;
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
      const remembered = readMigratedStorage(localStorage, helmStorageKeys.lastProject, legacyRoadmapStorageKeys.lastProject);
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
      // Navigation counts and issue health are deliberately scalar requests;
      // bootstrap never downloads the full Issues or My Work collections just
      // to render their badges.
      await Promise.all([loadIssueMetrics(), loadSidebarCounts()]);
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
      } else if (/^\/search\/?$/.test(path)) {
        roadmapProjectId = undefined;
        applySearchRouteFilters(new URL(window.location.href).searchParams);
        view = 'search';
        await loadSearch();
      } else if (/^\/settings\/?$/.test(path)) {
        roadmapProjectId = undefined;
        view = 'settings';
        await Promise.all([loadAgents(), loadCodexAccount(), loadProjectAdmin()]);
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

  async function loadBoard(options: BoardLoadOptions = {}): Promise<boolean> {
    if (options.criteriaRevision === undefined && boardFilterTimer) {
      window.clearTimeout(boardFilterTimer);
      boardFilterTimer = undefined;
    }
    const requestId = ++boardRequest;
    boardLivenessRequest += 1;
    const requestedSession = sessionGeneration;
    const mutationSnapshot = taskMutationRevision;
    const requestedSlug = activeProjectSlug;
    const requestedCriteriaRevision = options.criteriaRevision ?? boardCriteriaRevision;
    invalidateBoardColumnRequests(Object.keys(boardPages));
    if (!requestedSlug) {
      boardLoading = false;
      if (requestedCriteriaRevision === boardCriteriaRevision) boardCriteriaTransition = false;
      return true;
    }
    const project = projects.find((item) => item.slug === requestedSlug);
    if (!project) {
      boardLoading = false;
      if (requestedCriteriaRevision === boardCriteriaRevision) boardCriteriaTransition = false;
      return true;
    }
    boardLoading = true;
    boardMetadataErrors = boardMetadataErrorAfterRefresh(boardMetadataErrors, 'full', [], '');
    boardError = '';
    boardPartial = false;
    boardReconciliationNotice = '';
    try {
      const [columnResult, labelResult] = await Promise.all([
        api.listAllColumns(project.id),
        api.listAllLabels(project.id)
      ]);
      if (
        requestId !== boardRequest
        || activeProjectSlug !== requestedSlug
        || sessionGeneration !== requestedSession
        || !user
      ) return false;
      boardMetadataErrors = boardMetadataErrorAfterRefresh(boardMetadataErrors, 'full', [], '');
      boardError = boardMetadataErrorMessage(boardMetadataErrors);
      columns = columnResult.data;
      labels = labelResult.data;
      invalidateBoardColumnRequests(Object.keys(boardPages));
      boardPages = Object.fromEntries(columns.map((column) => [column.id, emptyBoardColumnPage()]));
      tasks = [];
      observeWorkTransitions(tasks);
      const pageResults = await Promise.all(
        [...columns].sort((a, b) => a.position - b.position).map((column) => loadBoardColumn(column.id, { reset: true, mutationSnapshot }))
      );
      return pageResults.every(Boolean);
    } catch (error) {
      if (
        requestId === boardRequest
        && activeProjectSlug === requestedSlug
        && sessionGeneration === requestedSession
        && user
      ) {
        boardMetadataErrors = boardMetadataErrorAfterRefresh(
          boardMetadataErrors,
          'full',
          [],
          friendlyError(error, 'This board could not be loaded.')
        );
        boardError = boardMetadataErrorMessage(boardMetadataErrors);
      }
      return false;
    } finally {
      if (requestId === boardRequest && sessionGeneration === requestedSession) {
        boardLoading = false;
        if (requestedCriteriaRevision === boardCriteriaRevision) boardCriteriaTransition = false;
      }
    }
  }

  async function loadBoardColumn(
    columnId: string,
    options: BoardColumnLoadOptions = {}
  ): Promise<boolean> {
    const {
      reset = false,
      mutationSnapshot = taskMutationRevision,
      recoveryNotice = false,
      announceChanges = true
    } = options;
    const page = boardPages[columnId] || emptyBoardColumnPage();
    if ((!reset && page.loading) || (!reset && !page.nextCursor)) return true;
    const requestedBoardRequest = boardRequest;
    const requestedSession = sessionGeneration;
    const requestedSlug = activeProjectSlug;
    const project = projects.find((item) => item.slug === requestedSlug);
    if (!project || !user) return false;
    const requestedProjectId = project.id;
    const columnGeneration = claimBoardColumnRequestGeneration(columnId);
    const requestIsCurrent = () => (
      requestedBoardRequest === boardRequest
      && requestedSession === sessionGeneration
      && requestedSlug === activeProjectSlug
      && activeProject?.id === requestedProjectId
      && Boolean(user)
      && boardColumnRequestIsCurrent(boardColumnGenerations, columnId, columnGeneration)
    );
    const cursor = reset ? '' : page.nextCursor;
    const existingColumnTasks = reset
      ? tasks.filter((task) => task.column_id === columnId)
      : page.tasks;
    boardPages = {
      ...boardPages,
      [columnId]: { ...page, loading: true, error: '' }
    };
    try {
      const result = await api.listTasks(project.id, boardTaskParams(columnId, cursor));
      if (!requestIsCurrent()) return false;
      const protectedMutations = mutationsForRequest('board', mutationSnapshot);
      const merged = reset
        ? mergeAuthoritativeTaskList(existingColumnTasks, result.data, protectedMutations)
        : mergeBoardPageTasks(existingColumnTasks, result.data, protectedMutations);
      const nextPage: BoardColumnPage = {
        tasks: sortBoardTasks(merged.filter((task) => task.column_id === columnId)),
        nextCursor: result.next_cursor || '',
        loading: false,
        loaded: true,
        error: ''
      };
      boardPages = { ...boardPages, [columnId]: nextPage };
      tasks = flattenBoardPages();
      boardPartial = Object.values(boardPages).some((item) => Boolean(item.error || item.nextCursor));
      boardOffline = false;
      observeWorkTransitions(tasks, announceChanges);
      if (announceChanges && recoveryNotice && requestIsCurrent()) {
        const refreshedMessage = 'This column changed while loading more tasks; its first page was refreshed.';
        boardReconciliationNotice = refreshedMessage;
        announce(refreshedMessage);
      }
      return true;
    } catch (error) {
      if (!requestIsCurrent()) return false;
      if (error instanceof ApiError && isRecoverableBoardCursorConflict(error.status, error.code, reset, cursor)) {
        const currentPage = boardPages[columnId] || page;
        const recoveryMessage = 'This column changed while loading more tasks. Refreshing its first page.';
        boardReconciliationNotice = recoveryMessage;
        announce(recoveryMessage);
        toast('info', recoveryMessage);
        // Clear the in-flight marker before the reset so loadBoardColumn does
        // not short-circuit on the stale page request. The reset path is
        // deliberately one-shot; if it also conflicts, the normal page error
        // state gives the person an explicit retry action.
        boardPages = {
          ...boardPages,
          [columnId]: { ...currentPage, loading: false, error: '' }
        };
        boardPartial = true;
        return loadBoardColumn(columnId, { reset: true, mutationSnapshot, recoveryNotice: true });
      }
      const offlineNow = typeof navigator !== 'undefined' && !navigator.onLine;
      const message = boardOffline || offlineNow
        ? 'You are offline. Reconnect and retry this column.'
        : friendlyError(error, 'This column could not be loaded.');
      boardOffline = offlineNow;
      const currentPage = boardPages[columnId] || page;
      boardPages = {
        ...boardPages,
        [columnId]: { ...currentPage, loading: false, loaded: true, error: message }
      };
      boardPartial = true;
      return false;
    } finally {
      // A newer board or same-column request may have replaced this page while
      // this request was in flight. Its owner is responsible for clearing the
      // loading state and committing its response.
      const current = boardPages[columnId];
      if (requestIsCurrent() && current?.loading) {
        boardPages = { ...boardPages, [columnId]: { ...current, loading: false } };
      }
    }
  }

  /** Invalidate and reload only pages whose physical ordering changed. */
  async function reloadBoardColumns(columnIds: string[]): Promise<boolean> {
    const ids = [...new Set(columnIds)].filter((id) => Boolean(boardPages[id]));
    if (!ids.length || !activeProject) return true;
    const requestedProjectId = activeProject.id;
    const requestedSlug = activeProjectSlug;
    const requestedBoardRequest = boardRequest;
    const requestedSession = sessionGeneration;
    const refreshToken = beginBoardColumnRefresh(ids);
    // Invalidate pending page responses before waiting for refreshed column
    // metadata. The old response must not repopulate a terminal page while
    // this targeted refresh is in flight.
    invalidateBoardColumnRequests(ids);
    // A newer refresh may claim one or more of these columns. Only overlapping
    // targets make this refresh stale; disjoint column refreshes remain safe to
    // complete independently while the board/session checks below protect the
    // shared project context.
    const refreshIsCurrent = () => (
      boardRefreshTargetsAreCurrent(boardColumnRefreshes, ids, refreshToken)
      && requestedBoardRequest === boardRequest
      && requestedSession === sessionGeneration
      && activeProjectSlug === requestedSlug
      && activeProject?.id === requestedProjectId
      && Boolean(user)
    );
    try {
      let columnsRefreshed = true;
      try {
        // Task responses carry the moved row but not the affected column
        // revisions. Refresh those revisions before the next mutation so a
        // subsequent anchor request is not rejected with our own stale value.
        const result = await api.listAllColumns(requestedProjectId);
        if (!refreshIsCurrent()) return false;
        const previousMetadataError = boardMetadataErrorMessage(boardMetadataErrors);
        columns = mergeOwnedBoardMetadata(columns, result.data, ids);
        boardMetadataErrors = boardMetadataErrorAfterRefresh(boardMetadataErrors, 'targeted', ids, '');
        const nextMetadataError = boardMetadataErrorMessage(boardMetadataErrors);
        if (boardError === previousMetadataError) boardError = nextMetadataError;
      } catch (error) {
        if (!refreshIsCurrent()) return false;
        columnsRefreshed = false;
        const metadataError = friendlyError(error, 'Board metadata could not be refreshed. Retry before using global ordering.');
        const previousMetadataError = boardMetadataErrorMessage(boardMetadataErrors);
        boardMetadataErrors = boardMetadataErrorAfterRefresh(
          boardMetadataErrors,
          'targeted',
          ids,
          metadataError
        );
        const nextMetadataError = boardMetadataErrorMessage(boardMetadataErrors);
        if (!boardError || boardError === previousMetadataError) boardError = nextMetadataError;
      }
      if (!refreshIsCurrent()) return false;
      const mutationSnapshot = taskMutationRevision;
      const nextPages: Record<string, BoardColumnPage> = { ...boardPages };
      ids.forEach((columnId) => {
        nextPages[columnId] = emptyBoardColumnPage();
      });
      boardPages = nextPages;
      tasks = flattenBoardPages();
      boardPartial = Object.values(boardPages).some((item) => Boolean(item.error || item.nextCursor));
      observeWorkTransitions(tasks, false);
      if (!refreshIsCurrent()) return false;
      const results = await Promise.all(ids.map((columnId) => loadBoardColumn(columnId, {
        reset: true,
        mutationSnapshot,
        announceChanges: false
      })));
      return columnsRefreshed && results.every(Boolean);
    } finally {
      endBoardColumnRefresh(ids, refreshToken);
    }
  }

  function loadMoreBoardColumn(columnId: string) {
    const page = boardPages[columnId];
    if (!page?.nextCursor || page.loading) return;
    void loadBoardColumn(columnId);
  }

  async function loadBoardTimelinePage(
    projectId = activeProject?.id,
    options: { older?: boolean; reset?: boolean; reconciliation?: TimelineCommentReconciliation; filter?: TaskTimelineFilter } = {}
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
    const requestedFilter = options.filter || boardTimelineFilter;
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
      const existingItems = older ? boardTimelineItems : reset ? [] : boardTimelineItems;
      boardTimelineItems = mergeTimelineItems(existingItems, result.data || [], options.reconciliation);
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
      if (older) boardTimelineLoadingOlder = false;
      else boardTimelineLoading = false;
    }
  }

  function loadBoardTimeline(
    projectId = activeProject?.id,
    options: { older?: boolean; reset?: boolean; reconciliation?: TimelineCommentReconciliation } = {}
  ): Promise<boolean> {
    const requestedFilter = boardTimelineFilter;
    const queued = queueBoardTimelineLoad(
      boardTimelineLoadQueue,
      () => loadBoardTimelinePage(projectId, { ...options, filter: requestedFilter })
    );
    boardTimelineLoadQueue = queued.queue;
    return queued.promise;
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

  async function loadIssueMetrics(): Promise<boolean> {
    const requestId = ++issueMetricsRequest;
    const requestedSession = sessionGeneration;
    issueMetricsStatus = 'loading';
    try {
      const result = await api.issueMetrics();
      if (requestId !== issueMetricsRequest || sessionGeneration !== requestedSession || !user) return false;
      issueMetricsData = result;
      issueMetricsStatus = 'known';
      return true;
    } catch {
      if (requestId === issueMetricsRequest && sessionGeneration === requestedSession && user) {
        issueMetricsStatus = 'error';
      }
      return false;
    }
  }

  async function loadSidebarCounts(requestedView: MyWorkView = myWorkView): Promise<boolean> {
    const requestId = ++sidebarCountsRequest;
    const requestedSession = sessionGeneration;
    sidebarCountsStatus = 'loading';
    try {
      const result = await api.sidebarCounts({ view: requestedView });
      if (
        requestId !== sidebarCountsRequest
        || sessionGeneration !== requestedSession
        || !user
        || myWorkView !== requestedView
      ) return false;
      sidebarCounts = result;
      sidebarCountsStatus = 'known';
      return true;
    } catch {
      if (requestId === sidebarCountsRequest && sessionGeneration === requestedSession && user && myWorkView === requestedView) {
        sidebarCountsStatus = 'error';
      }
      return false;
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
    void loadSidebarCounts(next);
  }

  function searchSortValue(): string {
    return `${searchSortField}:${searchSortDirection}`;
  }

  function runSearch() {
    searchViewId = '';
    const params = new URLSearchParams();
    if (searchQuery.trim()) params.set('q', searchQuery.trim());
    if (searchPriority !== 'all') params.set('priority', searchPriority);
    if (searchState !== 'all') params.set('state', searchState);
    params.set('sort', searchSortValue());
    navigate(`/search${params.toString() ? `?${params.toString()}` : ''}`);
    void loadSearch();
  }

  async function loadSearch(append = false): Promise<boolean> {
    if (append && !searchNextCursor) return true;
    const requestId = ++searchRequest;
    const requestedSession = sessionGeneration;
    const cursor = append ? searchNextCursor : undefined;
    if (!append) searchNextCursor = '';
    searchLoading = true;
    searchError = '';
    try {
      const params = searchViewId
        ? { view: searchViewId, cursor, limit: 200 }
        : {
            q: searchQuery.trim() || undefined,
            priority: searchPriority === 'all' ? undefined : searchPriority,
            state: searchState === 'all' ? undefined : searchState,
            sort: searchSortValue(),
            cursor,
            limit: 200
          };
      const result = await api.search(params);
      if (requestId !== searchRequest || sessionGeneration !== requestedSession || !user || view !== 'search') return false;
      searchTasks = append ? [...searchTasks, ...(result.data || [])] : (result.data || []);
      searchNextCursor = result.next_cursor || '';
      const projectIds = Array.from(new Set(searchTasks.map((task) => task.project_id)));
      const columnResults = await Promise.all(projectIds.map(async (projectId) => ({
        projectId,
        columns: (await api.listAllColumns(projectId)).data
      })));
      if (requestId !== searchRequest || sessionGeneration !== requestedSession || !user || view !== 'search') return false;
      searchColumnsByProject = Object.fromEntries(columnResults.map((result) => [result.projectId, result.columns]));
      if (!searchViewId || !searchSavedViews.some((item) => item.id === searchViewId)) {
        const savedViews = (await api.listAllSavedViews()).data;
        if (requestId !== searchRequest || sessionGeneration !== requestedSession || !user || view !== 'search') return false;
        searchSavedViews = savedViews;
        const selected = savedViews.find((item) => item.id === searchViewId);
        if (selected) {
          searchQuery = typeof selected.filters?.q === 'string' ? selected.filters.q : '';
          searchPriority = typeof selected.filters?.priority === 'string' ? selected.filters.priority : 'all';
          searchState = typeof selected.filters?.state === 'string' ? selected.filters.state : 'all';
          const firstSort = selected.sort?.[0];
          searchSortField = firstSort?.field || 'updated_at';
          searchSortDirection = firstSort?.direction || 'desc';
          savedViewShared = selected.shared;
        }
      }
      return true;
    } catch (error) {
      if (requestId === searchRequest && sessionGeneration === requestedSession && user) {
        searchError = friendlyError(error, 'Search could not be loaded.');
      }
      return false;
    } finally {
      if (requestId === searchRequest && sessionGeneration === requestedSession) searchLoading = false;
    }
  }

  async function saveCurrentSearchView() {
    const name = savedViewName.trim();
    if (!name) {
      searchError = 'Name the saved view before saving it.';
      return;
    }
    savedViewSaving = true;
    searchError = '';
    const filters: Record<string, unknown> = {};
    if (searchQuery.trim()) filters.q = searchQuery.trim();
    if (searchPriority !== 'all') filters.priority = searchPriority;
    if (searchState !== 'all') filters.state = searchState;
    try {
      const created = await api.createSavedView({
        name,
        filters,
        sort: [{ field: searchSortField, direction: searchSortDirection }],
        shared: savedViewShared
      });
      searchSavedViews = [created, ...searchSavedViews.filter((item) => item.id !== created.id)];
      savedViewName = '';
      searchViewId = created.id;
      navigate(`/search?view=${encodeURIComponent(created.id)}`);
      await loadSearch();
      toast('success', `${created.name} saved.`);
    } catch (error) {
      searchError = friendlyError(error, 'The saved view could not be created.');
    } finally {
      savedViewSaving = false;
    }
  }

  async function updateCurrentSearchView() {
    const current = searchSavedViews.find((item) => item.id === searchViewId);
    if (!current) return saveCurrentSearchView();
    savedViewSaving = true;
    searchError = '';
    try {
      const updated = await api.patchSavedView(current.id, {
        filters: {
          ...(searchQuery.trim() ? { q: searchQuery.trim() } : {}),
          ...(searchPriority !== 'all' ? { priority: searchPriority } : {}),
          ...(searchState !== 'all' ? { state: searchState } : {})
        },
        sort: [{ field: searchSortField, direction: searchSortDirection }],
        shared: savedViewShared
      });
      searchSavedViews = searchSavedViews.map((item) => item.id === updated.id ? updated : item);
      toast('success', `${updated.name} updated.`);
      await loadSearch();
    } catch (error) {
      searchError = friendlyError(error, 'The saved view could not be updated.');
    } finally {
      savedViewSaving = false;
    }
  }

  async function removeCurrentSearchView(savedView: SavedView) {
    const confirmed = await requestConfirm({
      title: `Delete ${savedView.name}?`,
      message: 'This removes the saved search for you. Shared views will also disappear for everyone who can use them.',
      confirmLabel: 'Delete saved view',
      fallbackSelector: '[aria-label="Saved view name"]'
    });
    if (!confirmed) return;
    savedViewSaving = true;
    try {
      await api.deleteSavedView(savedView.id);
      searchSavedViews = searchSavedViews.filter((item) => item.id !== savedView.id);
      if (searchViewId === savedView.id) {
        searchViewId = '';
        navigate('/search');
        await loadSearch();
      }
      toast('success', `${savedView.name} deleted.`);
    } catch (error) {
      searchError = friendlyError(error, 'The saved view could not be deleted.');
    } finally {
      savedViewSaving = false;
      restoreDialogFocus();
    }
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

  function setAdminProjectDraft(project: Project) {
    adminProjectId = project.id;
    adminProjectName = project.name;
    adminProjectDescription = project.description || '';
    adminProjectColor = project.color || '#64748b';
    adminChecklistPolicy = project.checklist_completion_policy || 'warn';
  }

  async function loadAdminColumns(projectId = adminProjectId) {
    if (!projectId || !user?.admin) return;
    adminColumnsLoading = true;
    try {
      adminColumns = (await api.listAllColumns(projectId, { includeArchived: true })).data;
    } catch (error) {
      adminError = friendlyError(error, 'Project columns could not be loaded.');
    } finally {
      adminColumnsLoading = false;
    }
  }

  async function loadProjectAdmin() {
    if (!user?.admin) return;
    adminLoading = true;
    adminError = '';
    try {
      const result = await api.listAllProjects({ includeArchived: true });
      adminProjects = result.data;
      const selected = adminProjects.find((project) => project.id === adminProjectId)
        || adminProjects.find((project) => project.id === activeProject?.id)
        || adminProjects[0];
      if (!selected) {
        adminProjectId = '';
        adminColumns = [];
        return;
      }
      setAdminProjectDraft(selected);
      await loadAdminColumns(selected.id);
    } catch (error) {
      adminError = friendlyError(error, 'Project administration could not be loaded.');
    } finally {
      adminLoading = false;
    }
  }

  function replaceAdminProject(updated: Project) {
    adminProjects = adminProjects.map((project) => project.id === updated.id ? updated : project);
    if (updated.archived_at) {
      projects = projects.filter((project) => project.id !== updated.id);
      if (activeProjectSlug === updated.slug) {
        activeProjectSlug = '';
        columns = [];
        tasks = [];
        labels = [];
      }
    } else if (!projects.some((project) => project.id === updated.id)) {
      projects = [...projects, updated];
    } else {
      projects = projects.map((project) => project.id === updated.id ? updated : project);
    }
    setAdminProjectDraft(updated);
  }

  async function saveAdminProject() {
    const current = adminProjects.find((project) => project.id === adminProjectId);
    if (!current || !adminProjectName.trim()) return;
    adminSaving = true;
    adminError = '';
    try {
      const updated = await api.patchProject(current.id, {
        name: adminProjectName.trim(),
        description: adminProjectDescription.trim(),
        color: adminProjectColor,
        checklist_completion_policy: adminChecklistPolicy
      }, current.version);
      replaceAdminProject(updated);
      toast('success', `${updated.name} settings saved.`);
    } catch (error) {
      adminError = friendlyError(error, 'The project settings could not be saved. Refresh and try again.');
      await loadProjectAdmin();
      throw error;
    } finally {
      adminSaving = false;
    }
  }

  function confirmAdminProjectSave() {
    if (!adminProject || !adminProjectName.trim()) return;
    showAdminConfirmation({
      title: `Save ${adminProjectName.trim()} settings?`,
      message: 'This updates the project name, description, accent color, and checklist completion policy. The stable project key and URL stay unchanged unless you edit them through the API.',
      confirmLabel: 'Save project',
      action: saveAdminProject
    });
  }

  async function mutateAdminProjectArchive(project: Project) {
    const updated = await api.patchProject(project.id, { archived: !project.archived_at }, project.version);
    replaceAdminProject(updated);
    await loadAdminColumns(updated.id);
    toast('success', `${updated.name} was ${updated.archived_at ? 'archived' : 'restored'}.`);
  }

  function confirmAdminProjectArchive(project: Project) {
    const archived = Boolean(project.archived_at);
    showAdminConfirmation({
      title: archived ? `Restore ${project.name}?` : `Archive ${project.name}?`,
      message: archived
        ? 'The project and its stable URL will be available in the workspace again.'
        : `${project.task_count ?? 0} task${project.task_count === 1 ? '' : 's'} will stay intact, but the project will leave the active workspace until restored.`,
      confirmLabel: archived ? 'Restore project' : 'Archive project',
      action: async () => {
        try {
          await mutateAdminProjectArchive(project);
        } catch (error) {
          await loadProjectAdmin();
          throw error;
        }
      }
    });
  }

  function replaceAdminColumn(updated: Column) {
    adminColumns = adminColumns.map((column) => column.id === updated.id ? updated : column);
    if (updated.project_id === activeProject?.id) {
      columns = columns.map((column) => column.id === updated.id ? updated : column).filter((column) => !column.archived_at);
    }
  }

  async function mutateAdminColumn(column: Column) {
    adminColumnSaving = column.id;
    try {
      const updated = await api.patchColumn(column.id, {
        name: column.name.trim(),
        semantic_state: column.semantic_state
      }, column.version);
      replaceAdminColumn(updated);
      toast('success', `${updated.name} was saved.`);
    } finally {
      if (adminColumnSaving === column.id) adminColumnSaving = '';
    }
  }

  function confirmAdminColumnSave(column: Column) {
    showAdminConfirmation({
      title: `Save ${column.name || 'column'}?`,
      message: 'Changing a semantic mapping may update task completion state and dependency readiness. The operation is transactional and can be retried if another admin edits it first.',
      confirmLabel: 'Save column',
      action: async () => {
        try {
          await mutateAdminColumn(column);
        } catch (error) {
          await loadAdminColumns();
          throw error;
        }
      }
    });
  }

  async function mutateAdminColumnArchive(column: Column) {
    adminColumnSaving = column.id;
    try {
      const updated = await api.patchColumn(column.id, { archived: !column.archived_at }, column.version);
      replaceAdminColumn(updated);
      await loadAdminColumns();
      toast('success', `${updated.name} was ${updated.archived_at ? 'archived' : 'restored'}.`);
    } finally {
      if (adminColumnSaving === column.id) adminColumnSaving = '';
    }
  }

  function confirmAdminColumnArchive(column: Column) {
    const archived = Boolean(column.archived_at);
    showAdminConfirmation({
      title: archived ? `Restore ${column.name}?` : `Archive ${column.name}?`,
      message: archived
        ? 'Tasks can use this column again after it is restored.'
        : 'Tasks in this column will move to another live column with the same semantic state. The five required mappings remain available.',
      confirmLabel: archived ? 'Restore column' : 'Archive column',
      action: async () => {
        try {
          await mutateAdminColumnArchive(column);
        } catch (error) {
          await loadAdminColumns();
          throw error;
        }
      }
    });
  }

  async function mutateAdminColumnMove(column: Column, position: number) {
    adminColumnSaving = column.id;
    try {
      const updated = await api.patchColumn(column.id, { position }, column.version);
      replaceAdminColumn(updated);
      await loadAdminColumns();
      toast('success', `${updated.name} moved.`);
    } finally {
      if (adminColumnSaving === column.id) adminColumnSaving = '';
    }
  }

  function confirmAdminColumnMove(column: Column, position: number) {
    showAdminConfirmation({
      title: `Move ${column.name}?`,
      message: 'Reordering changes the board for everyone. Existing tasks remain in their current column and keep their order.',
      confirmLabel: 'Move column',
      action: async () => {
        try {
          await mutateAdminColumnMove(column, position);
        } catch (error) {
          await loadAdminColumns();
          throw error;
        }
      }
    });
  }

  async function mutateAdminColumnCreate() {
    const name = adminColumnName.trim();
    if (!name) return;
    adminColumnCreating = true;
    try {
      const created = await api.createColumn(adminProjectId, { name, semantic_state: adminColumnState });
      const firstArchived = adminColumns.findIndex((column) => Boolean(column.archived_at));
      const insertAt = firstArchived === -1 ? adminColumns.length : firstArchived;
      adminColumns = [...adminColumns.slice(0, insertAt), created, ...adminColumns.slice(insertAt)];
      adminColumnName = '';
      adminColumnState = 'backlog';
      toast('success', `${created.name} was added.`);
    } finally {
      adminColumnCreating = false;
    }
  }

  function confirmAdminColumnCreate() {
    if (!adminColumnName.trim() || !adminProjectId) return;
    showAdminConfirmation({
      title: `Add ${adminColumnName.trim()}?`,
      message: 'The new column will appear at the end of the board and use the selected semantic mapping.',
      confirmLabel: 'Add column',
      action: mutateAdminColumnCreate
    });
  }

  function showAdminConfirmation(confirmation: AdminConfirmation) {
    rememberDialogFocus('[aria-labelledby="project-admin-heading"] button');
    adminConfirmation = confirmation;
  }

  async function runAdminConfirmation() {
    const pending = adminConfirmation;
    if (!pending || adminConfirmationBusy) return;
    adminConfirmationBusy = true;
    adminError = '';
    try {
      await pending.action();
      adminConfirmation = null;
      restoreDialogFocus();
    } catch (error) {
      adminError = friendlyError(error, 'The administration change could not be applied. Refresh and try again.');
    } finally {
      adminConfirmationBusy = false;
    }
  }

  function cancelAdminConfirmation() {
    adminConfirmation = null;
    restoreDialogFocus();
    void loadAdminColumns();
  }

  async function loadCodexAccount(refresh = false) {
    codexStatusLoading = true;
    codexError = '';
    try {
      codexAccount = await api.codexAccount(refresh);
      if (codexAccount.connected) codexLogin = null;
    } catch (error) {
      codexError = friendlyError(error, 'Codex connection status could not be loaded.');
    } finally {
      codexStatusLoading = false;
    }
  }

  async function connectCodex() {
    codexLoading = true;
    codexError = '';
    try {
      codexLogin = await api.startCodexLogin();
      const loginId = codexLogin.login_id;
      void pollCodexLogin(loginId);
    } catch (error) {
      codexError = friendlyError(error, 'Codex device login could not be started. Your ChatGPT workspace may have disabled device-code login.');
      if (showTaskModal && taskModalNeedsCodex) taskModalError = codexError;
    } finally {
      codexLoading = false;
    }
  }

  async function pollCodexLogin(loginId: string) {
    for (let attempt = 0; attempt < 150 && codexLogin?.login_id === loginId && user; attempt += 1) {
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
      if (codexLogin?.login_id !== loginId || !user) return;
      try {
        const account = await api.codexAccount(true);
        codexAccount = account;
        if (account.connected) {
          codexLogin = null;
          toast('success', 'Codex connected to your ChatGPT subscription.');
          if (showTaskModal && taskModalNeedsCodex) void assistTaskWithLuna();
          return;
        }
      } catch {
        // Keep the device-code panel usable through transient network errors.
      }
    }
    if (codexLogin?.login_id === loginId) codexError = 'The Codex login is still pending. Cancel it and start a new code if this one expired.';
  }

  async function cancelCodexLogin() {
    const loginId = codexLogin?.login_id;
    if (!loginId) return;
    codexLoading = true;
    codexError = '';
    try {
      await api.cancelCodexLogin(loginId);
      codexLogin = null;
    } catch (error) {
      codexError = friendlyError(error, 'The pending Codex login could not be canceled.');
      if (showTaskModal) taskModalError = codexError;
    } finally {
      codexLoading = false;
    }
  }

  async function disconnectCodex() {
    codexLoading = true;
    codexError = '';
    try {
      if (codexLogin) await api.cancelCodexLogin(codexLogin.login_id).catch(() => undefined);
      await api.logoutCodex();
      codexLogin = null;
      codexAccount = { connected: false, requires_openai_auth: true };
      toast('success', 'Codex disconnected from Helm.');
    } catch (error) {
      codexError = friendlyError(error, 'Codex could not be disconnected.');
    } finally {
      codexLoading = false;
    }
  }

  async function copyCodexCode() {
    if (!codexLogin) return;
    try {
      await navigator.clipboard.writeText(codexLogin.user_code);
      toast('success', 'Device code copied.');
    } catch {
      toast('error', 'Device code could not be copied. Select it manually.');
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
    const refresh = loadBoard();
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
          syncCleanDrawerDrafts(drawerTask);
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

  async function reconcileDrawerComments(taskId: string, events: ActivityEvent[]): Promise<TimelineCommentReconciliationResult> {
    const loadedCommentTasks = new Map(
      drawerTimelineItems
        .filter((item) => item.kind === 'comment' && item.comment?.id)
        .map((item) => [item.comment?.id as string, taskId])
    );
    return reconcileTimelineComments(
      events,
      loadedCommentTasks,
      (commentTaskID, commentID) => api.getComment(commentTaskID, commentID),
      (error) => error instanceof ApiError && error.status === 404,
      taskId
    );
  }

  async function reconcileBoardComments(projectId: string, events: ActivityEvent[]): Promise<TimelineCommentReconciliationResult> {
    if (boardTimelineProjectId && boardTimelineProjectId !== projectId) return { ok: true, reconciliation: {} };
    const loadedCommentTasks = new Map(
      boardTimelineItems
        .filter((item) => item.kind === 'comment' && item.comment?.id && item.task_id)
        .map((item) => [item.comment?.id as string, item.task_id])
    );
    return reconcileTimelineComments(
      events,
      loadedCommentTasks,
      (commentTaskID, commentID) => api.getComment(commentTaskID, commentID),
      (error) => error instanceof ApiError && error.status === 404
    );
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
          let boardTimelineReconciliation: TimelineCommentReconciliation | undefined;
          let boardTimelineReconciliationSucceeded = true;
          if (result.data.length) {
            const reconciliation = await reconcileBoardComments(activeProject.id, result.data);
            if (!isCurrentPoll()) return;
            boardTimelineReconciliation = reconciliation.reconciliation;
            boardTimelineReconciliationSucceeded = reconciliation.ok;
          }
          if (
            !(await loadBoardTimeline(activeProject.id, { reconciliation: boardTimelineReconciliation }))
            || !boardTimelineReconciliationSucceeded
            || !isCurrentPoll()
          ) return;
        }
        if (!result.data.length) return;
        const [issueMetricsReady, sidebarCountsReady] = await Promise.all([
          loadIssueMetrics(),
          loadSidebarCounts()
        ]);
        if (!isCurrentPoll()) return;
        const mergedEvents = new Map<string, ActivityEvent>();
        [...events, ...result.data].forEach((event) => mergedEvents.set(event.id || String(event.cursor), event));
        events = [...mergedEvents.values()].sort((a, b) => b.cursor - a.cursor).slice(0, 100);
        const nextEventsCursor = Math.max(...result.data.map((event) => event.cursor));
        const currentProjectId = activeProject?.id;
        const currentView = view;
        const boardChanged = result.data.some((event) => !event.project_id || event.project_id === currentProjectId);
        const boardRefreshRequired = boardEventPageRequiresRefresh(boardChanged, result.next_cursor);
        const boardTaskIdsBeforePoll = new Set(tasks.map((task) => task.id));
        const affectedTaskIds = new Set(result.data.map((event) => event.task_id).filter((id): id is string => Boolean(id)));
        const dependencyAffectedTaskIds = new Set(result.data.flatMap(dependencyEventTaskIds));
        dependencyAffectedTaskIds.forEach((taskId) => affectedTaskIds.add(taskId));
        const hierarchyAffectedTaskIds = new Set(result.data.flatMap(hierarchyEventTaskIds));
        hierarchyAffectedTaskIds.forEach((taskId) => affectedTaskIds.add(taskId));
        let reloadSucceeded = issueMetricsReady && sidebarCountsReady;
        let drawerReconciliation: TimelineCommentReconciliation | undefined;

        if (boardRefreshRequired && (currentView === 'board' || currentView === 'timeline')) {
          reloadSucceeded = (await loadBoard()) && reloadSucceeded;
          const missingAffectedTask = [...affectedTaskIds].some((taskId) => boardTaskIdsBeforePoll.has(taskId) && !tasks.some((task) => task.id === taskId));
          if (missingAffectedTask) {
            boardReconciliationNotice = 'A changed task is outside the loaded board page or current filters. Refresh or load more to find it.';
            announce(boardReconciliationNotice);
          }
        }
        if (!isCurrentPoll()) return;
        if (boardChanged && currentView === 'issues') reloadSucceeded = (await loadIssues()) && reloadSucceeded;
        if (!isCurrentPoll()) return;
        if (currentView === 'my-work') reloadSucceeded = (await loadMyWork()) && reloadSucceeded;
        if (!isCurrentPoll()) return;
        if (currentView === 'roadmap') reloadSucceeded = (await loadRoadmap()) && reloadSucceeded;
        if (!isCurrentPoll()) return;

        if (drawerTask && affectedTaskIds.has(drawerTask.id)) {
          const drawerTaskId = drawerTask.id;
          const dependencyChanged = dependencyAffectedTaskIds.has(drawerTaskId);
          const hierarchyChanged = hierarchyAffectedTaskIds.has(drawerTaskId);
          reloadSucceeded = (await refreshDrawerTask(drawerTaskId)) && reloadSucceeded;
          if (dependencyChanged && drawerTask?.id === drawerTaskId) {
            if (drawerView === 'details' && drawerDependencyPanel) {
              reloadSucceeded = (await drawerDependencyPanel.refreshRelationships()) && reloadSucceeded;
            } else {
              drawerDependencyRefresh += 1;
            }
          }
          if (hierarchyChanged && drawerTask?.id === drawerTaskId) {
            if (drawerView === 'details' && drawerHierarchyPanel) {
              reloadSucceeded = (await drawerHierarchyPanel.refreshRelationships()) && reloadSucceeded;
            } else {
              drawerHierarchyRefresh += 1;
            }
          }
          if (drawerView === 'activity') {
            const reconciliation = await reconcileDrawerComments(drawerTaskId, result.data);
            if (!isCurrentPoll()) return;
            drawerReconciliation = reconciliation.reconciliation;
            reloadSucceeded = reconciliation.ok && reloadSucceeded;
            reloadSucceeded = (await loadDrawerTimeline(drawerTaskId, { reconciliation: drawerReconciliation })) && reloadSucceeded;
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
    if (`${window.location.pathname}${window.location.search}` !== path) {
      window.history[replace ? 'replaceState' : 'pushState']({}, '', path);
    }
  }

  async function selectProject(project: Project, push = true) {
    projectSwitchVersion += 1;
    boardTimelineRequest += 1;
    if (drawerTask && !closeDrawer()) return;
    clearAnnouncement();
    boardMutationRequest += 1;
    taskActionLoading = '';
    activeProjectSlug = project.slug;
    auditIdFromRoute = '';
    roadmapProjectId = undefined;
    columns = [];
    tasks = [];
    labels = [];
    invalidateBoardColumnRequests(Object.keys(boardPages));
    boardPages = {};
    boardPartial = false;
    boardReconciliationNotice = '';
    boardError = '';
    boardTimelineItems = [];
    boardTimelineNextCursor = '';
    boardTimelineError = '';
    boardTimelineProjectId = '';
    recentProjectIds = rememberProject(project.id, localStorage);
    localStorage.setItem(helmStorageKeys.lastProject, project.slug);
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
    } else if (next === 'search') {
      if (push) navigate(searchViewId ? `/search?view=${encodeURIComponent(searchViewId)}` : '/search');
      await loadSearch();
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
      await Promise.all([loadAgents(), loadCodexAccount(), loadProjectAdmin()]);
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
    else if (/^\/search\/?$/.test(window.location.pathname)) {
      applySearchRouteFilters(new URL(window.location.href).searchParams);
      void setView('search', false);
    }
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

  function requestConfirm(input: Omit<ConfirmRequest, 'resolve'>): Promise<boolean> {
    rememberDialogFocus(input.fallbackSelector || '');
    return new Promise((resolve) => {
      confirmRequest = { ...input, resolve };
    });
  }

  function settleConfirm(confirmed: boolean) {
    const request = confirmRequest;
    confirmRequest = null;
    request?.resolve(confirmed);
    // Cancellation can return to the original trigger immediately. A
    // destructive confirmation must keep the focus record until its async
    // mutation removes the trigger, so the caller can resolve the fallback.
    if (!confirmed) restoreDialogFocus();
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
    commandIssuesError = '';
    if (!issueTasks.length) {
      void loadCommandIssues();
    }
    commandSearchTasks = [];
    commandSearchProjects = [];
    commandSearchViews = [];
    void searchCommand('');
    void tick().then(() => commandInput?.focus());
  }

  function openTaskModalFromCommand(returnFocus: DialogReturnFocus | null) {
    const opening = openTaskModal();
    if (returnFocus) dialogReturnFocus = returnFocus;
    return opening;
  }

  function openBugModalFromCommand(returnFocus: DialogReturnFocus | null) {
    const opening = openBugModal();
    if (returnFocus) dialogReturnFocus = returnFocus;
    return opening;
  }

  async function loadCommandIssues() {
    if (commandIssuesLoading) return;
    const requestId = ++commandIssuesRequest;
    commandIssuesLoading = true;
    commandIssuesError = '';
    try {
      const result = await listAllIssues({ limit: 200 });
      if (requestId !== commandIssuesRequest) return;
      issueTasks = result.data;
    } catch (error) {
      if (requestId === commandIssuesRequest) {
        commandIssuesError = friendlyError(error, 'Issue results could not be loaded.');
      }
    } finally {
      if (requestId === commandIssuesRequest) commandIssuesLoading = false;
    }
  }

  async function searchCommand(query: string) {
    const requestId = ++commandSearchRequest;
    commandSearchLoading = true;
    try {
      const result = await api.search({ q: query.trim() || undefined, limit: 50 });
      if (requestId !== commandSearchRequest || !commandOpen) return;
      commandSearchTasks = result.data || [];
      commandSearchProjects = result.projects || [];
      commandSearchViews = result.views || [];
    } catch {
      // The local project/view choices remain useful during a transient search
      // failure; command search retries on the next query change.
    } finally {
      if (requestId === commandSearchRequest) commandSearchLoading = false;
    }
  }

  function commandInputChanged() {
    void searchCommand(commandQuery);
  }
  function closeCommandPalette() {
    closeCommandPaletteWithFocus(true);
  }

  function closeCommandPaletteWithoutFocus() {
    closeCommandPaletteWithFocus(false);
  }

  function closeCommandPaletteWithFocus(restoreFocus: boolean) {
    if (!commandOpen) return;
    commandOpen = false;
    if (restoreFocus) restoreDialogFocus();
  }

  function handleProjectSwitcherPointerDown(event: PointerEvent) {
    if (!projectSwitcherOpen) return;
    const target = event.target;
    if (!(target instanceof Node)) return;
    if (projectSwitcherPopover?.contains(target) || projectPickerTrigger?.contains(target)) return;
    projectSwitcherOpen = false;
  }

  function closeTaskModal() {
    taskModalAssistController?.abort();
    taskModalAssistController = null;
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
    // A confirmation is the top-most modal. Do not let global shortcuts or a
    // second Escape handler act on the dialog's underlying drawer/view.
    if (confirmRequest) {
      if (event.key === 'Escape') {
        event.preventDefault();
        settleConfirm(false);
      }
      return;
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
      event.preventDefault();
      openCommandPalette();
    } else if (
      event.key === '/'
      && !event.metaKey
      && !event.ctrlKey
      && !event.altKey
      && !isEditableTarget(event.target)
      && !commandOpen
      && !projectSwitcherOpen
      && !showProjectModal
      && !showTaskModal
      && !showBugModal
      && !revealedToken
      && !drawerTask
    ) {
      const searchInput = view === 'issues' ? issueSearchInput : view === 'board' ? boardSearchInput : null;
      if (searchInput) {
        event.preventDefault();
        searchInput.focus();
      }
    } else if (event.key === 'Escape') {
      if (confirmRequest) settleConfirm(false);
      else if (commandOpen) closeCommandPalette();
      else if (projectSwitcherOpen) projectSwitcherOpen = false;
      else if (adminConfirmation) cancelAdminConfirmation();
      else if (showProjectModal) closeProjectModal();
      else if (showTaskModal) closeTaskModal();
      else if (showBugModal) closeBugModal();
      else if (revealedToken) closeTokenReveal();
      else if (drawerTask) closeDrawer();
    }
  }

  function commandKeydown(event: KeyboardEvent) {
    if (event.key === 'Tab') {
      const source = event.currentTarget instanceof HTMLElement ? event.currentTarget : null;
      const menu = source?.closest<HTMLElement>('.command-menu') || document.querySelector<HTMLElement>('.command-menu');
      const focusable = menu ? focusableElements(menu) : [];
      if (!focusable.length) return;
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const index = active ? focusable.indexOf(active) : -1;
      const nextIndex = event.shiftKey
        ? index <= 0 ? focusable.length - 1 : index - 1
        : index < 0 || index === focusable.length - 1 ? 0 : index + 1;
      // Result buttons remain tabindex=-1 for the aria-activedescendant
      // combobox pattern, so the command menu owns both Tab directions.
      event.preventDefault();
      event.stopPropagation();
      focusable[nextIndex].focus();
    } else if (event.key === 'ArrowDown') {
      event.preventDefault();
      commandIndex = nextCommandIndex(commandIndex, commandChoices.length, 1);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      commandIndex = nextCommandIndex(commandIndex, commandChoices.length, -1);
    } else if (event.key === 'Home') {
      event.preventDefault();
      commandIndex = commandChoices.length ? 0 : 0;
    } else if (event.key === 'End') {
      event.preventDefault();
      commandIndex = Math.max(0, commandChoices.length - 1);
    } else if (event.key === 'Enter') {
      event.preventDefault();
      const choice = commandChoices[commandIndex];
      if (choice) void selectCommand(choice);
    }
  }

  function commandOptionKeydown(event: KeyboardEvent, choice: CommandChoice) {
    if (['Tab', 'ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      commandKeydown(event);
      return;
    }
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      void selectCommand(choice);
    }
  }

  function commandChoiceIcon(choice: CommandChoice): string {
    if (choice.kind === 'project') return projectInitials(choice.project);
    if (choice.kind === 'saved-view') return '☆';
    if (choice.kind === 'issue') return '⚠';
    if (choice.kind === 'task') return '□';
    if (choice.kind === 'action') {
      return choice.action === 'new-task' ? '+' : choice.action === 'report-bug' ? '⚠' : '◐';
    }
    return choice.view === 'search' ? '⌕' : choice.view === 'issues' ? '⚠' : choice.view === 'my-work' ? '◌' : choice.view === 'roadmap' ? '◒' : '⚙';
  }

  async function selectCommand(choice: CommandChoice) {
    if (choice.kind === 'action') {
      const returnFocus = dialogReturnFocus;
      if (choice.action === 'toggle-theme') {
        closeCommandPalette();
        toggleTheme();
        return;
      }
      closeCommandPaletteWithoutFocus();
      await tick();
      if (choice.action === 'new-task') await openTaskModalFromCommand(returnFocus);
      else await openBugModalFromCommand(returnFocus);
    } else if (choice.kind === 'project') {
      await selectProject(choice.project);
    } else if (choice.kind === 'task') {
      const returnFocus = dialogReturnFocus;
      closeCommandPaletteWithoutFocus();
      await tick();
      await openWorkTask(choice.task, returnFocus);
    } else if (choice.kind === 'issue') {
      const returnFocus = dialogReturnFocus;
      closeCommandPaletteWithoutFocus();
      await tick();
      await setView('issues');
      await openWorkTask(issueTasks.find((task) => task.id === choice.task.id) || choice.task, returnFocus);
    } else if (choice.kind === 'saved-view') {
      closeCommandPaletteWithoutFocus();
      await openSavedView(choice.savedView);
    } else if (choice.kind === 'view') await setView(choice.view);
  }

  function applySearchRouteFilters(params: URLSearchParams) {
    searchViewId = params.get('view') || '';
    searchQuery = params.get('q') || '';
    searchPriority = params.get('priority') || 'all';
    searchState = params.get('state') || 'all';
    const sort = params.get('sort') || '';
    const [field, direction] = sort.split(':', 2);
    const validFields = ['updated_at', 'created_at', 'due_at', 'title', 'key', 'priority', 'state', 'position'];
    if (validFields.includes(field) && (direction === 'asc' || direction === 'desc')) {
      searchSortField = field;
      searchSortDirection = direction;
    } else {
      searchSortField = 'updated_at';
      searchSortDirection = 'desc';
    }
  }

  async function openSavedView(savedView: SavedView) {
    commandOpen = false;
    searchViewId = savedView.id;
    searchQuery = typeof savedView.filters?.q === 'string' ? savedView.filters.q : '';
    searchPriority = typeof savedView.filters?.priority === 'string' ? savedView.filters.priority : 'all';
    searchState = typeof savedView.filters?.state === 'string' ? savedView.filters.state : 'all';
    const firstSort = savedView.sort?.[0];
    searchSortField = firstSort?.field || 'updated_at';
    searchSortDirection = firstSort?.direction || 'desc';
    await setView('search');
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

  async function openTaskModal(parentTaskId: string | null = null) {
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
    taskModalParentId = parentTaskId;
    taskModalIdea = '';
    taskModalSuggestion = null;
    taskModalAssisting = false;
    taskModalAssistStage = '';
    taskModalNeedsCodex = false;
    taskModalError = '';
    resetTaskModalSuggestionState();
    rememberDialogFocus('[data-task-modal-trigger]');
    showTaskModal = true;
    projectSwitcherOpen = false;
    void loadCodexAccount();
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

	function changeTaskModalProject() {
		taskModalSuggestion = null;
		taskModalNeedsCodex = false;
		resetTaskModalSuggestionState();
		// A parent is project-local. If the user changes the destination project,
		// clear the inherited relationship instead of submitting a guaranteed
		// cross-project mutation.
		taskModalParentId = null;
		void loadTaskModalColumns(taskModalProjectId);
	}

  function resetTaskModalSuggestionState() {
    taskModalAppliedFields = { title: false, description: false, priority: false };
    taskModalApplyNotice = '';
    taskModalHasAppliedAll = false;
    taskModalSuggestionCollapsed = false;
  }

  function suggestionDescription(suggestion: TaskDraftSuggestion): string {
    const criteria = suggestion.acceptance_criteria.map((criterion) => `- [ ] ${criterion}`).join('\n');
    return [suggestion.description.trim(), `## Acceptance criteria\n${criteria}`].filter(Boolean).join('\n\n');
  }

  function lunaFieldLabel(field: TaskModalSuggestionField): string {
    return field === 'description' ? 'Description' : field === 'priority' ? 'Priority' : 'Title';
  }

  function lunaSuggestionFieldValue(field: TaskModalSuggestionField): string {
    if (!taskModalSuggestion) return '';
    if (field === 'description') return suggestionDescription(taskModalSuggestion);
    if (field === 'priority') return taskModalSuggestion.priority;
    return taskModalSuggestion.title;
  }

  function applyLunaField(field: TaskModalSuggestionField) {
    if (!taskModalSuggestion) return;
    const value = lunaSuggestionFieldValue(field);
    if (field === 'title') taskModalTitle = value;
    if (field === 'description') taskModalDescription = value;
    if (field === 'priority') taskModalPriority = value as Priority;
    taskModalAppliedFields = { ...taskModalAppliedFields, [field]: true };
    taskModalApplyNotice = `${lunaFieldLabel(field)} suggestion applied to the task form.`;
  }

  function applyAllLunaFields() {
    applyLunaField('title');
    applyLunaField('description');
    applyLunaField('priority');
    taskModalHasAppliedAll = true;
    taskModalSuggestionCollapsed = true;
    taskModalApplyNotice = 'Luna applied all suggested fields. Review the populated task details before creating.';
  }

  async function assistTaskWithLuna() {
    const query = taskModalIdea.trim() || taskModalTitle.trim();
    if (!taskModalProjectId || !query) {
      taskModalError = 'Choose a project and describe the task idea before asking Luna.';
      return;
    }
    taskModalAssistController?.abort();
    const controller = new AbortController();
    taskModalAssistController = controller;
    taskModalAssisting = true;
    taskModalAssistStage = 'Reviewing relevant project history…';
    taskModalError = '';
    taskModalNeedsCodex = false;
    resetTaskModalSuggestionState();
    try {
      const suggestion = await api.draftTask(taskModalProjectId, query, controller.signal);
      if (controller.signal.aborted || taskModalAssistController !== controller) return;
      taskModalAssistStage = 'Checking the draft and priority…';
      await tick();
      taskModalSuggestion = suggestion;
      resetTaskModalSuggestionState();
    } catch (error) {
      if (controller.signal.aborted) return;
      if (error instanceof ApiError && error.code === 'codex_not_connected') {
        codexAccount = { connected: false, requires_openai_auth: true };
        taskModalNeedsCodex = true;
      } else {
        taskModalError = friendlyError(error, 'Luna is unavailable right now. You can retry or keep creating the task manually.');
      }
    } finally {
      if (taskModalAssistController === controller) {
        taskModalAssistController = null;
        taskModalAssisting = false;
        taskModalAssistStage = '';
      }
    }
  }

  function cancelTaskAssist() {
    taskModalAssistController?.abort();
    taskModalAssistController = null;
    taskModalAssisting = false;
    taskModalAssistStage = '';
  }

  async function createGlobalTask() {
    if (!taskModalProjectId || !taskModalTitle.trim()) {
      taskModalError = 'Choose a project and add a title.';
      return;
    }
    taskModalCreating = true;
    taskModalError = '';
    try {
      const created = await api.createTask(taskModalProjectId, {
        title: taskModalTitle.trim(),
        description: taskModalDescription.trim(),
        priority: taskModalPriority,
        column_id: taskModalColumnId || undefined,
        due_at: dateToIso(taskModalDueDate),
        assignee: taskModalAssignee.trim() || null,
        parent_task_id: taskModalParentId
      });
      recordTaskMutation(created.id, 'upsert', ['board']);
      if (taskModalProjectId === activeProject?.id) {
        tasks = [...tasks, created];
        syncBoardTaskPages(created);
      }
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
      const created = await api.createTask(bugModalProjectId, {
        title: bugModalTitle.trim(),
        description: bugModalDescription.trim(),
        kind: 'bug',
        priority: bugModalPriority,
        column_id: bugModalColumnId || undefined,
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
      if (bugModalProjectId === activeProject?.id) {
        tasks = [...tasks, created];
        syncBoardTaskPages(created);
      }
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
    filters = { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all', dependency: 'all' };
    boardWorkFilter = 'all';
    scheduleBoardReload();
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
    dragOverColumnId = '';
    event.dataTransfer?.setData('text/plain', task.id);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move';
  }

  function dragOverColumn(event: DragEvent, columnId: string) {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'move';
    if (draggingTaskId) dragOverColumnId = columnId;
  }

  function dragLeaveColumn(event: DragEvent, columnId: string) {
    const target = event.currentTarget as HTMLElement;
    const related = event.relatedTarget as Node | null;
    if (!related || !target.contains(related)) {
      if (dragOverColumnId === columnId) dragOverColumnId = '';
    }
  }

  async function dropTask(event: DragEvent, destinationColumnId: string, boundaryTaskId = '') {
    event.preventDefault();
    event.stopPropagation();
    const taskId = event.dataTransfer?.getData('text/plain') || draggingTaskId;
    draggingTaskId = '';
    dragOverColumnId = '';
    const task = tasks.find((item) => item.id === taskId);
    if (!task || taskActionLoading) return;
    // A drop on the column body is an append-only column move. A drop on a
    // rendered card is a precise reorder and therefore requires the physical
    // position/ascending view; visual sorts cannot be translated safely to
    // physical anchors without loading and sorting the complete column.
    if (!boundaryTaskId) {
      await moveTask(task, destinationColumnId);
      return;
    }
    if (!physicalOrderingEnabled(currentBoardOrderingGate(destinationColumnId))) {
      toast('info', visualOrderingUnavailableMessage());
      return;
    }
    const destinationTasks = tasksByColumn[destinationColumnId] || [];
    let targetIndex = destinationTasks.length;
    if (boundaryTaskId) {
      const boundaryIndex = destinationTasks.findIndex((item) => item.id === boundaryTaskId);
      if (boundaryIndex >= 0) {
        const target = event.currentTarget as HTMLElement | null;
        const bounds = target?.getBoundingClientRect();
        targetIndex = boundaryIndex + (bounds && event.clientY >= bounds.top + bounds.height / 2 ? 1 : 0);
      }
    }
    await reorderTaskAt(task, destinationColumnId, targetIndex);
  }

  function endDrag() {
    draggingTaskId = '';
    dragOverColumnId = '';
  }

  function currentBoardOrderingGate(columnId: string): BoardOrderingGate {
    return makeBoardOrderingGate({
      criteriaTransition: boardCriteriaTransition,
      filterTimerPending: boardFilterTimer !== undefined,
      boardLoading,
      metadataError: Boolean(boardError),
      pageRefreshActive: Boolean(boardColumnRefreshes[columnId]),
      page: boardPages[columnId],
      filters,
      workFilter: boardWorkFilter,
      sort: boardSort,
      order: boardOrder
    });
  }

  function physicalOrderingEnabled(gate: BoardOrderingGate): boolean {
    return boardOrderingUsesPhysicalOrder(gate);
  }

  function boardColumnPageComplete(gate: BoardOrderingGate): boolean {
    return boardColumnHasKnownGlobalBounds(boardOrderingFiltersActive(gate), gate.page, {
      metadataLoading: gate.boardLoading || gate.metadataError,
      pageRefreshActive: gate.pageRefreshActive,
      criteriaTransition: gate.criteriaTransition || gate.filterTimerPending
    });
  }

  function globalOrderingUnavailableMessage(gate: BoardOrderingGate): string {
    if (boardOrderingFiltersActive(gate)) {
      return 'Home and End cannot move to a global first or last position while board filters are active. Clear filters; previous and next remain available for visible cards.';
    }
    if (gate.criteriaTransition || gate.filterTimerPending) {
      return 'Home and End cannot move to a global first or last position while the board criteria are changing. Wait for the refreshed column; previous and next remain available for visible cards.';
    }
    if (gate.metadataError) {
      return 'Home and End cannot move to a global first or last position because the board metadata refresh failed. Retry the board; previous and next remain available for loaded cards.';
    }
    if (gate.boardLoading || gate.pageRefreshActive || gate.page?.loading) {
      return 'Home and End cannot move to a global first or last position while this board column is refreshing. Wait for the refreshed page; previous and next remain available for visible cards.';
    }
    if (gate.page?.error) {
      return 'Home and End cannot move to a global first or last position because this column did not finish loading. Retry the column; previous and next remain available for loaded cards.';
    }
    if (gate.page?.nextCursor) {
      return 'Home and End cannot move to a global first or last position until this column is fully loaded. Load more tasks; previous and next remain available for loaded cards.';
    }
    return 'Home and End cannot move to a global first or last position until this column is fully loaded.';
  }

  function announceGlobalOrderingUnavailable(gate: BoardOrderingGate) {
    const message = globalOrderingUnavailableMessage(gate);
    announce(message);
    toast('info', message);
  }

  function visualOrderingUnavailableMessage(): string {
    return 'Precise ordering is unavailable unless Sort tasks is Board order and Sort direction is Ascending.';
  }

  function orderingMoveUnavailableReason(
    placement: 'first' | 'previous' | 'next' | 'last',
    gate: BoardOrderingGate
  ): string {
    if (!physicalOrderingEnabled(gate)) return visualOrderingUnavailableMessage();
    const refreshReason = boardOrderingRefreshReason(gate);
    if (refreshReason) return refreshReason;
    if ((placement === 'first' || placement === 'last') && !boardColumnPageComplete(gate)) {
      return globalOrderingUnavailableMessage(gate);
    }
    return '';
  }

  function orderingMoveDisabled(
    task: Task,
    placement: 'first' | 'previous' | 'next' | 'last',
    ordered: Task[],
    gate: BoardOrderingGate,
    taskBusy: boolean
  ): boolean {
    if (!physicalOrderingEnabled(gate) || taskBusy) return true;
    const index = ordered.findIndex((item) => item.id === task.id);
    if (index < 0) return true;
    if (orderingMoveUnavailableReason(placement, gate)) return true;
    return placement === 'first' || placement === 'previous'
      ? index === 0
      : index === ordered.length - 1;
  }

  function orderingMoveTitle(
    placement: 'first' | 'previous' | 'next' | 'last',
    gate: BoardOrderingGate
  ): string {
    return orderingMoveUnavailableReason(placement, gate) || `Move to ${placement} position`;
  }

  async function moveTask(task: Task, destinationColumnId: string) {
    if (taskActionLoading) return;
    if (task.column_id === destinationColumnId) return;
    const destination = columns.find((column) => column.id === destinationColumnId);
    const blockedReason = dependencyMoveExplanation(task, destination);
    if (blockedReason) {
      toast('info', blockedReason);
      return;
    }
    const mutationContext = beginBoardMutation();
    if (!mutationContext) return;
    taskActionLoading = task.id;
    try {
      // Column moves remain append-only and are safe with cursor pagination:
      // the guarded endpoint computes the destination tail under its writer
      // lock, rather than treating the loaded page as the whole column. The
      // legacy move route owns backlog/ready moves; precise last-placement
      // handles active/blocked columns, while completed transitions retain
      // the existing checklist/lifecycle patch path.
      const updated = destination && ['backlog', 'ready'].includes(destination.semantic_state)
        ? await api.moveTask(task.id, {
            destination_column_id: destinationColumnId,
            expected_source_column_id: task.column_id,
            source: 'board'
          }, task.version)
        : destination?.semantic_state === 'completed'
          ? await api.patchTask(task.id, { column_id: destinationColumnId }, task.version)
          : await api.reorderTask(task.id, {
              destination_column_id: destinationColumnId,
              expected_source_column_id: task.column_id,
              source: 'board',
              reason: 'board column move',
              placement: 'last',
              ...(columns.find((column) => column.id === task.column_id)?.ordering_version ? { expected_source_ordering_version: columns.find((column) => column.id === task.column_id)?.ordering_version } : {}),
              ...(destination?.ordering_version ? { expected_destination_ordering_version: destination.ordering_version } : {})
            }, task.version);
      if (!boardMutationIsCurrent(mutationContext)) return;
      replaceTask(updated, true, false);
      mutationContext.mutationRevision = taskMutationRevision;
      const reloadSucceeded = await reloadBoardColumns([task.column_id, destinationColumnId]);
      if (!boardMutationReloadCanAnnounce(boardMutationIsCurrent(mutationContext), reloadSucceeded)) return;
      const destinationName = destination?.name || 'another column';
      toast('success', `${task.key} moved to ${destinationName}.`, {
        label: 'Undo',
        run: () => undoTaskMove(task, updated)
      });
      announce(`${task.key} moved to ${destinationName}.`);
    } catch (error) {
      if (!boardMutationIsCurrent(mutationContext)) return;
      if (error instanceof ApiError && error.status === 409) {
        if (error.details?.current) replaceTask(error.details.current as Task, false, false);
        mutationContext.mutationRevision = taskMutationRevision;
        const reloadSucceeded = await reloadBoardColumns([task.column_id, destinationColumnId]);
        if (!boardMutationReloadCanAnnounce(boardMutationIsCurrent(mutationContext), reloadSucceeded)) return;
        announce(`${task.key} changed elsewhere; the board was refreshed.`);
        toast('info', `${task.key} changed elsewhere. The board was refreshed; review the new order.`);
      } else {
        toast('error', friendlyError(error, 'The task could not be moved. Refresh and try again.'));
      }
    } finally {
      if (mutationContext.requestId === boardMutationRequest && taskActionLoading === task.id) taskActionLoading = '';
    }
  }

  function normalizedOrderingIndex(task: Task, destinationColumnId: string, targetIndex: number): number {
    const destinationTasks = tasksByColumn[destinationColumnId] || [];
    const index = Math.max(0, Math.min(destinationTasks.length, Math.trunc(targetIndex)));
    const currentIndex = destinationTasks.findIndex((item) => item.id === task.id);
    return task.column_id === destinationColumnId && currentIndex >= 0 && currentIndex < index ? index - 1 : index;
  }

  async function reorderTaskAt(task: Task, destinationColumnId: string, targetIndex: number, spokenPlacement = '') {
    if (taskActionLoading) return;
    const destinationOrderingGate = currentBoardOrderingGate(destinationColumnId);
    const sourceOrderingGate = task.column_id === destinationColumnId
      ? destinationOrderingGate
      : currentBoardOrderingGate(task.column_id);
    if (!physicalOrderingEnabled(destinationOrderingGate)) {
      toast('info', visualOrderingUnavailableMessage());
      return;
    }
    const refreshReason = boardOrderingRefreshReason(destinationOrderingGate)
      || boardOrderingRefreshReason(sourceOrderingGate);
    if (refreshReason) {
      toast('info', refreshReason);
      return;
    }
    const destination = columns.find((column) => column.id === destinationColumnId);
    const blockedReason = dependencyMoveExplanation(task, destination);
    if (blockedReason) {
      toast('info', blockedReason);
      return;
    }
    const destinationTasks = tasksByColumn[destinationColumnId] || [];
    const currentIndex = destinationTasks.findIndex((item) => item.id === task.id);
    const index = normalizedOrderingIndex(task, destinationColumnId, targetIndex);
    if (task.column_id === destinationColumnId && currentIndex >= 0 && currentIndex === index) return;
    let anchors = taskOrderingAnchors(visibleTasks, destinationColumnId, index, task.id);
    const visibleDestination = visibleTasks.filter((item) => item.column_id === destinationColumnId && item.id !== task.id);
    const completeDestination = tasks.filter((item) => item.column_id === destinationColumnId && item.id !== task.id);
    const globalBoundsUnknown = !boardColumnPageComplete(destinationOrderingGate);
    // When a filter hides leading/trailing cards, anchor at the visible edge
    // instead of claiming the global first/last position. If every
    // destination card is hidden, wait for a visible anchor rather than
    // disturbing an unknown part of the column.
    if (globalBoundsUnknown && visibleDestination.length === 0) {
      toast('info', 'Load or reveal a destination card before choosing its exact position.');
      return;
    } else if ((completeDestination.length > visibleDestination.length || globalBoundsUnknown) && anchors.placement === 'first' && visibleDestination[0]) {
      anchors = { before_task_id: visibleDestination[0].id, placement: 'before' };
    } else if ((completeDestination.length > visibleDestination.length || globalBoundsUnknown) && anchors.placement === 'last' && visibleDestination.length) {
      anchors = { after_task_id: visibleDestination[visibleDestination.length - 1].id, placement: 'after' };
    } else if (completeDestination.length > visibleDestination.length && visibleDestination.length === 0) {
      anchors = { placement: 'last' };
    }
    const sourceColumn = columns.find((column) => column.id === task.column_id);
    const sourceOrderingVersion = sourceColumn?.ordering_version;
    const destinationOrderingVersion = destination?.ordering_version;
    const mutationContext = beginBoardMutation();
    if (!mutationContext) return;
    taskActionLoading = task.id;
    try {
      const updated = await api.reorderTask(task.id, {
        destination_column_id: destinationColumnId,
        expected_source_column_id: task.column_id,
        source: 'board',
        reason: 'precise board reorder',
        ...anchors,
        ...(sourceOrderingVersion ? { expected_source_ordering_version: sourceOrderingVersion } : {}),
        ...(destinationOrderingVersion ? { expected_destination_ordering_version: destinationOrderingVersion } : {})
      }, task.version);
      if (!boardMutationIsCurrent(mutationContext)) return;
      replaceTask(updated, true, false);
      mutationContext.mutationRevision = taskMutationRevision;
      // Rebalancing updates neighbors without changing their task versions;
      // refresh keeps the filtered board aligned with those authoritative keys.
      const reloadSucceeded = await reloadBoardColumns([task.column_id, destinationColumnId]);
      if (!boardMutationReloadCanAnnounce(boardMutationIsCurrent(mutationContext), reloadSucceeded)) return;
      toast('success', `${task.key} moved to ${columns.find((column) => column.id === destinationColumnId)?.name || 'another column'}.`);
      announce(`${task.key} moved to ${spokenPlacement || orderingPlacementLabel(anchors.placement)} position.`);
    } catch (error) {
      if (!boardMutationIsCurrent(mutationContext)) return;
      if (error instanceof ApiError && error.status === 409) {
        if (error.details?.current) replaceTask(error.details.current as Task, false, false);
        mutationContext.mutationRevision = taskMutationRevision;
        const reloadSucceeded = await reloadBoardColumns([task.column_id, destinationColumnId]);
        if (!boardMutationReloadCanAnnounce(boardMutationIsCurrent(mutationContext), reloadSucceeded)) return;
        announce(`${task.key} order changed elsewhere; the board was refreshed.`);
        toast('info', `${task.key} changed elsewhere. The board was refreshed; review the new order.`);
      } else {
        toast('error', friendlyError(error, 'The task could not be moved. Refresh and try again.'));
      }
    } finally {
      if (mutationContext.requestId === boardMutationRequest && taskActionLoading === task.id) taskActionLoading = '';
    }
  }

  async function undoTaskMove(previous: Task, moved: Task) {
    if (moved.project_id !== activeProject?.id || previous.project_id !== activeProject?.id) return;
    const mutationContext = beginBoardMutation();
    if (!mutationContext) return;
    try {
      const restored = await api.patchTask(
        moved.id,
        { column_id: previous.column_id, position: previous.position },
        moved.version
      );
      if (!boardMutationIsCurrent(mutationContext)) return;
      replaceTask(restored, true, false);
      mutationContext.mutationRevision = taskMutationRevision;
      const reloadSucceeded = await reloadBoardColumns([previous.column_id, restored.column_id]);
      if (!boardMutationReloadCanAnnounce(boardMutationIsCurrent(mutationContext), reloadSucceeded)) return;
      announce(`${restored.key} moved back to ${columns.find((column) => column.id === previous.column_id)?.name || 'its previous column'}.`);
    } catch (error) {
      if (!boardMutationIsCurrent(mutationContext)) return;
      toast('error', friendlyError(error, 'Undo could not be applied because the task changed elsewhere.'));
    }
  }

  function keyboardMove(event: KeyboardEvent, task: Task) {
    if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      event.preventDefault();
      const destination = adjacentTaskColumn(task, event.key === 'ArrowLeft' ? -1 : 1);
      if (destination) void moveTask(task, destination.id);
      return;
    }
    if (!event.altKey || !['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) return;
    const ordered = tasksByColumn[task.column_id] || [];
    const currentIndex = ordered.findIndex((item) => item.id === task.id);
    if (currentIndex < 0) return;
    const orderingGate = currentBoardOrderingGate(task.column_id);
    if (!physicalOrderingEnabled(orderingGate)) {
      event.preventDefault();
      const message = visualOrderingUnavailableMessage();
      announce(message);
      toast('info', message);
      return;
    }
    const refreshReason = boardOrderingRefreshReason(orderingGate);
    if (refreshReason) {
      event.preventDefault();
      announce(refreshReason);
      toast('info', refreshReason);
      return;
    }
    if ((event.key === 'Home' || event.key === 'End') && !boardColumnPageComplete(orderingGate)) {
      event.preventDefault();
      announceGlobalOrderingUnavailable(orderingGate);
      return;
    }
    const targetIndex = event.key === 'Home' ? 0 : event.key === 'End' ? ordered.length : event.key === 'ArrowUp' ? currentIndex - 1 : currentIndex + 2;
    if (targetIndex < 0 || targetIndex > ordered.length || (event.key === 'ArrowUp' && currentIndex === 0) || (event.key === 'ArrowDown' && currentIndex === ordered.length - 1)) return;
    event.preventDefault();
    const spokenPlacement = event.key === 'Home' ? 'first' : event.key === 'End' ? 'last' : event.key === 'ArrowUp' ? 'previous' : 'next';
    void reorderTaskAt(task, task.column_id, targetIndex, spokenPlacement);
  }

  function adjacentTaskColumn(task: Task, offset: -1 | 1): Column | undefined {
    const index = sortedColumns.findIndex((column) => column.id === task.column_id);
    return sortedColumns[index + offset];
  }

  function cardMoveReason(task: Task, offset: -1 | 1): string {
    return dependencyMoveExplanation(task, adjacentTaskColumn(task, offset));
  }

  function cardMoveLabel(task: Task, offset: -1 | 1): string {
    const direction = offset < 0 ? 'previous' : 'next';
    const reason = cardMoveReason(task, offset);
    return `Move ${task.key} to ${direction} column${reason ? `. Unavailable: ${reason}` : ''}`;
  }

  function moveTaskBy(task: Task, offset: -1 | 1): void {
    const destination = adjacentTaskColumn(task, offset);
    if (destination) void moveTask(task, destination.id);
  }

  function orderingPlacementLabel(placement: string): string {
    if (placement === 'first') return 'first';
    if (placement === 'last') return 'last';
    return 'the selected';
  }

  function orderingMoveLabel(
    task: Task,
    placement: 'first' | 'previous' | 'next' | 'last',
    gate: BoardOrderingGate
  ): string {
    const reason = orderingMoveUnavailableReason(placement, gate);
    return `Move ${task.key} to ${placement} position${reason ? `. Unavailable: ${reason}` : ''}`;
  }

  function moveTaskToPosition(task: Task, placement: 'first' | 'previous' | 'next' | 'last'): void {
    const ordered = tasksByColumn[task.column_id] || [];
    const index = ordered.findIndex((item) => item.id === task.id);
    if (index < 0) return;
    const targetIndex = placement === 'first' ? 0 : placement === 'last' ? ordered.length : placement === 'previous' ? index - 1 : index + 2;
    void reorderTaskAt(task, task.column_id, targetIndex, placement);
  }

  function replaceTask(updated: Task, localMutation = false, announceChanges = true) {
    const issueHasTask = issueTasks.some((task) => task.id === updated.id);
    const existing = [
      drawerTask?.id === updated.id ? drawerTask : undefined,
      tasks.find((task) => task.id === updated.id),
      issueTasks.find((task) => task.id === updated.id),
      myWorkTasks.find((task) => task.id === updated.id),
      searchTasks.find((task) => task.id === updated.id),
      commandSearchTasks.find((task) => task.id === updated.id)
    ].filter((task): task is Task => Boolean(task));
    const previous = existing.reduce<Task | undefined>(
      (current, task) => !current || task.version > current.version ? task : current,
      undefined
    );
    const nextTask = mergeAuthoritativeTask(previous, updated);
    const belongsToActiveBoard = activeProject?.id === nextTask.project_id;
    if (belongsToActiveBoard) {
      tasks = tasks.some((task) => task.id === updated.id)
        ? tasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task))
        : [nextTask, ...tasks];
    }
    if (issueTasks.some((task) => task.id === updated.id)) {
      issueTasks = issueTasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task));
    } else if (nextTask.kind === 'bug') {
      // A delete removes the task from the Issues cache. Restore must put a
      // bug back into the currently rendered Issues view without waiting for
      // the next 15-second event poll or a manual refresh.
      issueTasks = [nextTask, ...issueTasks];
    }
    if (drawerTask?.id === updated.id) {
      drawerTask = mergeAuthoritativeTask(drawerTask, nextTask);
    }
    if (searchTasks.some((task) => task.id === updated.id)) {
      searchTasks = searchTasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task));
    }
    if (commandSearchTasks.some((task) => task.id === updated.id)) {
      commandSearchTasks = commandSearchTasks.map((task) => (task.id === updated.id ? mergeAuthoritativeTask(task, nextTask) : task));
    }
    const myWorkIndex = myWorkTasks.findIndex((task) => task.id === updated.id);
    const belongsToUser = actorId(nextTask.assignee) === user?.id
      || (actorId(nextTask.claimed_by) === user?.id && claimIsActive(nextTask, pulseClock));
    const keepInMyWork = myWorkView === 'live'
      ? !nextTask.completed_at && Boolean(nextTask.agent_work)
      : belongsToUser;
    if (localMutation) {
      const mutations: Partial<Record<TaskMutationScope, TaskMutationKind>> = {};
      if (belongsToActiveBoard) mutations.board = 'upsert';
      if (issueHasTask || nextTask.kind === 'bug') mutations.issues = 'upsert';
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
    syncBoardTaskPages(nextTask, announceChanges);
    const previousTransition = previous ? workTransitionKey(previous) : '';
    const nextTransition = workTransitionKey(nextTask);
    if (announceChanges && previousTransition && nextTransition && previousTransition !== nextTransition) {
      const status = isWorkStale(nextTask) && workState(nextTask) !== 'waiting' ? 'stale' : workState(nextTask) || 'updated';
      announce(`${nextTask.key} is now ${status}${isActionNeeded(nextTask) ? ' · action needed' : ''}.`);
    }
  }

  function openQuickAdd(columnId: string, trigger: HTMLButtonElement) {
    quickAddReturnFocus = trigger;
    quickAddColumn = columnId;
    void tick().then(() => {
      if (quickAddColumn === columnId) quickAddInput?.focus();
    });
  }

  function cancelQuickAdd() {
    const columnId = quickAddColumn;
    const previousTrigger = quickAddReturnFocus;
    quickAddReturnFocus = null;
    quickAddColumn = '';
    void tick().then(() => {
      const fallbackTrigger = Array.from(document.querySelectorAll<HTMLButtonElement>('[data-quick-add-trigger]'))
        .find((trigger) => trigger.dataset.quickAddTrigger === columnId && isFocusableVisible(trigger));
      const target = previousTrigger && isFocusableVisible(previousTrigger) ? previousTrigger : fallbackTrigger;
      target?.focus();
    });
  }

  async function submitQuickAdd(columnId: string) {
    const title = (quickAddTitle[columnId] || '').trim();
    if (!title || !activeProject) return;
    taskActionLoading = `create-${columnId}`;
    try {
      const created = await api.createTask(activeProject.id, {
        title,
        column_id: columnId,
        priority: 'normal'
      });
      recordTaskMutation(created.id, 'upsert', ['board']);
      tasks = [...tasks, created];
      syncBoardTaskPages(created);
      quickAddTitle = { ...quickAddTitle, [columnId]: '' };
      quickAddColumn = '';
      quickAddReturnFocus = null;
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

  async function loadDrawerTimeline(
    taskId = drawerTask?.id,
    options: { older?: boolean; reconciliation?: TimelineCommentReconciliation } = {}
  ): Promise<boolean> {
    if (!taskId) return false;
    const older = Boolean(options.older);
    // A poll and an explicit "load older" action may overlap. Deduplicate the
    // same lane and report false so the event poller keeps its cursor and will
    // retry after the in-flight request settles.
    if (drawerTimelineLoading || drawerTimelineLoadingOlder) return false;
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
      drawerTimelineItems = mergeTimelineItems(
        older ? previousItems : hadItems ? previousItems : [],
        result.data || [],
        options.reconciliation
      );
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

  function refreshDrawerTimelineAfterMutation(taskId: string) {
    if (drawerTask?.id !== taskId) return;
    // A lifecycle mutation creates a timeline event immediately, while the
    // normal event poll can take up to 15 seconds. Invalidate any in-flight
    // read and fetch the newest page so Details → Activity is immediately
    // truthful after claim/block/bug-lifecycle actions.
    drawerTimelineRequest += 1;
    drawerTimelineLoading = false;
    drawerTimelineLoadingOlder = false;
    void loadDrawerTimeline(taskId);
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

  async function openTask(
    task: Task,
    intent: TaskRouteIntent = taskRouteIntent,
    options: OpenTaskOptions = {}
  ) {
    if (!options.skipDiscardGuard && !confirmDrawerTaskSwitch(task)) return;
    const requestId = ++taskDetailRequest;
    rememberDialogFocus('[data-task-trigger], .work-row');
    if (options.returnFocus) dialogReturnFocus = options.returnFocus;
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
    drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
    drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
    const openingDraft = drawerDraftFingerprint();
    drawerLoading = true;
    void loadDrawerTimeline(task.id);
    try {
      const detail = await api.getTask(task.id);
      if (requestId !== taskDetailRequest || drawerTask?.id !== task.id) return;
      replaceTask(detail);
      // A fast editor can type before the detail request settles. Hydrate
      // only while every draft field still matches the opening snapshot.
      if (drawerDraftFingerprint() === openingDraft) {
        syncDraft(detail);
        drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
        drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
      }
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
        replaceTask(updated);
        if (drawerTask?.id === taskId) syncCleanDrawerDrafts(drawerTask);
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

  function handleChecklistTaskUpdated(updated: Task) {
    // Checklist mutations are task-versioned just like dependencies. Merge
    // the authoritative task while preserving title/description drafts.
    replaceTask(updated, true);
  }

  async function refreshDependencyTask(): Promise<void> {
    if (drawerTask) await refreshDrawerTask(drawerTask.id);
  }

  function handleHierarchyTaskUpdated(updated: Task) {
    // Parent-edge mutations update the child version and the parent's derived
    // rollup. Merge the returned child without replacing unsaved drawer text.
    replaceTask(updated, true);
  }

  async function refreshHierarchyTask(): Promise<void> {
    if (drawerTask) await refreshDrawerTask(drawerTask.id);
  }

  async function openHierarchyTask(reference: TaskHierarchyReference): Promise<void> {
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
        throw new Error('Hierarchy tasks must belong to the same project.');
      }
      // Check the drawer's dirty state before changing the URL. Navigation is
      // itself observable, so a cancelled discard must leave both the drawer
      // and the current route untouched.
      if (!confirmDrawerTaskSwitch(related)) return;
      if (!taskRouteOrigin) taskRouteOrigin = `${window.location.pathname}${window.location.search}`;
      navigate(taskDeepLink(project.slug, related.key, 'details'));
      await openTask(related, 'details', { skipDiscardGuard: true });
    } catch (error) {
      if (drawerTask?.id === sourceTask.id) {
        drawerError = friendlyError(error, 'The hierarchy task could not be opened.');
      }
    }
  }

  async function createChildTask(): Promise<void> {
    if (!drawerTask) return;
    await openTaskModal(drawerTask.id);
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
      if (!confirmDrawerTaskSwitch(related)) return;
      if (!taskRouteOrigin) taskRouteOrigin = `${window.location.pathname}${window.location.search}`;
      navigate(taskDeepLink(project.slug, related.key, 'details'));
      await openTask(related, 'details', { skipDiscardGuard: true });
    } catch (error) {
      if (drawerTask?.id === sourceTask.id) {
        drawerError = friendlyError(error, 'The linked task could not be opened.');
      }
    }
  }

  function syncTaskDraft(task: Task) {
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
  }

  function syncActionDraft(task: Task) {
    triageSeverityDraft = task.bug?.severity || 's3';
    resolutionDraft = task.bug?.resolution || 'fixed';
    duplicateOfDraft = task.bug?.duplicate_of || '';
    resolutionNoteDraft = '';
    reopenReasonDraft = '';
  }

  function syncDraft(task: Task, resetActionDrafts = true) {
    syncTaskDraft(task);
    if (resetActionDrafts) {
      syncActionDraft(task);
    }
  }

  function syncCleanDrawerDrafts(authoritative: Task) {
    if (!drawerTask || drawerTask.id !== authoritative.id) return;
    if (drawerTaskDraftFingerprint() === drawerSavedTaskDraftFingerprint) {
      syncTaskDraft(authoritative);
      drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
    }
    if (drawerActionDraftFingerprint() === drawerSavedActionDraftFingerprint) {
      syncActionDraft(authoritative);
      drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
    }
  }

  function drawerTaskDraftFingerprint(): string {
    return JSON.stringify(drawerTaskDraftValues());
  }

  function drawerTaskDraftValues(): unknown[] {
    return [
      draftTitle,
      draftDescription,
      draftPriority,
      draftDueDate,
      draftAssignee,
      draftLabels,
      draftBugActual,
      draftBugExpected,
      draftBugReproduction,
      draftBugEnvironment,
      draftBugVersion
    ];
  }

  function drawerActionDraftFingerprint(): string {
    return JSON.stringify(drawerActionDraftValues());
  }

  function drawerActionDraftValues(): unknown[] {
    return [
      triageSeverityDraft,
      resolutionDraft,
      duplicateOfDraft,
      resolutionNoteDraft,
      reopenReasonDraft,
      blockReasonDraft,
      commentBody
    ];
  }

  function drawerDraftFingerprint(): string {
    return JSON.stringify([...drawerTaskDraftValues(), ...drawerActionDraftValues()]);
  }

  function restoreTaskRouteOrigin(origin: string) {
    const path = new URL(origin || '/', window.location.origin).pathname;
    if (/^\/my-work\/?$/.test(path)) {
      view = 'my-work';
      void loadMyWork();
    } else if (/^\/search\/?$/.test(path)) {
      applySearchRouteFilters(new URL(origin, window.location.origin).searchParams);
      view = 'search';
      void loadSearch();
    } else if (/^\/issues\/?$/.test(path)) {
      applyIssueRouteFilters(new URL(origin, window.location.origin).searchParams);
      view = 'issues';
      void loadIssues();
    } else if (/^\/roadmap\/?$/.test(path)) {
      view = 'roadmap';
      void loadRoadmap();
    } else if (/^\/settings\/?$/.test(path)) {
      view = 'settings';
    } else if (/^\/p\/[^/]+\/timeline\/?$/.test(path)) {
      // Board timeline task links use the same drawer as board, My Work, and
      // search. Restore the timeline view after closing a task instead of
      // falling through to the default board view.
      const timelineSlug = path.match(/^\/p\/([^/]+)\/timeline\/?$/)?.[1];
      const timelineProject = projects.find((project) => project.slug === decodeURIComponent(timelineSlug || ''));
      if (timelineProject) {
        activeProjectSlug = timelineProject.slug;
        roadmapProjectId = undefined;
        view = 'timeline';
        if (boardTimelineProjectId !== timelineProject.id) void loadBoardTimeline(timelineProject.id, { reset: true });
      } else {
        view = 'timeline';
      }
    } else {
      view = 'board';
    }
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

  function confirmDrawerDiscard(): boolean {
    if (!drawerDraftDirty) return true;
    return window.confirm('You have unsaved task details. Discard them and close the drawer?');
  }

  function confirmDrawerTaskSwitch(task: Task): boolean {
    if (!drawerTask || drawerTask.id === task.id) return true;
    return confirmDrawerDiscard();
  }

  function closeDrawer(force = false): boolean {
    if (!force && !confirmDrawerDiscard()) return false;
    const routeOrigin = taskRouteOrigin;
    taskDetailRequest += 1;
    drawerLivenessRequest += 1;
    drawerTask = null;
    drawerDependencyPanel = null;
    drawerHierarchyPanel = null;
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
    drawerSavedTaskDraftFingerprint = '';
    drawerSavedActionDraftFingerprint = '';
    if (isTaskLocation()) {
      const destination = routeOrigin || `/p/${encodeURIComponent(activeProjectSlug)}`;
      navigate(destination);
      restoreTaskRouteOrigin(destination);
    }
    restoreDialogFocus();
    return true;
  }

  async function deleteProjectLabel(label: Label) {
    const confirmed = await requestConfirm({
      title: `Delete label “${label.name}”?`,
      message: `The ${label.name} label will be removed from tasks in this project.`,
      confirmLabel: 'Delete label',
      fallbackSelector: '#drawer-title'
    });
    if (!confirmed) return;
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
      restoreDialogFocus();
      toast('success', `${label.name} deleted.`);
    } catch (error) {
      restoreDialogFocus();
      toast('error', friendlyError(error, 'The label could not be deleted.'));
    } finally {
      labelDeleting = '';
    }
  }

  async function saveTask(silent = false): Promise<boolean> {
    if (!drawerTask || !draftTitle.trim()) {
      drawerError = 'A task needs a title.';
      return false;
    }
    if (drawerTask.kind === 'bug' && !draftBugActual.trim()) {
      drawerError = 'A bug report needs actual behavior.';
      return false;
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
      // Keep action-only drafts (triage severity, resolution note, block
      // reason, and comments) intact. They are not part of the PATCH body and
      // must remain dirty until their own action commits them.
      syncDraft(updated, false);
      drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
      if (!silent) toast('success', `${updated.key} saved.`);
      return true;
    } catch (error) {
      drawerError = friendlyError(error, 'The task changed elsewhere. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) {
        const current = error.details.current as Task;
        replaceTask(current);
        drawerError = 'This task changed in another session. Your draft was not overwritten.';
      }
      return false;
    } finally {
      drawerSaving = false;
    }
  }

  async function deleteDrawerTask() {
    const task = drawerTask;
    if (!task) return;
    const confirmed = await requestConfirm({
      title: `Delete ${task.key}?`,
      message: `“${task.title}” will be removed from the board. You can undo this deletion from the next notification.`,
      confirmLabel: 'Delete task',
      fallbackSelector: '[data-destructive-focus-target]'
    });
    if (!confirmed) return;
    taskActionLoading = task.id;
    drawerError = '';
    try {
      await api.deleteTask(task.id, task.version);
      recordTaskMutation(task.id, 'remove', ['board', 'issues', 'my-work-live', 'my-work-assigned']);
      tasks = tasks.filter((item) => item.id !== task.id);
      removeTaskFromBoardPages(task.id);
      issueTasks = issueTasks.filter((item) => item.id !== task.id);
      myWorkTasks = myWorkTasks.filter((item) => item.id !== task.id);
      closeDrawer(true);
      toast('success', `${task.key} deleted.`, {
        label: 'Undo',
        run: () => undoTaskDelete(task)
      });
    } catch (error) {
      restoreDialogFocus();
      drawerError = friendlyError(error, 'The task could not be deleted. Refresh and try again.');
      if (error instanceof ApiError && error.details.current) {
        replaceTask(error.details.current as Task);
        drawerError = 'This task changed in another session. Refresh and try again.';
      }
    } finally {
      taskActionLoading = '';
    }
  }

  async function undoTaskDelete(deletedTask: Task) {
    try {
      // DELETE increments the task version atomically. The restore endpoint
      // accepts that deleted version, so a concurrent edit or second restore
      // fails closed with a conflict instead of resurrecting stale data.
      const restored = await api.restoreTask(deletedTask.id, deletedTask.version + 1);
      replaceTask(restored, true);
      announce(`${restored.key} restored to the board.`);
    } catch (error) {
      toast('error', friendlyError(error, 'Undo could not be applied because the task changed elsewhere.'));
    }
  }

  async function saveDraftBeforeImmediateAction(actionLabel: string): Promise<boolean> {
    // Triage/resolve/reopen submit their own action-only drafts. Only task
    // fields need a preceding PATCH; comments, block reasons, and lifecycle
    // options must remain available to the action that consumes them.
    if (drawerTaskDraftFingerprint() === drawerSavedTaskDraftFingerprint) return true;
    const saved = await saveTask(true);
    if (!saved) {
      drawerError = drawerError || `Save your changes before ${actionLabel}.`;
      return false;
    }
    return true;
  }

  async function runTaskAction(action: 'claim' | 'renew' | 'release' | 'complete' | 'block', reason = '') {
    if (!drawerTask) return;
    if (['claim', 'renew', 'complete'].includes(action) && dependencyBlocked(drawerTask)) {
      drawerError = dependencyActionExplanation(
        drawerTask,
        action === 'complete' ? 'complete this task' : action === 'renew' ? 'renew this claim' : 'claim this task'
      );
      return;
    }
    if (action === 'block' && !reason.trim()) {
      drawerError = 'Add a reason before blocking this task.';
      blockReasonOpen = true;
      return;
    }
    if (action === 'claim' && claimConflict(drawerTask, pulseClock)) {
      drawerError = `Claim conflict: ${claimOwnerLabel(drawerTask)} holds this task until ${claimExpiryExact(drawerTask.claim_expires_at)} (${claimCountdown(drawerTask, pulseClock)}). Current task version: v${drawerTask.version}.`;
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
      refreshDrawerTimelineAfterMutation(updated.id);
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
    if (dependencyBlocked(drawerTask)) {
      drawerError = dependencyActionExplanation(drawerTask, 'start triage');
      return;
    }
    if (!(await saveDraftBeforeImmediateAction('triaging this issue'))) return;
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
      drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
      drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
      refreshDrawerTimelineAfterMutation(updated.id);
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
    if (dependencyBlocked(drawerTask)) {
      drawerError = dependencyActionExplanation(drawerTask, 'resolve this issue');
      return;
    }
    if (resolutionDraft === 'duplicate' && !duplicateOfDraft.trim()) {
      drawerError = 'Add the task key or ID this issue duplicates.';
      return;
    }
    if (!(await saveDraftBeforeImmediateAction('resolving this issue'))) return;
    if (!drawerTask?.bug) return;
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
      drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
      drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
      refreshDrawerTimelineAfterMutation(updated.id);
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
    if (!(await saveDraftBeforeImmediateAction('reopening this issue'))) return;
    if (!drawerTask?.bug) return;
    taskActionLoading = drawerTask.id;
    drawerError = '';
    try {
      const updated = await api.reopenTask(drawerTask.id, drawerTask.version, { reason: reopenReasonDraft.trim() });
      replaceTask(updated, true);
      syncDraft(updated);
      drawerSavedTaskDraftFingerprint = drawerTaskDraftFingerprint();
      drawerSavedActionDraftFingerprint = drawerActionDraftFingerprint();
      refreshDrawerTimelineAfterMutation(updated.id);
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

  async function editDrawerComment(comment: Comment, body: string): Promise<void> {
    if (!drawerTask) return;
    const taskId = drawerTask.id;
    try {
      const updatedComment = await api.patchComment(taskId, comment.id, body.trim(), comment.version ?? 1);
      drawerTimelineRequest += 1;
      drawerTimelineLoading = false;
      drawerTimelineLoadingOlder = false;
      await loadDrawerTimeline(taskId, { reconciliation: { updatedComments: new Map([[comment.id, updatedComment]]) } });
      toast('success', 'Comment updated.');
    } catch (error) {
      const message = friendlyError(error, 'Your comment could not be updated.');
      drawerError = message;
      throw new Error(message);
    }
  }

  async function deleteDrawerComment(comment: Comment): Promise<void> {
    if (!drawerTask) return;
    const taskId = drawerTask.id;
    try {
      await api.deleteComment(taskId, comment.id, comment.version ?? 1);
      drawerTimelineRequest += 1;
      drawerTimelineLoading = false;
      drawerTimelineLoadingOlder = false;
      await loadDrawerTimeline(taskId, { reconciliation: { deletedCommentIds: [comment.id] } });
      toast('success', 'Comment deleted.');
    } catch (error) {
      const message = friendlyError(error, 'Your comment could not be deleted.');
      drawerError = message;
      throw new Error(message);
    } finally {
      restoreDialogFocus();
    }
  }

  function confirmDrawerCommentDelete(comment: Comment): Promise<boolean> {
    return requestConfirm({
      title: 'Delete this comment?',
      message: 'The comment body will be removed, while the deletion remains recorded in activity.',
      confirmLabel: 'Delete comment',
      fallbackSelector: '#drawer-activity-tab'
    });
  }

  function projectForTask(task: Task): Project | undefined {
    return projects.find((project) => project.id === task.project_id);
  }

  async function openWorkTask(task: Task, returnFocus: DialogReturnFocus | null = null) {
    if (!confirmDrawerTaskSwitch(task)) return;
    const project = projectForTask(task);
    const origin = window.location.pathname + window.location.search;
    taskRouteOrigin = isTaskLocation() ? (taskRouteOrigin || origin) : origin;
    if (project) {
      activeProjectSlug = project.slug;
      recentProjectIds = rememberProject(project.id, localStorage);
      view = 'board';
      navigate(taskDeepLink(project.slug, task.key, 'details'));
      await loadBoard();
    } else {
      // A task from a scoped/global search can outlive the project list cache.
      // Keep the drawer usable; the detail response still supplies its task
      // metadata and closeDrawer returns to the originating view.
      // There is no stable project route to push until the project metadata is
      // available, so leave the current URL untouched.
    }
    await openTask(task, taskRouteIntent, { skipDiscardGuard: true, returnFocus });
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
    localStorage.setItem(helmStorageKeys.lastProject, project.slug);
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

  function toast(kind: ToastKind, message: string, action?: ToastAction) {
    const id = ++toastSequence;
    toasts = [...toasts, { id, kind, message, action, ...toastAccessibility(kind) }];
    window.setTimeout(() => {
      toasts = toasts.filter((item) => item.id !== id);
    }, 4200);
  }

  async function runToastAction(item: ToastItem) {
    if (!item.action || item.action.pending) return;
    item.action.pending = true;
    toasts = [...toasts];
    try {
      await item.action.run();
    } finally {
      toasts = toasts.filter((toastItem) => toastItem.id !== item.id);
    }
  }

  async function exportActiveProject() {
    if (!activeProject || portableBusy) return;
    portableBusy = true;
    try {
      const archive = await api.exportProject(activeProject.id);
      const blob = new Blob([JSON.stringify(archive, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `helm-${activeProject.slug}-portable-v${archive.version}.json`;
      anchor.click();
      URL.revokeObjectURL(url);
      toast('success', `Exported ${activeProject.name}.`);
    } catch (error) {
      toast('error', friendlyError(error, 'The project could not be exported.'));
    } finally {
      portableBusy = false;
    }
  }

  async function handlePortableFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file || !activeProject || portableBusy) return;
    portableBusy = true;
    portablePreviewError = '';
    let archive: PortableArchive | null = null;
    try {
      archive = JSON.parse(await file.text()) as PortableArchive;
      const report = await api.importPortable(archive, { targetProject: activeProject.id, dryRun: true });
      portablePreview = { archive, fileName: file.name, report };
    } catch (error) {
      const report = portableImportReportFromError(error);
      if (archive && report) {
        // Validation failures still carry the complete structured report in
        // ApiError.details. Keep the review dialog open so users can inspect
        // every issue and remap before choosing another file or cancelling.
        portablePreview = { archive, fileName: file.name, report };
      } else {
        portablePreviewError = friendlyError(error, 'The portable archive could not be previewed.');
        toast('error', portablePreviewError);
      }
    } finally {
      portableBusy = false;
    }
  }

  function closePortablePreview() {
    portablePreview = null;
    portablePreviewError = '';
  }

  async function confirmPortableImport() {
    if (!portablePreview || !activeProject || portableBusy) return;
    portableBusy = true;
    try {
      const report = await api.importPortable(portablePreview.archive, { targetProject: activeProject.id });
      closePortablePreview();
      const created = report.counts.tasks_created || 0;
      toast('success', `Imported ${created} task${created === 1 ? '' : 's'} into ${activeProject.name}.`);
      await loadProjects();
      await loadBoard();
    } catch (error) {
      const report = portableImportReportFromError(error);
      if (report) {
        portablePreview = { ...portablePreview, report };
      } else {
        toast('error', friendlyError(error, 'The portable archive could not be imported.'));
      }
    } finally {
      portableBusy = false;
    }
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
    const confirmed = await requestConfirm({
      title: `Revoke token “${token.name}”?`,
      message: 'This token will stop working immediately and cannot be restored.',
      confirmLabel: 'Revoke token',
      fallbackSelector: '[data-destructive-focus-target]'
    });
    if (!confirmed) return;
    try {
      await api.deleteToken(token.id);
      agents = agents.map((agent) => ({ ...agent, tokens: agent.tokens?.filter((item) => item.id !== token.id) }));
      restoreDialogFocus();
      toast('success', `${token.name} was revoked.`);
    } catch (error) {
      restoreDialogFocus();
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
    <HelmMark size={46} decorative className="brand-mark brand-mark-large" />
    <div class="splash-copy">
      <strong>Helm</strong>
      <span>Getting your workspace ready…</span>
    </div>
    <span class="spinner" aria-label="Loading"></span>
  </div>
{:else if !user}
  <main class="auth-page">
    <section class="auth-panel" aria-labelledby="auth-heading">
      <div class="auth-intro">
        <HelmMark size={30} className="brand-mark" />
        <span class="eyebrow">Agent-first planning</span>
        <h1 id="auth-heading">Make progress visible.</h1>
        <p>Helm keeps projects focused, tasks accountable, and every handoff easy to follow.</p>
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
          <div class="inline-alert error" role="alert"><span>!</span><span>{authError}</span>{#if authBootstrapFailed}<button class="text-button" type="button" on:click={bootstrap}>Retry</button>{/if}</div>
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
          <HelmMark size={30} decorative className="brand-mark" />
          <span><strong>Helm</strong><small>Stay in motion</small></span>
        </button>
        <button class="button new-project-button" type="button" data-project-modal-trigger on:click={openProjectModal}><span aria-hidden="true">＋</span> New project</button>
      </div>

      <nav class="nav-links" aria-label="Workspace views">
        <button class:active={view === 'issues'} type="button" aria-label="Issues" on:click={() => setView('issues')}><span class="nav-icon">⚠</span><span>Issues</span>{#if sidebarCountsStatus === 'known' && sidebarCounts}<span class="nav-count">{sidebarCounts.issues}</span>{/if}</button>
        <button class:active={view === 'my-work'} type="button" aria-label="My work" on:click={() => setView('my-work')}><span class="nav-icon">◌</span><span>My work</span>{#if sidebarCountsStatus === 'known' && sidebarCounts}<span class="nav-count">{sidebarCounts.my_work}</span>{/if}</button>
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
        <div class="mobile-brand"><HelmMark size={30} decorative className="brand-mark" /><strong>Helm</strong></div>
        <div class="topbar-project">
          {#if activeProject}
            <button bind:this={projectPickerTrigger} class="project-picker" type="button" data-project-picker-trigger aria-label={`Switch project, current ${activeProject.name}`} aria-expanded={projectSwitcherOpen} on:click={() => { projectSwitcherOpen = !projectSwitcherOpen; closeCommandPalette(); }}>
              <span class="project-dot large" style={`--project-color: ${activeProject.color || '#6d5efc'}`}>{projectInitials(activeProject)}</span><span>{activeProject.name}</span><span class="picker-chevron">⌄</span>
            </button>
          {:else}<span class="muted">Workspace</span>{/if}
          {#if projectSwitcherOpen}
            <div bind:this={projectSwitcherPopover} class="popover project-popover" data-project-switcher-popover>
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
          <button class="command-trigger" type="button" aria-label="Search anything" data-command-trigger on:click={openCommandPalette}><span>⌕</span><span class="command-trigger-label">Search anything</span><kbd data-command-shortcut>{commandShortcut}</kbd></button>
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

      <main class="content" class:my-work-live={view === 'my-work' && myWorkView === 'live'} tabindex="-1" data-destructive-focus-target>
        {#if view === 'board' || view === 'timeline'}
          {#if projectsLoading && !projects.length}
            <section class="workspace-loading" role="status" aria-label="Loading workspace" aria-live="polite" aria-busy="true">
              <span class="sr-only">Loading projects and board…</span>
              <div class="workspace-loading-heading" aria-hidden="true">
                <span class="loading-line loading-line-short"></span>
                <span class="loading-line loading-line-title"></span>
                <span class="loading-line loading-line-copy"></span>
              </div>
              <div class="board board-loading" aria-hidden="true">{#each [1, 2, 3, 4] as item}<div class="column-skeleton"><div></div><div></div><div></div></div>{/each}</div>
            </section>
          {:else if activeProject}
            <section class="page-heading board-heading">
              <div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{activeProject.key}</span></div><div class="heading-title-row"><span class="heading-project-dot" style={`--project-color: ${activeProject.color || '#6d5efc'}`}></span><h1>{activeProject.name}</h1><button class="icon-button favorite-heading" class:starred={activeProject.favorite} type="button" aria-label={activeProject.favorite ? 'Remove from favorites' : 'Add to favorites'} on:click={(event) => toggleFavorite(event, activeProject)}>{activeProject.favorite ? '★' : '☆'}</button></div><p>{view === 'timeline' ? 'Everything recently worked on in this board, in one chronological view.' : activeProject.description || 'A focused space for turning ideas into shipped work.'}</p></div>
              <div class="heading-actions"><button class="button quiet-button" type="button" disabled={portableBusy} on:click={() => void exportActiveProject()}><span aria-hidden="true">⇩</span> Export</button><button class="button quiet-button" type="button" disabled={portableBusy} on:click={() => portableFileInput?.click()}><span aria-hidden="true">⇧</span> Import</button><button class="button quiet-button" type="button" on:click={openProjectRoadmap}><span aria-hidden="true">◒</span> Progress</button><button class="button quiet-button" type="button" on:click={openProjectAudits}><span aria-hidden="true">◎</span> Audits</button><button class="button quiet-button" type="button" data-report-bug-trigger on:click={openBugModal}><span aria-hidden="true">⚠</span> Report bug</button><button class="button primary" type="button" data-task-modal-trigger on:click={() => openTaskModal()}><span aria-hidden="true">＋</span> New task</button><input class="sr-only" bind:this={portableFileInput} type="file" accept=".json,application/json" aria-label="Import a Helm portable archive" on:change={handlePortableFile} /></div>
            </section>

            <div class="board-view-switch" role="tablist" aria-label="Board view">
              <button id="board-view-board" class:active={view === 'board'} type="button" role="tab" aria-selected={view === 'board'} tabindex={view === 'board' ? 0 : -1} on:click={() => setView('board')} on:keydown={boardViewKeydown}><span aria-hidden="true">▦</span> Board</button>
              <button id="board-view-timeline" class:active={view === 'timeline'} type="button" role="tab" aria-selected={view === 'timeline'} tabindex={view === 'timeline' ? 0 : -1} on:click={openProjectTimeline} on:keydown={boardViewKeydown}><span aria-hidden="true">◷</span> Timeline</button>
            </div>

            {#if view === 'board'}
            <section class="board-toolbar" aria-label="Board filters">
              <div class="filter-search"><span aria-hidden="true">⌕</span><input bind:this={boardSearchInput} aria-label="Search tasks" bind:value={filters.query} on:input={scheduleBoardReload} placeholder="Search tasks…" /><kbd>/</kbd></div>
              <div class="filter-group"><select aria-label="Filter by state" bind:value={filters.state} on:change={scheduleBoardReload}><option value="all">All states</option>{#each sortedColumns as column}<option value={column.semantic_state}>{stateLabels[column.semantic_state] || column.name}</option>{/each}</select><select aria-label="Filter by priority" bind:value={filters.priority} on:change={scheduleBoardReload}><option value="all">All priorities</option><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select><select aria-label="Filter by agent work" bind:value={boardWorkFilter} on:change={scheduleBoardReload}><option value="all">All agent work</option><option value="action-needed">Action needed{boardWorkCounts.actionNeeded ? ` · ${boardWorkCounts.actionNeeded}` : ''}</option><option value="missing">Missing{boardWorkCounts.missing ? ` · ${boardWorkCounts.missing}` : ''}</option><option value="stale">Stale{boardWorkCounts.stale ? ` · ${boardWorkCounts.stale}` : ''}</option><option value="waiting">Waiting{boardWorkCounts.waiting ? ` · ${boardWorkCounts.waiting}` : ''}</option><option value="handoff">Handoff{boardWorkCounts.handoff ? ` · ${boardWorkCounts.handoff}` : ''}</option><option value="working">Working{boardWorkCounts.working ? ` · ${boardWorkCounts.working}` : ''}</option><option value="verifying">Verifying{boardWorkCounts.verifying ? ` · ${boardWorkCounts.verifying}` : ''}</option></select><select aria-label="Filter by dependency readiness" bind:value={filters.dependency} on:change={scheduleBoardReload}><option value="all">All dependencies</option><option value="blocked">Waiting on prerequisites</option><option value="ready">Prerequisites finished</option></select><select aria-label="Filter by label" bind:value={filters.label} on:change={scheduleBoardReload}><option value="all">All labels</option>{#each labels as label}<option value={label.id}>{label.name}</option>{/each}</select><select aria-label="Filter by assignee" bind:value={filters.assignee} on:change={scheduleBoardReload}><option value="all">All assignees</option>{#each Array.from(new Map(tasks.map((task) => [actorId(task.assignee), task.assignee])).entries()).filter(([id]) => id) as pair}<option value={pair[0]}>{actorName(pair[1]) || pair[0]}</option>{/each}</select><select aria-label="Sort tasks" bind:value={boardSort} on:change={scheduleBoardReload}><option value="position">Board order</option><option value="number">Task number</option><option value="priority">Priority</option><option value="title">Title</option><option value="created_at">Created</option><option value="updated_at">Updated</option></select><select aria-label="Sort direction" bind:value={boardOrder} on:change={scheduleBoardReload}><option value="asc">Ascending</option><option value="desc">Descending</option></select></div>
              {#if boardFiltersActive()}<button class="clear-filters" type="button" on:click={clearFilters}>Clear filters</button>{/if}
              <span class="toolbar-spacer"></span><span class="task-total">{visibleTasks.length}{boardPartial ? '+' : ''} {visibleTasks.length === 1 ? 'task' : 'tasks'}</span><button class="icon-button" type="button" aria-label="Refresh board" on:click={() => loadBoard()}>↻</button>
            </section>

            {#if boardError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{boardError}</span><button class="text-button" type="button" on:click={() => loadBoard()}>Retry</button></div>{/if}
            {#if boardOffline}<div class="inline-alert warning content-alert" role="status"><span aria-hidden="true">⌁</span><span>You are offline. Showing the last loaded board pages.</span><button class="text-button" type="button" on:click={() => loadBoard()}>Retry</button></div>{/if}
            {#if Object.values(boardPages).some((page) => Boolean(page.error))}<div class="inline-alert warning content-alert" role="status"><span aria-hidden="true">…</span><span>Some board columns could not be loaded.</span><button class="text-button" type="button" on:click={() => loadBoard()}>Retry columns</button></div>{/if}
            {#if boardReconciliationNotice}<div class="inline-alert info content-alert" role="status"><span aria-hidden="true">↻</span><span>{boardReconciliationNotice}</span><button class="text-button" type="button" on:click={() => loadBoard()}>Refresh</button></div>{/if}
            {#if boardLoading && tasks.length}<div class="board-loading-note" role="status" aria-live="polite">Loading the latest board pages…</div>{/if}
            {#if boardLoading && !tasks.length}
              <div class="board board-loading" role="status" aria-label="Loading board" aria-live="polite" aria-busy="true"><span class="sr-only">Loading board…</span>{#each [1, 2, 3, 4] as item}<div class="column-skeleton" aria-hidden="true"><div></div><div></div><div></div></div>{/each}</div>
            {:else if !sortedColumns.length}
              <div class="empty-state board-empty"><div class="empty-icon">◇</div><h2>Your board is almost ready</h2><p>Columns will appear here once this project has been initialized.</p><button class="button primary" type="button" on:click={() => loadBoard()}>Refresh board</button></div>
            {:else}
              <section class="board" aria-label={`${activeProject.name} board`}>
                {#each sortedColumns as column}
                {@const orderingGate = makeBoardOrderingGate({
                  criteriaTransition: boardCriteriaTransition,
                  filterTimerPending: boardFilterTimer !== undefined,
                  boardLoading,
                  metadataError: Boolean(boardError),
                  pageRefreshActive: Boolean(boardColumnRefreshes[column.id]),
                  page: boardPages[column.id],
                  filters,
                  workFilter: boardWorkFilter,
                  sort: boardSort,
                  order: boardOrder
                })}
                {@const orderedColumnTasks = tasksByColumn[column.id] || []}
                <article
                  class="board-column"
                  class:drop-target={dragOverColumnId === column.id}
                  aria-label={`${column.name} column`}
                  aria-dropeffect="move"
                  on:dragover={(event) => dragOverColumn(event, column.id)}
                  on:dragleave={(event) => dragLeaveColumn(event, column.id)}
                  on:drop={(event) => dropTask(event, column.id)}
                >
                    <header class="column-header"><div class="column-name"><span class="column-dot" style={`--column-color: ${columnColor(column)}`}></span><h2>{column.name}</h2><span class="column-count">{tasksByColumn[column.id].length}{boardPages[column.id]?.nextCursor ? '+' : ''}</span></div></header>
                    <div class="column-progress"><span style={`width: ${Math.min(100, tasksByColumn[column.id].length * 4)}%; --column-color: ${columnColor(column)}`}></span></div>
                    <div class="column-cards">
                      {#if dragOverColumnId === column.id && draggingTaskId && tasksByColumn[column.id].some((task) => task.id === draggingTaskId) === false}
                        <div class="drop-placeholder" role="status">Drop task in {column.name}</div>
                      {/if}
                      {#if !tasksByColumn[column.id].length}
                        <div class="column-empty">{#if boardPages[column.id]?.error}<span>{boardPages[column.id].error}</span><button class="text-button" type="button" on:click={() => loadBoardColumn(column.id, { reset: true })}>Retry</button>{:else if boardFiltersActive()}<span>No tasks match the current filters.</span><button class="text-button" type="button" on:click={clearFilters}>Clear filters</button>{:else}<span>Nothing here yet</span><button class="text-button quick-add-trigger" type="button" data-quick-add-trigger={column.id} on:click={(event) => openQuickAdd(column.id, event.currentTarget as HTMLButtonElement)}>Add the first task</button>{/if}</div>
                      {:else}
                        {#each tasksByColumn[column.id] as task (task.id)}
                          <article class="task-card" class:dependency-blocked={dependencyBlocked(task)} class:dragging={draggingTaskId === task.id} on:dragend={endDrag} on:dragover|preventDefault={(event) => { if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'; }} on:drop={(event) => dropTask(event, column.id, task.id)}>
                            <button class="task-drag-handle" type="button" draggable="true" aria-label={`Drag ${task.key}, ${task.title}`} title="Drag task" on:click|stopPropagation={() => undefined} on:dragstart|stopPropagation={(event) => dragStart(event, task)}>⠿</button>
                            <button class="task-main" type="button" data-task-trigger aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown Alt+Home Alt+End" on:click={() => openTask(task)} on:keydown={(event) => keyboardMove(event, task)}>
                              <span class="task-card-top"><span class="task-key">{task.key}</span>{#if task.kind === 'bug'}<span class="issue-kind-badge">Bug</span>{#if task.bug?.severity}<span class="severity-badge">{task.bug.severity.toUpperCase()}</span>{/if}{/if}<span class={`priority-dot priority-${task.priority}`} title={`${priorityLabels[task.priority]} priority`}></span>{#if task.claimed_by}<span class="claim-mini" title={`Claimed by ${actorName(task.claimed_by) || 'another actor'}`}>●</span>{/if}</span>
                              <strong class="task-title">{task.title}</strong>
                              {#if task.description}<span class="task-excerpt">{task.description.replace(/[#*_`]/g, '').slice(0, 92)}{task.description.length > 92 ? '…' : ''}</span>{/if}
	                              {#if task.labels?.length}<span class="task-labels">{#each task.labels.slice(0, 3) as label}<span class="label-chip" style={`--label-color: ${label.color || '#8b7cf6'}`}>{label.name}</span>{/each}{#if task.labels.length > 3}<span class="label-more">+{task.labels.length - 3}</span>{/if}</span>{/if}
	                              <TaskDependencyStatus {task} />
	                              {#if task.checklist_summary?.total}<span class="checklist-card-progress" title={`${task.checklist_summary.completed} of ${task.checklist_summary.total} checklist items complete`} aria-label={`${task.checklist_summary.completed} of ${task.checklist_summary.total} checklist items complete`}>☑ {task.checklist_summary.completed}/{task.checklist_summary.total}</span>{/if}
	                              {#if hierarchyBadgeLabel(task)}<span class="hierarchy-badge" aria-label={`Hierarchy: ${hierarchyBadgeLabel(task)}`}><span aria-hidden="true">⌘</span>{hierarchyBadgeLabel(task)}</span>{/if}
                            </button>
                            {#if showAgentPulse(task)}<AgentPulse {task} now={pulseClock} actorLabel={agentLabelForTask(task)} />{/if}
                            <div class="task-card-footer"><span class={`due-date ${taskDueClass(task)}`}>{#if task.due_at}<span aria-hidden="true">◷</span>{formatDate(task.due_at)}{/if}</span><span class="card-footer-spacer"></span>{#if task.assignee}<span class="mini-avatar" title={`Assigned to ${actorName(task.assignee) || actorId(task.assignee)}`}>{(actorName(task.assignee) || actorId(task.assignee)).slice(0, 1).toUpperCase()}</span>{/if}{#if task.comment_count}<span class="comment-count" title={`${task.comment_count} comments`}>◌ {task.comment_count}</span>{/if}<button class="icon-button card-move order-move" type="button" aria-label={orderingMoveLabel(task, 'first', orderingGate)} title={orderingMoveTitle('first', orderingGate)} disabled={orderingMoveDisabled(task, 'first', orderedColumnTasks, orderingGate, taskActionLoading === task.id)} on:click={() => moveTaskToPosition(task, 'first')}>⇈</button><button class="icon-button card-move order-move" type="button" aria-label={orderingMoveLabel(task, 'previous', orderingGate)} title={orderingMoveTitle('previous', orderingGate)} disabled={orderingMoveDisabled(task, 'previous', orderedColumnTasks, orderingGate, taskActionLoading === task.id)} on:click={() => moveTaskToPosition(task, 'previous')}>↑</button><button class="icon-button card-move order-move" type="button" aria-label={orderingMoveLabel(task, 'next', orderingGate)} title={orderingMoveTitle('next', orderingGate)} disabled={orderingMoveDisabled(task, 'next', orderedColumnTasks, orderingGate, taskActionLoading === task.id)} on:click={() => moveTaskToPosition(task, 'next')}>↓</button><button class="icon-button card-move order-move" type="button" aria-label={orderingMoveLabel(task, 'last', orderingGate)} title={orderingMoveTitle('last', orderingGate)} disabled={orderingMoveDisabled(task, 'last', orderedColumnTasks, orderingGate, taskActionLoading === task.id)} on:click={() => moveTaskToPosition(task, 'last')}>⇊</button><button class="icon-button card-move" type="button" aria-label={cardMoveLabel(task, -1)} title={cardMoveReason(task, -1) || undefined} disabled={!adjacentTaskColumn(task, -1) || Boolean(cardMoveReason(task, -1)) || taskActionLoading === task.id} on:click={() => moveTaskBy(task, -1)}>←</button><button class="icon-button card-move" type="button" aria-label={cardMoveLabel(task, 1)} title={cardMoveReason(task, 1) || undefined} disabled={!adjacentTaskColumn(task, 1) || Boolean(cardMoveReason(task, 1)) || taskActionLoading === task.id} on:click={() => moveTaskBy(task, 1)}>→</button></div>
                          </article>
                        {/each}
                      {/if}
                      {#if boardPages[column.id]?.nextCursor}<button class="load-more-tasks" type="button" on:click={() => loadMoreBoardColumn(column.id)} disabled={boardPages[column.id].loading}>{boardPages[column.id].loading ? 'Loading…' : 'Load more tasks'}</button>{/if}
                      {#if boardPages[column.id]?.error && tasksByColumn[column.id].length}<div class="column-page-error" role="alert"><span>{boardPages[column.id].error}</span><button class="text-button" type="button" on:click={() => loadBoardColumn(column.id, { reset: false })}>Retry</button></div>{/if}
                    </div>
                    <div class="quick-add-wrap">
                      {#if quickAddColumn === column.id}
                        <form class="quick-add-form" on:submit|preventDefault={() => submitQuickAdd(column.id)}><input bind:this={quickAddInput} bind:value={quickAddTitle[column.id]} aria-label={`New task in ${column.name}`} placeholder="What needs doing?" /><div><button class="text-button" type="button" on:click={cancelQuickAdd}>Cancel</button><button class="button primary compact-button" type="submit" disabled={!quickAddTitle[column.id]?.trim() || taskActionLoading === `create-${column.id}`}>Add task</button></div></form>
                      {:else}<button class="quick-add-trigger" type="button" data-quick-add-trigger={column.id} on:click={(event) => openQuickAdd(column.id, event.currentTarget as HTMLButtonElement)}><span>＋</span> Add task</button>{/if}
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
        {:else if view === 'search'}
          <section class="page-heading search-heading">
            <div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Search</span>{#if searchView}<span>/</span><span>{searchView.name}</span>{/if}</div><h1>{searchView ? searchView.name : 'Search everything'}</h1><p>Find tasks across every project you can access, then open the board context in one click.</p></div>
            <div class="heading-actions"><button class="button quiet-button" type="button" on:click={() => { searchViewId = ''; navigate('/search'); void loadSearch(); }}>New search</button><button class="button quiet-button" type="button" on:click={() => loadSearch()}>↻ Refresh</button></div>
          </section>
          <section class="search-workspace" aria-label="Global task search">
              <form class="search-toolbar" on:submit|preventDefault={runSearch}>
                <div class="filter-search"><span aria-hidden="true">⌕</span><input aria-label="Search all tasks" bind:value={searchQuery} placeholder="Search tasks, keys, labels, projects…" /></div>
              <select aria-label="Filter search by state" bind:value={searchState} on:change={runSearch}><option value="all">All states</option>{#each Object.entries(stateLabels) as pair}<option value={pair[0]}>{pair[1]}</option>{/each}</select>
              <select aria-label="Filter search by priority" bind:value={searchPriority} on:change={runSearch}><option value="all">All priorities</option>{#each Object.entries(priorityLabels) as pair}<option value={pair[0]}>{pair[1]}</option>{/each}</select>
              <select aria-label="Sort search results" bind:value={searchSortField} on:change={runSearch}><option value="updated_at">Recently updated</option><option value="due_at">Due date</option><option value="priority">Priority</option><option value="title">Title</option><option value="key">Task key</option></select>
              <select aria-label="Sort search direction" bind:value={searchSortDirection} on:change={runSearch}><option value="desc">Descending</option><option value="asc">Ascending</option></select>
              <button class="button primary" type="submit">Search</button>
            </form>
            <section class="saved-view-panel" aria-label="Saved views">
              <div class="saved-view-heading"><div><span class="eyebrow">Reusable filters</span><h2>Saved views</h2><p>Save this search and share it with your workspace.</p></div><div class="saved-view-save"><input aria-label="Saved view name" bind:value={savedViewName} placeholder="Name this view" /><label class="check-label"><input type="checkbox" bind:checked={savedViewShared} /><span>Share</span></label><button class="button quiet-button" type="button" disabled={savedViewSaving} on:click={searchViewId ? updateCurrentSearchView : saveCurrentSearchView}>{searchViewId ? 'Update view' : 'Save view'}</button></div></div>
              {#if searchSavedViews.length}<div class="saved-view-list">{#each searchSavedViews as saved (saved.id)}<div class="saved-view-item"><button class:active={searchViewId === saved.id} class="saved-view-chip" type="button" on:click={() => openSavedView(saved)}><span>☆</span><span><strong>{saved.name}</strong><small>{saved.shared ? 'Shared' : 'Private'}</small></span></button>{#if saved.owner_id === user?.id}<button class="saved-view-delete" type="button" aria-label={`Delete saved view ${saved.name}`} on:click={() => removeCurrentSearchView(saved)}>×</button>{/if}</div>{/each}</div>{/if}
            </section>
            {#if searchError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{searchError}</span><button class="text-button" type="button" on:click={() => loadSearch()}>Retry</button></div>{/if}
            {#if searchLoading}<div class="list-skeleton" aria-label="Loading search results"><div></div><div></div><div></div></div>{:else if !searchTasks.length}<div class="empty-state search-empty"><div class="empty-icon">⌕</div><h2>No matching tasks</h2><p>Try a task key, project name, label, or a shorter phrase.</p></div>{:else}<section class="search-results" aria-label="Search results">{#each searchTasks as task (task.id)}<button class="search-result-row" type="button" on:click={() => openWorkTask(task)}><span class="work-project-dot" style={`--project-color: ${projectForTask(task)?.color || '#6d5efc'}`}></span><span class="search-result-main"><span class="work-row-top"><span class="task-key">{task.key}</span><span class={`priority-pill priority-${task.priority}`}>{priorityLabels[task.priority]}</span></span><strong>{task.title}</strong><span class="work-project-name">{projectForTask(task)?.name || 'Project'}</span></span><span class="search-result-column">{searchColumnsByProject[task.project_id]?.find((column) => column.id === task.column_id)?.name || '—'}</span><span class="row-arrow">→</span></button>{/each}</section>{#if searchNextCursor}<div class="search-pagination"><button class="button quiet-button" type="button" on:click={() => loadSearch(true)}>Load more results</button></div>{/if}{/if}
          </section>
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
            <div class="filter-search"><span aria-hidden="true">⌕</span><input bind:this={issueSearchInput} aria-label="Search issues" bind:value={issueFilters.query} placeholder="Search issues…" /><kbd>/</kbd></div>
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
            <article><span>Open</span><strong>{issueHealthMetrics.open}</strong></article>
            <article><span>Untriaged</span><strong>{issueHealthMetrics.untriaged}</strong></article>
            <article><span>S1 / S2 open</span><strong>{issueHealthMetrics.severe}</strong></article>
            <article><span>Resolved · 7d</span><strong>{issueHealthMetrics.recentlyResolved}</strong></article>
            <article><span>Reopened · 7d</span><strong>{issueHealthMetrics.reopened === null ? '—' : issueHealthMetrics.reopened}</strong></article>
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
          {#if user?.admin}
            <section class="settings-section admin-section" aria-labelledby="project-admin-heading">
              <div class="settings-section-heading">
                <div><span class="eyebrow">Workspace administration</span><h2 id="project-admin-heading">Projects &amp; columns</h2><p>Rename projects, tune their descriptions and colors, and keep the board’s semantic columns in a safe order.</p></div>
                <span class="admin-lock-badge">Admin only</span>
              </div>
              {#if adminError}<div class="inline-alert error" role="alert"><span>!</span>{adminError}<button class="text-button" type="button" on:click={loadProjectAdmin}>Retry</button></div>{/if}
              {#if adminLoading}<div class="list-skeleton" aria-label="Loading project administration"><div></div><div></div></div>
              {:else if !adminProjects.length}<div class="empty-state compact-empty"><div class="empty-icon">◎</div><h3>No projects yet</h3><p>Create a project first, then its columns can be managed here.</p></div>
              {:else if adminProject}
                <div class="admin-project-picker"><label for="admin-project-select">Project<select id="admin-project-select" bind:value={adminProjectId} on:change={() => { const selected = adminProjects.find((project) => project.id === adminProjectId); if (selected) { setAdminProjectDraft(selected); void loadAdminColumns(selected.id); } }}><option value="" disabled>Choose a project</option>{#each adminProjects as project}<option value={project.id}>{project.key} · {project.name}{#if project.archived_at} · archived{/if}</option>{/each}</select></label><span class:admin-status-archived={Boolean(adminProject.archived_at)} class="admin-status">{adminProject.archived_at ? 'Archived' : 'Active'}</span></div>
                <form class="admin-project-form" on:submit|preventDefault={confirmAdminProjectSave}>
                  <div class="form-row"><label>Project name<input bind:value={adminProjectName} maxlength="200" /></label><label>Accent color<input class="color-input" type="color" bind:value={adminProjectColor} /></label></div>
                  <label>Description <span class="optional">Optional</span><textarea rows="3" maxlength="20000" bind:value={adminProjectDescription}></textarea></label>
                  <label>Checklist completion policy<select bind:value={adminChecklistPolicy}><option value="warn">Warn · allow completion with open items</option><option value="require">Require · block completion until all items are checked</option></select><span class="field-hint">Applies to task moves, bug resolution, and columns remapped to Done.</span></label>
                  <div class="form-actions"><button class="button quiet-button" type="button" on:click={() => confirmAdminProjectArchive(adminProject)}>{adminProject.archived_at ? 'Restore project' : 'Archive project'}</button><button class="button primary" type="submit" disabled={adminSaving || !adminProjectName.trim()}>{#if adminSaving}<span class="button-spinner"></span>{/if}Save project</button></div>
                </form>
                <div class="admin-columns-heading"><div><h3>Columns</h3><p>Each semantic state must keep one live mapping. Archiving a column moves its tasks to another live column with the same state.</p></div></div>
                <form class="admin-column-create" on:submit|preventDefault={confirmAdminColumnCreate}><label>New column name<input bind:value={adminColumnName} maxlength="100" placeholder="Review" /></label><label>Semantic mapping<select bind:value={adminColumnState}>{#each Object.entries(stateLabels) as [state, label]}<option value={state}>{label}</option>{/each}</select></label><button class="button primary" type="submit" disabled={adminColumnCreating || !adminColumnName.trim()}>＋ Add column</button></form>
                {#if adminColumnsLoading}<div class="list-skeleton" aria-label="Loading columns"><div></div></div>
                {:else if !adminColumns.length}<p class="admin-empty-note">No columns found for this project.</p>
                {:else}<div class="admin-column-list" aria-label="Project columns">{#each adminColumns as column (column.id)}<article class:archived={Boolean(column.archived_at)} class="admin-column-row"><div class="admin-column-grip" aria-hidden="true">⋮⋮</div><div class="admin-column-fields"><label><span class="sr-only">Column name</span><input aria-label={`Column name for ${column.name}`} bind:value={column.name} maxlength="100" /></label><label><span class="sr-only">Semantic mapping</span><select aria-label={`Semantic mapping for ${column.name}`} bind:value={column.semantic_state}>{#each Object.entries(stateLabels) as [state, label]}<option value={state}>{label}</option>{/each}</select></label><span class="admin-column-position">#{column.position + 1}</span></div><div class="admin-column-actions"><button class="icon-button tiny" type="button" aria-label={`Move ${column.name} up`} disabled={Boolean(column.archived_at) || adminColumnSaving === column.id || (adminLiveColumnIndexes.get(column.id) ?? -1) <= 0} on:click={() => confirmAdminColumnMove(column, Math.max(0, column.position - 1))}>↑</button><button class="icon-button tiny" type="button" aria-label={`Move ${column.name} down`} disabled={Boolean(column.archived_at) || adminColumnSaving === column.id || (adminLiveColumnIndexes.get(column.id) ?? -1) >= adminLiveColumns.length - 1} on:click={() => confirmAdminColumnMove(column, column.position + 1)}>↓</button><button class="button quiet-button compact-button" type="button" disabled={adminColumnSaving === column.id || !column.name.trim()} on:click={() => confirmAdminColumnSave(column)}>{adminColumnSaving === column.id ? 'Saving…' : 'Save'}</button><button class="text-button compact-button" type="button" disabled={adminColumnSaving === column.id} on:click={() => confirmAdminColumnArchive(column)}>{column.archived_at ? 'Restore' : 'Archive'}</button></div></article>{/each}</div>{/if}
              {/if}
            </section>
          {/if}
          <section class="settings-section codex-section">
            <div class="settings-section-heading"><div><span class="eyebrow">Luna assistant</span><h2>Your Codex subscription</h2><p>Connect your own Codex-enabled ChatGPT account. Helm stores it only in your private user runtime and never shares it with another user.</p></div>{#if codexAccount?.connected}<span class="codex-connected">✓ Connected</span>{/if}</div>
            {#if codexError}<div class="inline-alert error" role="alert"><span>!</span>{codexError}</div>{/if}
            {#if codexStatusLoading && !codexAccount && !codexLogin}<div class="list-skeleton"><div></div></div>
            {:else if codexLogin}<div class="codex-device-panel"><div><strong>Finish connecting in ChatGPT</strong><p>Open the secure sign-in page and enter this one-time code.</p></div><div class="codex-code-row"><code>{codexLogin.user_code}</code><button class="button quiet-button compact-button" type="button" on:click={copyCodexCode}>Copy</button></div><div class="form-actions"><button class="text-button" type="button" disabled={codexLoading} on:click={cancelCodexLogin}>Cancel login</button><a class="button primary" href={codexLogin.verification_url} target="_blank" rel="noreferrer">Open ChatGPT ↗</a></div></div>
            {:else if codexAccount?.connected}<div class="codex-account-row"><span class="agent-avatar">✦</span><span><strong>{codexAccount.email || 'ChatGPT account'}</strong><small>{codexAccount.plan_type ? `${codexAccount.plan_type} plan` : 'Codex enabled'}</small></span><div class="form-actions"><button class="button quiet-button compact-button" type="button" disabled={codexLoading || codexStatusLoading} on:click={() => loadCodexAccount(true)}>Check access</button><button class="text-button danger-button" type="button" disabled={codexLoading} on:click={disconnectCodex}>Disconnect</button></div></div>
            {:else}<div class="codex-empty"><p>Luna needs your personal ChatGPT subscription before it can draft tasks. Device-code login works on remote and headless self-hosted Helm installations.</p><button class="button primary" type="button" disabled={codexLoading} on:click={connectCodex}>{#if codexLoading}<span class="button-spinner"></span>{/if}Connect Codex</button></div>{/if}
          </section>
          {#if agentsError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{agentsError}<button class="text-button" type="button" on:click={loadAgents}>Retry</button></div>{/if}
          <section class="settings-layout"><div class="settings-main"><div class="settings-section"><div class="settings-section-heading"><div><span class="eyebrow">Coordination</span><h2>Agents &amp; tokens</h2><p>Give software agents scoped access without sharing a human login.</p></div><button class="button primary" type="button" on:click={() => showAgentForm = !showAgentForm}>＋ Add agent</button></div>{#if showAgentForm}<div class="settings-form"><label>Agent name<input bind:value={agentNameDraft} placeholder="Release assistant" /></label><label>Description <span class="optional">Optional</span><textarea rows="2" bind:value={agentDescriptionDraft} placeholder="What is this agent responsible for?"></textarea></label><div class="form-actions"><button class="text-button" type="button" on:click={() => showAgentForm = false}>Cancel</button><button class="button primary" type="button" disabled={!agentNameDraft.trim()} on:click={createAgent}>Create agent</button></div></div>{/if}{#if agentsLoading}<div class="list-skeleton">{#each [1, 2] as item}<div></div>{/each}</div>{:else if !agents.length}<div class="empty-state compact-empty"><div class="empty-icon">✦</div><h3>No agents yet</h3><p>Create a scoped identity for the tools that collaborate with you.</p><button class="button quiet-button" type="button" on:click={() => showAgentForm = true}>Create your first agent</button></div>{:else}<div class="agent-list">{#each agents as agent}<article class="agent-card"><div class="agent-card-header"><span class="agent-avatar">✦</span><div><h3>{agent.name}</h3><p>{agent.description || 'No description'}</p></div><button class="button quiet-button compact-button" type="button" data-token-trigger on:click={() => { selectedAgentId = agent.id; showTokenForm = selectedAgentId === agent.id && !showTokenForm; }}>＋ Token</button></div>{#if agent.tokens?.length}<div class="token-list">{#each agent.tokens as token}<div class="token-row"><span class="token-icon">⌘</span><span class="token-info"><strong>{token.name}</strong><small>{token.scopes.join(' · ')}</small></span><span class="token-date">{token.expires_at ? `Expires ${formatDate(token.expires_at)}` : 'No expiry'}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Revoke ${token.name}`} on:click={() => deleteToken(token)}>×</button></div>{/each}</div>{:else}<div class="agent-no-tokens">No active tokens</div>{/if}{#if showTokenForm && selectedAgentId === agent.id}<div class="token-form"><div class="settings-form"><label>Token name<input bind:value={tokenNameDraft} placeholder="CI deployment token" /></label><fieldset><legend>Scopes</legend><div class="scope-grid">{#each scopeOptions as scope}<label class="check-label"><input type="checkbox" checked={tokenScopes.includes(scope)} on:change={() => toggleScope(scope)} /><span>{scope}</span></label>{/each}</div></fieldset><fieldset><legend>Limit to projects <span class="optional">Optional</span></legend><div class="scope-grid project-checks">{#each projects as project}<label class="check-label"><input type="checkbox" checked={tokenProjectIds.includes(project.id)} on:change={() => toggleTokenProject(project.id)} /><span>{project.name}</span></label>{/each}</div></fieldset><div class="form-actions"><button class="text-button" type="button" on:click={() => showTokenForm = false}>Cancel</button><button class="button primary" type="button" disabled={!tokenNameDraft.trim() || !tokenScopes.length || tokenCreating} on:click={createToken}>{#if tokenCreating}<span class="button-spinner"></span>{/if}Create token</button></div></div></div>{/if}</article>{/each}</div>{/if}</div><div class="settings-section appearance-section"><div class="settings-section-heading"><div><span class="eyebrow">Workspace</span><h2>Appearance</h2><p>Choose how Helm feels on this device.</p></div></div><div class="theme-options"><button class:chosen={theme === 'light'} type="button" on:click={() => { theme = 'light'; localStorage.setItem(helmStorageKeys.theme, theme); applyTheme(); }}><span class="theme-preview light-preview">☼</span><span><strong>Light</strong><small>Clear and airy</small></span>{#if theme === 'light'}<span class="theme-check">✓</span>{/if}</button><button class:chosen={theme === 'dark'} type="button" on:click={() => { theme = 'dark'; localStorage.setItem(helmStorageKeys.theme, theme); applyTheme(); }}><span class="theme-preview dark-preview">☾</span><span><strong>Dark</strong><small>Focused and low-glare</small></span>{#if theme === 'dark'}<span class="theme-check">✓</span>{/if}</button></div></div></div><aside class="settings-aside"><div class="settings-aside-card"><span class="aside-icon">◎</span><h3>Built for safe handoffs</h3><p>Every mutation records its actor. Scoped agent tokens and optimistic versions keep collaboration predictable.</p><span class="aside-rule"></span><span class="aside-caption">Helm v1 · API-connected</span></div></aside></section>
        {/if}
      </main>
    </div>

    {#if drawerTask}
      <div class="drawer-backdrop" role="presentation" on:click={() => closeDrawer()}></div>
      <div class="task-drawer" role="dialog" aria-modal="true" aria-label={`${drawerTask.key}: ${drawerTask.title}`} use:focusTrap>
        <div class="drawer-focus-target sr-only" tabindex="-1" data-dialog-initial-focus aria-label="Task details"></div>
        <div class="drawer-header"><div><span class="drawer-key">{drawerTask.key}</span><span class="issue-kind-badge" class:task-kind={drawerTask.kind !== 'bug'}>{drawerTask.kind === 'bug' ? 'Bug' : 'Task'}</span>{#if drawerTask.kind === 'bug'}<span class:untriaged={!drawerTask.bug?.severity} class="severity-badge">{drawerTask.bug?.severity ? severityLabels[drawerTask.bug.severity] : 'Untriaged'}</span>{/if}<span class={`priority-pill priority-${drawerTask.priority}`}>{priorityLabels[drawerTask.priority]}</span></div><button class="icon-button" type="button" aria-label="Close task details" on:click={() => closeDrawer()}>×</button></div>
        {#if drawerLoading}<div class="drawer-loading"><span class="spinner"></span><span>Loading task details…</span></div>{/if}
        {#if drawerError}<div class="inline-alert error drawer-alert" role="alert"><span>!</span>{drawerError}</div>{/if}
        <div class="drawer-tabs" role="tablist" aria-label="Task views">
          <button class:active={drawerView === 'details'} id="drawer-details-tab" class="drawer-tab" type="button" role="tab" aria-selected={drawerView === 'details'} aria-controls="drawer-details-panel" tabindex={drawerView === 'details' ? 0 : -1} on:click={() => setDrawerView('details')} on:keydown={drawerTabKeydown}>Details</button>
          <button class:active={drawerView === 'activity'} id="drawer-activity-tab" class="drawer-tab" type="button" role="tab" aria-selected={drawerView === 'activity'} aria-controls="drawer-activity-panel" tabindex={drawerView === 'activity' ? 0 : -1} on:click={() => setDrawerView('activity')} on:keydown={drawerTabKeydown}>Activity</button>
        </div>
        {#if drawerView === 'details'}
        <div id="drawer-details-panel" class="drawer-details-panel" role="tabpanel" aria-labelledby="drawer-details-tab">
          <div class="drawer-scroll" data-drawer-scroll>
        {#if drawerTask.kind === 'bug'}
          <div class="drawer-bug-controls">
            <section class="drawer-section bug-details-section" aria-labelledby="bug-details-heading"><div class="section-heading-inline"><h2 id="bug-details-heading">Bug report</h2><span class="optional">Reporter: {drawerTask.bug?.reporter_id || 'Unknown'}</span></div><label>Actual behavior<textarea rows="3" bind:value={draftBugActual} placeholder="What happened?"></textarea></label><label>Expected behavior<textarea rows="3" bind:value={draftBugExpected} placeholder="What should have happened?"></textarea></label><label>Reproduction steps<textarea rows="3" bind:value={draftBugReproduction} placeholder="1. Open…&#10;2. Click…"></textarea></label><div class="drawer-field-grid"><label>Environment<input bind:value={draftBugEnvironment} placeholder="Browser, OS, device" /></label><label>Affected version<input bind:value={draftBugVersion} placeholder="e.g. 1.4.0" /></label></div></section>
            <section class="drawer-section bug-triage-section" aria-labelledby="bug-triage-heading"><div class="section-heading-inline"><h2 id="bug-triage-heading">Triage</h2><span class="optional">Set severity and ownership</span></div><div class="drawer-field-grid"><label>Severity<select aria-label="Bug severity" bind:value={triageSeverityDraft}><option value="s1">{severityLabels.s1}</option><option value="s2">{severityLabels.s2}</option><option value="s3">{severityLabels.s3}</option><option value="s4">{severityLabels.s4}</option></select></label><label>Priority<select aria-label="Triage priority" bind:value={draftPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label></div><label>Assignee<input aria-label="Triage assignee" bind:value={draftAssignee} placeholder="Actor ID (optional)" /></label><button class="button primary" type="button" title={dependencyBlocked(drawerTask) ? dependencyActionExplanation(drawerTask, 'start triage') : undefined} disabled={dependencyBlocked(drawerTask) || drawerSaving || taskActionLoading === drawerTask.id} on:click={triageBug}>{#if taskActionLoading === drawerTask.id}<span class="button-spinner"></span>{/if}{drawerTask.bug?.severity ? 'Update triage' : 'Triage issue'}</button></section>
            {#if drawerTask.bug?.resolution}
              <section class="drawer-section bug-resolution-section" aria-labelledby="bug-resolution-heading"><div class="section-heading-inline"><h2 id="bug-resolution-heading">Resolved as {resolutionLabels[drawerTask.bug.resolution] || drawerTask.bug.resolution}</h2><span class="optional">Reopen if the issue persists</span></div><label>Reopen reason<textarea rows="2" bind:value={reopenReasonDraft} placeholder="Why does this need another look?"></textarea></label><button class="button quiet-button" type="button" disabled={drawerSaving || taskActionLoading === drawerTask.id || !reopenReasonDraft.trim()} on:click={reopenBug}>Reopen issue</button></section>
            {:else}
              <section class="drawer-section bug-resolution-section" aria-labelledby="bug-resolution-heading"><div class="section-heading-inline"><h2 id="bug-resolution-heading">Resolve</h2><span class="optional">Close the loop for reporters</span></div><label>Resolution<select aria-label="Bug resolution" bind:value={resolutionDraft}>{#each resolutionOptions as resolution}<option value={resolution}>{resolutionLabels[resolution]}</option>{/each}</select></label>{#if resolutionDraft === 'duplicate'}<label>Duplicate of<input aria-label="Duplicate issue" bind:value={duplicateOfDraft} placeholder="Task key or ID" /></label>{/if}<label>Resolution note <span class="optional">Optional</span><textarea rows="2" bind:value={resolutionNoteDraft} placeholder="What changed or why was this closed?"></textarea></label><button class="button complete-button" type="button" title={dependencyBlocked(drawerTask) ? dependencyActionExplanation(drawerTask, 'resolve this issue') : undefined} disabled={dependencyBlocked(drawerTask) || drawerSaving || taskActionLoading === drawerTask.id} on:click={resolveBug}>Resolve issue</button></section>
            {/if}
          </div>
        {/if}
        {#if drawerTask.claimed_by}
          <div class="claim-lease" class:expired={!claimIsActive(drawerTask, pulseClock)} class:expiring-soon={claimExpiresSoon(drawerTask, pulseClock)} class:conflict={claimConflict(drawerTask, pulseClock)} role={claimConflict(drawerTask, pulseClock) || claimExpiresSoon(drawerTask, pulseClock) ? 'alert' : undefined}>
            <span class="claim-lease-icon" aria-hidden="true">⚑</span>
            <span><strong>{claimConflict(drawerTask, pulseClock) ? `Claim conflict · ${claimOwnerLabel(drawerTask)}` : `Claimed by ${claimOwnerLabel(drawerTask)}`}</strong>{#if drawerTask.claim_expires_at}<small><time datetime={drawerTask.claim_expires_at}>{claimIsActive(drawerTask, pulseClock) ? `Expires ${claimExpiryExact(drawerTask.claim_expires_at)}` : `Expired ${claimExpiryExact(drawerTask.claim_expires_at)}`}</time> · {claimCountdown(drawerTask, pulseClock)} · Task version v{drawerTask.version}</small>{:else}<small>No expiry reported · Task version v{drawerTask.version}</small>{/if}{#if claimExpiresSoon(drawerTask, pulseClock)}<small class="claim-lease-warning">Expiring soon — renew this claim before it expires.</small>{/if}</span>
          </div>
        {/if}
        <button class="button quiet-button block-button" type="button" disabled={Boolean(drawerTask.completed_at) || drawerSaving || taskActionLoading === drawerTask.id} on:click={openBlockReason}>■ Block</button>
        {#if blockReasonOpen}
          <section class="block-reason-form" aria-labelledby="block-reason-heading"><label id="block-reason-heading">Why is this task blocked?<textarea rows="3" bind:value={blockReasonDraft} placeholder="Describe the dependency or decision needed." required></textarea></label><div class="form-actions"><button class="text-button" type="button" on:click={() => { blockReasonOpen = false; blockReasonDraft = ''; }}>Cancel</button><button class="button danger-button" type="button" disabled={!blockReasonDraft.trim() || drawerSaving || taskActionLoading === drawerTask.id} on:click={() => runTaskAction('block', blockReasonDraft)}>Block task</button></div></section>
        {/if}
        <TaskDependencyStatus task={drawerTask} mode="notice" />
              <label class="drawer-title-label"><span class="sr-only">Task title</span><input id="drawer-title" class="drawer-title-input" bind:value={draftTitle} /></label><div class="drawer-meta"><span class="task-project-marker" style={`--project-color: ${projectForTask(drawerTask)?.color || '#6d5efc'}`}></span><span>{projectForTask(drawerTask)?.name || 'Project'}</span><span>·</span><span>Updated {formatRelative(drawerTask?.updated_at)}</span></div><div class="drawer-actions"><button class="button quiet-button" type="button" title={dependencyBlocked(drawerTask) ? dependencyActionExplanation(drawerTask, claimAction(drawerTask) === 'renew' ? 'renew this claim' : 'claim this task') : claimConflict(drawerTask, pulseClock) ? `Claim held by ${claimOwnerLabel(drawerTask)} · task version v${drawerTask.version}` : undefined} disabled={drawerSaving || taskActionLoading === drawerTask?.id || dependencyBlocked(drawerTask) || claimConflict(drawerTask, pulseClock)} on:click={() => runTaskAction(claimAction(drawerTask))}>{drawerTask.claimed_by && actorId(drawerTask.claimed_by) === user?.id && claimIsActive(drawerTask, pulseClock) ? '↻ Renew claim' : drawerTask.claimed_by && claimConflict(drawerTask, pulseClock) ? `Claimed by ${actorName(drawerTask.claimed_by) || 'agent'}` : '⚑ Claim task'}</button>{#if drawerTask.claimed_by && actorId(drawerTask.claimed_by) === user?.id && claimIsActive(drawerTask, pulseClock)}<button class="button quiet-button" type="button" disabled={drawerSaving || taskActionLoading === drawerTask?.id} on:click={() => runTaskAction('release')}>Release</button>{/if}<button class="button complete-button" type="button" title={dependencyBlocked(drawerTask) ? dependencyActionExplanation(drawerTask, 'complete this task') : undefined} disabled={drawerSaving || Boolean(drawerTask.completed_at) || dependencyBlocked(drawerTask) || taskActionLoading === drawerTask.id} on:click={() => runTaskAction('complete')}>{drawerTask.completed_at ? '✓ Completed' : '✓ Complete'}</button></div>{#if showAgentPulse(drawerTask)}<AgentWorkPanel task={drawerTask} now={pulseClock} actorLabel={agentLabelForTask(drawerTask)} />{/if}<section class="drawer-section"><div class="drawer-field-grid"><label>Priority<select bind:value={draftPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label><label>Due date<input type="date" bind:value={draftDueDate} /></label></div><label>Assignee<input bind:value={draftAssignee} placeholder="Actor ID (optional)" /></label><label>Labels <span class="optional">Comma separated</span><input bind:value={draftLabels} placeholder="frontend, design" /></label>{#if labels.filter((label) => label.project_id === drawerTask?.project_id).length}<div class="drawer-label-picker"><span class="optional">Project labels</span><div class="drawer-label-options">{#each labels.filter((label) => label.project_id === drawerTask?.project_id) as label (label.id)}<span class="drawer-label-option" style={`--label-color: ${label.color || '#8b7cf6'}`}><span>{label.name}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Delete label ${label.name}`} disabled={labelDeleting === label.id} on:click|stopPropagation={() => deleteProjectLabel(label)}>×</button></span>{/each}</div></div>{/if}</section>
                <TaskDependencies
                  bind:this={drawerDependencyPanel}
                  task={drawerTask}
                  refreshToken={drawerDependencyRefresh}
                  onTaskUpdated={handleDependencyTaskUpdated}
                  onNavigate={openDependencyTask}
                  onRefreshTask={refreshDependencyTask}
                />
	                <TaskChecklist
	                  task={drawerTask}
	                  onTaskUpdated={handleChecklistTaskUpdated}
	                  onRefreshTask={refreshDependencyTask}
	                />
	                <TaskHierarchy
	                  bind:this={drawerHierarchyPanel}
	                  task={drawerTask}
	                  refreshToken={drawerHierarchyRefresh}
	                  onTaskUpdated={handleHierarchyTaskUpdated}
	                  onNavigate={openHierarchyTask}
	                  onRefreshTask={refreshHierarchyTask}
	                  onCreateChild={createChildTask}
	                />
	                <section class="drawer-section description-section"><div class="section-heading-inline"><h2>Description</h2><span class="markdown-hint">Markdown supported</span></div><textarea class="description-input" rows="7" bind:value={draftDescription} placeholder="What does success look like?"></textarea></section><div class="drawer-save-bar" class:dirty={drawerDraftDirty} aria-live="polite"><span class="drawer-save-status">{#if drawerSaving}<span class="button-spinner"></span>Saving changes…{:else if drawerTaskDraftDirty}<span class="drawer-status-dot"></span>Unsaved changes{:else if drawerDraftDirty}<span class="drawer-status-dot"></span>Action draft pending{:else}<span class="drawer-status-check">✓</span>All changes saved{/if}</span><button class="button primary save-task-button" type="button" disabled={drawerSaving || !drawerTaskDraftDirty || !draftTitle.trim()} on:click={() => void saveTask()}>{#if drawerSaving}<span class="button-spinner"></span>{/if}{drawerTaskDraftDirty ? 'Save changes' : 'Saved'}</button></div>
	          </div>
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
                currentActorId={user?.id || ''}
                canManageComments={Boolean(user?.admin)}
                onEditComment={editDrawerComment}
                onConfirmDelete={confirmDrawerCommentDelete}
                onDeleteComment={deleteDrawerComment}
              />
            </section>
          </div>
        {/if}
        <div class="drawer-delete-wrap"><button class="button danger-button" type="button" disabled={drawerSaving || taskActionLoading === drawerTask?.id} on:click={deleteDrawerTask}>Delete task</button></div>
      </div>
    {/if}

    {#if commandOpen}
      <div class="modal-backdrop command-backdrop" role="presentation" on:click={closeCommandPalette}></div>
      <div class="command-menu" role="dialog" aria-modal="true" aria-labelledby="command-dialog-title" use:focusTrap>
        <h2 id="command-dialog-title" class="sr-only">Search Helm</h2>
        <div class="command-input-wrap"><span aria-hidden="true">⌕</span><input bind:this={commandInput} data-dialog-initial-focus bind:value={commandQuery} on:input={() => { commandIndex = 0; commandInputChanged(); }} on:keydown={commandKeydown} placeholder="Search projects, tasks, issues, views, or actions…" aria-label="Search projects and views, tasks, issues, and actions" role="combobox" aria-expanded={commandOpen} aria-controls="command-results" aria-autocomplete="list" aria-activedescendant={activeCommandOptionId || undefined} autocomplete="off" /><kbd>ESC</kbd></div>
        {#if commandIssuesLoading || commandSearchLoading}<div class="command-status" role="status" aria-live="polite">Searching Helm…</div>{/if}
        {#if commandIssuesError}<div class="command-status command-status-error" role="alert"><span>{commandIssuesError}</span><button class="text-button" type="button" on:click={() => void loadCommandIssues()}>Retry issue search</button></div>{/if}
        <div id="command-results" class="command-results" role="listbox" aria-label="Command results" aria-busy={commandIssuesLoading || commandSearchLoading}>
          {#if commandChoices.length}
            {#each commandChoices as choice, index (choice.id)}
              <button id={commandChoiceId(choice)} class:selected={index === commandIndex} class={`command-row ${choice.kind}`} role="option" aria-selected={index === commandIndex} aria-posinset={index + 1} aria-setsize={commandChoices.length} aria-label={`${choice.label}. ${choice.hint}`} tabindex="-1" type="button" on:focus={() => commandIndex = index} on:mouseenter={() => commandIndex = index} on:click={() => void selectCommand(choice)} on:keydown={(event) => commandOptionKeydown(event, choice)} on:mousedown|preventDefault><span class={`command-icon ${choice.kind}`} aria-hidden="true">{commandChoiceIcon(choice)}</span><span><strong>{choice.label}</strong><small>{choice.hint}</small></span><span class="command-enter" aria-hidden="true">↵</span></button>
            {/each}
          {:else if commandIssuesLoading || commandSearchLoading}
            <div class="command-empty" role="status" aria-live="polite">Searching Helm…</div>
          {:else}
            <div class="command-empty" role="status" aria-live="polite">No commands match “{commandQuery}”. Try a different search.</div>
          {/if}
        </div>
        <div class="command-footer"><span><kbd>↑</kbd><kbd>↓</kbd> Navigate</span><span><kbd>↵</kbd> Open</span><span><kbd data-command-shortcut>{commandShortcut}</kbd> Open palette</span><span><kbd>ESC</kbd> Close</span></div>
      </div>
    {/if}

    {#if showTaskModal}
      <div class="modal-backdrop" role="presentation" on:click={closeTaskModal}></div>
      <div class="modal task-create-modal" role="dialog" aria-modal="true" aria-labelledby="task-modal-title" use:focusTrap>
        <div class="modal-header"><div><span class="eyebrow">Capture an idea</span><h2 id="task-modal-title">Create a task</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeTaskModal}>×</button></div>
        {#if taskModalError}<div class="inline-alert error" role="alert"><span>!</span>{taskModalError}</div>{/if}
		<form on:submit|preventDefault={createGlobalTask}>
		  <div class="form-row task-destination-row"><label>Project<select bind:value={taskModalProjectId} on:change={changeTaskModalProject}>{#each projects as project}<option value={project.id}>{project.key} · {project.name}</option>{/each}</select></label><label>Column<select bind:value={taskModalColumnId} disabled={taskModalLoading || !taskModalColumns.length}>{#each taskModalColumns as column}<option value={column.id}>{column.name}</option>{/each}</select></label></div>
		  {#if taskModalParentId && drawerTask?.id === taskModalParentId}<p class="task-parent-note">This task will be created as a child of <strong>{drawerTask.key}</strong>.</p>{/if}
		  <section class="luna-assist-panel" aria-labelledby="luna-assist-heading" aria-describedby="luna-assist-description">
		    <div class="luna-assist-heading"><div><span class="eyebrow">Optional assist</span><h3 id="luna-assist-heading">Plan it with Luna</h3><p id="luna-assist-description">Describe the outcome and Luna will suggest the task details.</p></div><span class="luna-mark" aria-hidden="true">✦</span></div>
            <label>Rough idea<textarea data-dialog-initial-focus rows="2" bind:value={taskModalIdea} placeholder="e.g. Let self-hosted users connect their own Codex subscription"></textarea></label>
            {#if taskModalAssisting}<div class="luna-progress" aria-live="polite"><span class="button-spinner"></span><span>{taskModalAssistStage}</span><button class="text-button" type="button" on:click={cancelTaskAssist}>Cancel</button></div>
            {:else}<button class="button luna-button" type="button" disabled={!taskModalIdea.trim() && !taskModalTitle.trim()} on:click={assistTaskWithLuna}>✦ {taskModalSuggestion ? 'Try again with Luna' : 'Assist with Luna'}</button>{/if}
            {#if taskModalNeedsCodex}
              <div class="luna-connect" role="status"><div><strong>Connect your Codex subscription</strong><p>This draft stays here while you finish device-code login.</p></div>{#if codexLogin}<div class="codex-code-row"><code>{codexLogin.user_code}</code><button class="button quiet-button compact-button" type="button" on:click={copyCodexCode}>Copy</button></div><div class="form-actions"><button class="text-button" type="button" on:click={cancelCodexLogin}>Cancel login</button><a class="button primary" href={codexLogin.verification_url} target="_blank" rel="noreferrer">Open ChatGPT ↗</a></div>{:else}<button class="button primary" type="button" disabled={codexLoading} on:click={connectCodex}>{#if codexLoading}<span class="button-spinner"></span>{/if}Connect Codex</button>{/if}</div>
            {/if}
            {#if taskModalSuggestion}
              <div class="luna-preview" class:collapsed={taskModalSuggestionCollapsed}>
                <div class="luna-preview-header"><div><span class="eyebrow">Suggestion ready</span><h4>{taskModalSuggestionCollapsed && taskModalAllFieldsApplied ? 'Suggestion applied' : 'Review before applying'}</h4></div><div class="luna-preview-actions"><button class="button primary compact-button" type="button" disabled={taskModalAllFieldsApplied} on:click={applyAllLunaFields}>{taskModalAllFieldsApplied ? '✓ All applied' : taskModalHasAppliedAll ? 'Reapply all' : 'Apply all'}</button><button class="text-button luna-review-toggle" type="button" aria-expanded={!taskModalSuggestionCollapsed} aria-controls="luna-suggestion-details" on:click={() => taskModalSuggestionCollapsed = !taskModalSuggestionCollapsed}>{taskModalSuggestionCollapsed ? 'Review suggestion' : 'Hide suggestion'}</button></div></div>
                {#if taskModalApplyNotice}<div class="luna-feedback" class:luna-apply-success={taskModalAllFieldsApplied} role="status" aria-live="polite"><span class="luna-feedback-icon" aria-hidden="true">{taskModalAllFieldsApplied ? '✓' : 'i'}</span><span>{taskModalHasAppliedAll && !taskModalAllFieldsApplied ? 'Task details changed after applying. Review the suggestion or reapply all.' : taskModalApplyNotice}</span></div>{/if}
                <div id="luna-suggestion-details" class="luna-suggestion-details" hidden={taskModalSuggestionCollapsed}>
                  <div class="luna-preview-field" data-luna-field="title"><span><strong>Title</strong>{taskModalSuggestion.title}</span><button class="text-button luna-apply-button" class:applied={taskModalTitleApplied} type="button" disabled={taskModalTitleApplied} aria-label={taskModalTitleApplied ? 'Title suggestion applied' : 'Apply title suggestion'} on:click={() => applyLunaField('title')}>{taskModalTitleApplied ? '✓ Applied' : 'Apply'}</button></div>
                  <div class="luna-preview-field" data-luna-field="description"><span><strong>Description &amp; acceptance criteria</strong>{taskModalSuggestion.description}<small>{taskModalSuggestion.acceptance_criteria.length} measurable criteria</small></span><button class="text-button luna-apply-button" class:applied={taskModalDescriptionApplied} type="button" disabled={taskModalDescriptionApplied} aria-label={taskModalDescriptionApplied ? 'Description suggestion applied' : 'Apply description suggestion'} on:click={() => applyLunaField('description')}>{taskModalDescriptionApplied ? '✓ Applied' : 'Apply'}</button></div>
                  <div class="luna-preview-field" data-luna-field="priority"><span><strong>Priority</strong><span class={`priority-pill priority-${taskModalSuggestion.priority}`}>{priorityLabels[taskModalSuggestion.priority]}</span><small>{taskModalSuggestion.rationale}</small></span><button class="text-button luna-apply-button" class:applied={taskModalPriorityApplied} type="button" disabled={taskModalPriorityApplied} aria-label={taskModalPriorityApplied ? 'Priority suggestion applied' : 'Apply priority suggestion'} on:click={() => applyLunaField('priority')}>{taskModalPriorityApplied ? '✓ Applied' : 'Apply'}</button></div>
                {#if taskModalSuggestion.supporting_task_keys.length}<div class="luna-sources"><strong>Related tasks</strong>{#each taskModalSuggestion.supporting_task_keys as key}<a href={taskDeepLink(taskModalProject?.slug || activeProjectSlug, key)} on:click={closeTaskModal}>{key}</a>{/each}</div>{/if}
                </div>
              </div>
            {/if}
          </section>
          <section class="task-details-fields" aria-labelledby="task-details-heading"><div class="task-details-heading"><div><span class="eyebrow">Task details</span><h3 id="task-details-heading">Shape the work</h3></div><span>Review before creating</span></div>
            <label>Task title<input bind:value={taskModalTitle} placeholder="What should move forward?" /></label>
            <label>Description <span class="optional">Optional · Markdown supported</span><textarea rows="3" bind:value={taskModalDescription} placeholder="Add the context your future self will need."></textarea></label>
            <div class="form-row"><label>Priority<select bind:value={taskModalPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label><label>Due date <span class="optional">Optional</span><input type="date" bind:value={taskModalDueDate} /></label></div>
            <label>Assignee <span class="optional">Optional</span><input bind:value={taskModalAssignee} placeholder="Actor ID" /></label>
          </section>
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

    {#if portablePreview}
      <div class="modal-backdrop" role="presentation" on:click={closePortablePreview}></div>
      <div class="modal portable-preview-modal" role="dialog" aria-modal="true" aria-labelledby="portable-preview-title" use:focusTrap>
        <div class="modal-header"><div><span class="eyebrow">Portable archive</span><h2 id="portable-preview-title">Review import</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closePortablePreview}>×</button></div>
        <p><strong>{portablePreview.fileName}</strong> will be merged into {activeProject?.name || 'the current project'}.</p>
        <div class="portable-import-summary"><strong>{portablePreview.report.counts.tasks_created || 0}</strong><span>tasks to create</span><strong>{portablePreview.report.remaps.length}</strong><span>reported remaps</span><strong>{portablePreview.report.warnings.length}</strong><span>warnings</span><strong>{portablePreview.report.errors.length}</strong><span>validation failures</span></div>
        {#if portablePreviewError}<div class="inline-alert error" role="alert"><span>!</span><span>{portablePreviewError}</span></div>{/if}
        {#if portablePreview.report.errors.length}<div class="portable-import-errors" role="alert"><strong>Validation failures</strong><ul>{#each portablePreview.report.errors as issue}<li><code>{issue.entity}{issue.id ? ` · ${issue.id}` : ''}{issue.field ? ` · ${issue.field}` : ''}</code><span>{issue.message}</span></li>{/each}</ul></div>{/if}
        {#if portablePreview.report.remaps.length}<div class="portable-import-remaps"><strong>Remappings</strong><ul>{#each portablePreview.report.remaps as remap}<li><code>{remap.entity}{remap.field ? ` · ${remap.field}` : ''}: {remap.source} → {remap.target}</code><span>{remap.reason}</span></li>{/each}</ul></div>{/if}
        {#if portablePreview.report.warnings.length}<div class="portable-import-warnings"><strong>Review warnings</strong><ul>{#each portablePreview.report.warnings as warning}<li>{warning}</li>{/each}</ul></div>{/if}
        <div class="modal-actions"><button class="text-button" type="button" disabled={portableBusy} on:click={closePortablePreview}>Cancel</button><button class="button primary" type="button" disabled={portableBusy || portablePreview.report.errors.length > 0} on:click={() => void confirmPortableImport()}>{#if portableBusy}<span class="button-spinner"></span>{/if}Import archive</button></div>
      </div>
    {/if}

    {#if showProjectModal}
      <div class="modal-backdrop" role="presentation" on:click={closeProjectModal}></div>
      <div class="modal project-modal" role="dialog" aria-modal="true" aria-labelledby="project-modal-title" use:focusTrap><div class="modal-header"><div><span class="eyebrow">New workspace</span><h2 id="project-modal-title">Create a project</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeProjectModal}>×</button></div>{#if projectFormError}<div class="inline-alert error" role="alert"><span>!</span>{projectFormError}</div>{/if}<form on:submit|preventDefault={createProject}><div class="project-form-title"><span class="project-dot huge" style={`--project-color: ${projectColorDraft}`}>{projectInitials({ name: projectNameDraft || 'New project', key: projectKeyDraft || 'NP' })}</span><div><label>Project name<input data-dialog-initial-focus bind:value={projectNameDraft} placeholder="Product launch" /></label></div></div><div class="form-row"><label>Project key<input maxlength="16" bind:value={projectKeyDraft} placeholder="PROD" /></label><label>Accent color<input class="color-input" type="color" bind:value={projectColorDraft} /></label></div><label>Description <span class="optional">Optional</span><textarea rows="3" bind:value={projectDescriptionDraft} placeholder="A short note about what this project is for."></textarea><span class="field-hint">Helm will add Backlog, Ready, In progress, Blocked, and Done columns automatically.</span></label><div class="modal-actions"><button class="text-button" type="button" on:click={closeProjectModal}>Cancel</button><button class="button primary" type="submit" disabled={projectCreating || !projectNameDraft.trim() || !projectKeyDraft.trim()}>{#if projectCreating}<span class="button-spinner"></span>{/if}Create project</button></div></form></div>
    {/if}

    {#if confirmRequest}
      <ConfirmDialog
        title={confirmRequest.title}
        message={confirmRequest.message}
        confirmLabel={confirmRequest.confirmLabel}
        onConfirm={() => settleConfirm(true)}
        onCancel={() => settleConfirm(false)}
      />
    {/if}

    {#if adminConfirmation}
      <div class="modal-backdrop" role="presentation" on:click={cancelAdminConfirmation}></div>
      <div class="modal admin-confirm-modal" role="alertdialog" aria-modal="true" aria-labelledby="admin-confirm-title" aria-describedby="admin-confirm-message" use:focusTrap>
        <div class="modal-header"><div><span class="eyebrow">Confirm administration change</span><h2 id="admin-confirm-title">{adminConfirmation.title}</h2></div><button class="icon-button" type="button" aria-label="Close confirmation" on:click={cancelAdminConfirmation}>×</button></div>
        <p id="admin-confirm-message" class="admin-confirm-message">{adminConfirmation.message}</p>
        <div class="modal-actions"><button class="text-button" type="button" disabled={adminConfirmationBusy} on:click={cancelAdminConfirmation}>Cancel</button><button class="button primary" data-dialog-initial-focus type="button" disabled={adminConfirmationBusy} on:click={runAdminConfirmation}>{#if adminConfirmationBusy}<span class="button-spinner"></span>{/if}{adminConfirmation.confirmLabel}</button></div>
      </div>
    {/if}

    {#if revealedToken}
      <div class="modal-backdrop" role="presentation"></div>
      <div class="modal token-reveal-modal" role="alertdialog" aria-modal="true" aria-labelledby="token-reveal-title" use:focusTrap><div class="token-reveal-icon">✓</div><span class="eyebrow">One-time secret</span><h2 id="token-reveal-title">Copy your token now</h2><p>For your security, the token will not be shown again after closing this dialog.</p><div class="token-value"><code>{revealedToken.plaintext || revealedToken.token || ''}</code><button class="icon-button" type="button" aria-label="Copy token" on:click={() => void copyRevealedToken()}>⧉</button></div><button class="button primary button-large" type="button" data-dialog-initial-focus on:click={closeTokenReveal}>I’ve copied it</button></div>
    {/if}

    <div class="sr-only" aria-live="polite" aria-atomic="true">{liveAnnouncement}</div>
    <div class="toast-stack" aria-label="Notifications">{#each toasts as item (item.id)}<div class={`toast ${item.kind}`} role={item.role} aria-live={item.live} aria-atomic="true"><span aria-hidden="true">{item.kind === 'success' ? '✓' : item.kind === 'error' ? '!' : 'i'}</span><span class="toast-message">{item.message}</span>{#if item.action}<button class="toast-action" type="button" disabled={item.action.pending} on:click={() => void runToastAction(item)}>{item.action.pending ? 'Working…' : item.action.label}</button>{/if}<button class="icon-button tiny" type="button" aria-label="Dismiss notification" on:click={() => toasts = toasts.filter((toastItem) => toastItem.id !== item.id)}>×</button></div>{/each}</div>
  </div>
{/if}
