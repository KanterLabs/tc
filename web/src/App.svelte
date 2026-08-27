<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { api, listAllTasks, unwrapActor } from './lib/api';
  import {
    actorId,
    actorName,
    dateToIso,
    displayEvent,
    filterTasks,
    formatDate,
    formatRelative,
    isDueSoon,
    isOverdue,
    loadRecentProjects,
    moveTaskLocal,
    nextPosition,
    projectInitials,
    rememberProject,
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
    type Column,
    type Comment,
    type Label,
    type Project,
    type RoadmapSummary,
    type Task,
    type Priority
  } from './lib/types';

  type View = 'board' | 'my-work' | 'roadmap' | 'settings';
  type AuthView = 'login' | 'setup';
  type ToastKind = 'success' | 'error' | 'info';
  type CommandChoice = {
    kind: 'project' | 'view';
    id: string;
    label: string;
    hint: string;
    project?: Project;
    view?: View;
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
  const labelPalette = ['#6d5efc', '#2ea879', '#d49534', '#dc626f', '#4b9cf5'];
  const scopeOptions = ['projects:read', 'projects:write', 'tasks:read', 'tasks:write', 'tasks:claim', 'events:read'];

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

  let theme: 'light' | 'dark' = 'light';
  let view: View = 'board';
  let projects: Project[] = [];
  let activeProjectSlug = '';
  let recentProjectIds: string[] = [];
  let projectsLoading = false;
  let projectsError = '';
  let activeProject: Project | undefined;
  let columns: Column[] = [];
  let tasks: Task[] = [];
  let labels: Label[] = [];
  let boardLoading = false;
  let boardError = '';
  let filters: BoardFilters = { query: '', priority: 'all', label: 'all', assignee: 'all', state: 'all' };
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
  let dialogReturnFocus: { element: HTMLElement | null; fallbackSelector: string } | null = null;

  let drawerTask: Task | null = null;
  let drawerLoading = false;
  let drawerSaving = false;
  let drawerError = '';
  let comments: Comment[] = [];
  let commentBody = '';
  let commentSending = false;
  let draftTitle = '';
  let draftDescription = '';
  let draftPriority: Priority = 'normal';
  let draftDueDate = '';
  let draftAssignee = '';
  let draftLabels = '';

  let draggingTaskId = '';
  let quickAddColumn = '';
  let quickAddTitle: Record<string, string> = {};
  let taskActionLoading = '';
  let labelDeleting = '';

  let myWorkTasks: Task[] = [];
  let myWorkLoading = false;
  let myWorkError = '';
  let roadmap: RoadmapSummary | null = null;
  let roadmapProjectId: string | undefined;
  let roadmapLoading = false;
  let roadmapError = '';

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

  let events: ActivityEvent[] = [];
  let eventsCursor: number | undefined;
  let pollTimer: number | undefined;
  let toastSequence = 0;
  let toasts: { id: number; kind: ToastKind; message: string }[] = [];

  $: activeProject = projects.find((project) => project.slug === activeProjectSlug);
  $: visibleTasks = filterTasks(tasks, columns, filters);
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
  $: drawerActivity = drawerTask
    ? events.filter((event) => event.task_id === drawerTask?.id).sort((a, b) => b.cursor - a.cursor)
    : [];
  $: roadmapTotal = roadmap?.task_total ?? roadmap?.total_tasks ?? 0;
  $: roadmapCompletion = Math.max(0, Math.min(100, roadmap?.completion_percentage ?? roadmap?.completion_percent ?? 0));
  $: roadmapCompleted = roadmap?.completed_count ?? roadmap?.completed ?? 0;
  $: roadmapOverdue = roadmap?.overdue_count ?? roadmap?.overdue ?? 0;
  $: roadmapDueSoon = roadmap?.due_soon_count ?? roadmap?.due_soon ?? 0;
  $: roadmapUpcoming = roadmap?.upcoming_tasks ?? roadmap?.upcoming ?? [];
  $: roadmapProject = projects.find((project) => project.id === roadmapProjectId);
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

  onMount(() => {
    theme = (localStorage.getItem('roadmap.theme') as 'light' | 'dark' | null) || 'light';
    applyTheme();
    recentProjectIds = loadRecentProjects(localStorage);
    const cleanup = () => {
      if (pollTimer) window.clearInterval(pollTimer);
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
    try {
      authStatus = await api.authStatus();
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
      authError = friendlyError(error, 'Roadmap could not connect to the server.');
    } finally {
      booting = false;
    }
  }

  async function finishAuthentication() {
    await loadProjects();
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
    try {
      await api.authLogout();
    } catch {
      // Clearing the local session is still the least surprising UI result.
    }
    user = null;
    projectListRequest += 1;
    boardRequest += 1;
    roadmapRequest += 1;
    taskModalColumnsRequest += 1;
    if (drawerTask) closeDrawer();
    activeProjectSlug = '';
    roadmapProjectId = undefined;
    projects = [];
    columns = [];
    tasks = [];
    events = [];
    if (pollTimer) window.clearInterval(pollTimer);
    pollTimer = undefined;
  }

  async function loadProjects() {
    const requestId = ++projectListRequest;
    const selectionVersion = projectSwitchVersion;
    projectsLoading = true;
    projectsError = '';
    try {
      const result = await api.listAllProjects();
      if (requestId !== projectListRequest) return;
      const nextProjects = result.data.filter((project) => !project.archived_at);
      projects = nextProjects;
      if (selectionVersion !== projectSwitchVersion) return;
      const routeSlug = getProjectSlugFromLocation();
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
      if (requestId !== projectListRequest || selectionVersion !== projectSwitchVersion) return;
      const path = window.location.pathname;
      if (path === '/my-work') {
        roadmapProjectId = undefined;
        view = 'my-work';
        await loadMyWork();
      } else if (path === '/roadmap') {
        roadmapProjectId = undefined;
        view = 'roadmap';
        await loadRoadmap();
      } else if (path === '/settings') {
        roadmapProjectId = undefined;
        view = 'settings';
        await loadAgents();
      } else if (routeSlug && path.endsWith('/roadmap')) {
        roadmapProjectId = target?.id;
        view = 'roadmap';
        await loadRoadmap(roadmapProjectId);
      }
    } catch (error) {
      if (requestId === projectListRequest) projectsError = friendlyError(error, 'Projects could not be loaded.');
    } finally {
      if (requestId === projectListRequest) projectsLoading = false;
    }
  }

  async function loadBoard() {
    const requestId = ++boardRequest;
    const requestedSlug = activeProjectSlug;
    if (!requestedSlug) {
      boardLoading = false;
      return;
    }
    const project = projects.find((item) => item.slug === requestedSlug);
    if (!project) {
      boardLoading = false;
      return;
    }
    boardLoading = true;
    boardError = '';
    try {
      const [columnResult, taskResult, labelResult] = await Promise.all([
        api.listAllColumns(project.id),
        listAllTasks(project.id, { limit: 200 }),
        api.listAllLabels(project.id)
      ]);
      if (requestId !== boardRequest || activeProjectSlug !== requestedSlug) return;
      columns = columnResult.data;
      tasks = taskResult.data;
      labels = labelResult.data;
    } catch (error) {
      if (requestId === boardRequest && activeProjectSlug === requestedSlug) {
        boardError = friendlyError(error, 'This board could not be loaded.');
      }
    } finally {
      if (requestId === boardRequest) boardLoading = false;
    }
  }

  async function loadMyWork() {
    myWorkLoading = true;
    myWorkError = '';
    try {
      myWorkTasks = (await api.allMyWork()).data;
    } catch (error) {
      myWorkError = friendlyError(error, 'Assigned work could not be loaded.');
    } finally {
      myWorkLoading = false;
    }
  }

  async function loadRoadmap(projectId = roadmapProjectId) {
    const requestId = ++roadmapRequest;
    const requestedProjectId = projectId;
    roadmapLoading = true;
    roadmapError = '';
    try {
      const result = await api.roadmap(projectId);
      if (requestId !== roadmapRequest || view !== 'roadmap' || roadmapProjectId !== requestedProjectId) return;
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
        if (requestId !== roadmapRequest || view !== 'roadmap' || roadmapProjectId !== requestedProjectId) return;
        projects = projects.map((project) => {
          const row = summaries.find((summary) => summary?.projectId === project.id)?.summary;
          return row
            ? { ...project, task_count: row.task_total ?? row.total_tasks ?? project.task_count, completed_task_count: row.completed ?? row.completed_count ?? project.completed_task_count }
            : project;
        });
      }
    } catch (error) {
      if (requestId === roadmapRequest && view === 'roadmap' && roadmapProjectId === requestedProjectId) {
        roadmapError = friendlyError(error, 'Roadmap progress could not be loaded.');
      }
    } finally {
      if (requestId === roadmapRequest) roadmapLoading = false;
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
    pollTimer = window.setInterval(() => void pollEvents(), 15000);
    void pollEvents();
  }

  async function pollEvents() {
    if (!user) return;
    try {
      const result = await api.listEvents({ after: eventsCursor });
      if (!result.data.length) return;
      events = [...result.data, ...events].sort((a, b) => b.cursor - a.cursor).slice(0, 100);
      eventsCursor = Math.max(...result.data.map((event) => event.cursor));
      const projectChanged = result.data.some((event) => !event.project_id || event.project_id === activeProject?.id);
      if (projectChanged && view === 'board' && !drawerSaving) await loadBoard();
      if (projectChanged && view === 'roadmap') await loadRoadmap();
    } catch {
      // Polling is best effort; the visible board remains usable during a blip.
    }
  }

  function getProjectSlugFromLocation(): string {
    const match = window.location.pathname.match(/^\/p\/([^/]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  function isProjectRoadmapLocation(): boolean {
    return /^\/p\/[^/]+\/roadmap\/?$/.test(window.location.pathname);
  }

  function navigate(path: string, replace = false) {
    if (window.location.pathname !== path) {
      window.history[replace ? 'replaceState' : 'pushState']({}, '', path);
    }
  }

  async function selectProject(project: Project, push = true) {
    projectSwitchVersion += 1;
    if (drawerTask) closeDrawer();
    activeProjectSlug = project.slug;
    roadmapProjectId = undefined;
    columns = [];
    tasks = [];
    labels = [];
    boardError = '';
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
    } else if (next === 'roadmap') {
      roadmapProjectId = undefined;
      if (push) navigate('/roadmap');
      await loadRoadmap();
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
    if (slug) {
      const project = projects.find((item) => item.slug === slug);
      if (project && isProjectRoadmapLocation()) {
        projectSwitchVersion += 1;
        activeProjectSlug = project.slug;
        roadmapProjectId = project.id;
        view = 'roadmap';
        void loadRoadmap(project.id);
      } else if (project) void selectProject(project, false);
      return;
    }
    if (window.location.pathname === '/my-work') void setView('my-work', false);
    else if (window.location.pathname === '/roadmap') void setView('roadmap', false);
    else if (window.location.pathname === '/settings') void setView('settings', false);
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
      else if (revealedToken) closeTokenReveal();
      else if (drawerTask) closeDrawer();
    }
  }

  function buildCommandChoices(query: string): CommandChoice[] {
    const normalized = query.trim().toLowerCase();
    const choices: CommandChoice[] = [
      { kind: 'view', id: 'my-work', view: 'my-work', label: 'My work', hint: 'Assigned and claimed tasks' },
      { kind: 'view', id: 'roadmap', view: 'roadmap', label: 'Roadmap overview', hint: 'Progress across every project' },
      { kind: 'view', id: 'settings', view: 'settings', label: 'Settings', hint: 'Agents, tokens, and appearance' },
      ...projects.map((project) => ({ kind: 'project' as const, id: project.id, project, label: project.name, hint: project.key }))
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
    else if (choice.view) await setView(choice.view);
  }

  async function openProjectRoadmap() {
    if (!activeProject) return;
    roadmapProjectId = activeProject.id;
    view = 'roadmap';
    navigate(`/p/${encodeURIComponent(activeProject.slug)}/roadmap`);
    await loadRoadmap(activeProject.id);
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
      if (taskModalProjectId === activeProject?.id) tasks = [...tasks, created];
      closeTaskModal();
      toast('success', `${created.key} created in ${taskModalProject?.name || 'your project'}.`);
    } catch (error) {
      taskModalError = friendlyError(error, 'The task could not be created.');
    } finally {
      taskModalCreating = false;
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
      replaceTask(updated);
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

  function replaceTask(updated: Task) {
    tasks = tasks.some((task) => task.id === updated.id) ? tasks.map((task) => (task.id === updated.id ? updated : task)) : [updated, ...tasks];
    if (drawerTask?.id === updated.id) {
      drawerTask = updated;
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

  async function openTask(task: Task) {
    const requestId = ++taskDetailRequest;
    rememberDialogFocus('[data-task-trigger], .work-row');
    drawerTask = task;
    drawerError = '';
    comments = [];
    commentBody = '';
    syncDraft(task);
    drawerLoading = true;
    try {
      const [detail, commentResult] = await Promise.all([api.getTask(task.id), api.listAllComments(task.id)]);
      if (requestId !== taskDetailRequest || drawerTask?.id !== task.id) return;
      replaceTask(detail);
      syncDraft(detail);
      comments = commentResult.data;
    } catch (error) {
      if (requestId === taskDetailRequest && drawerTask?.id === task.id) {
        drawerError = friendlyError(error, 'Some task details could not be loaded.');
      }
    } finally {
      if (requestId === taskDetailRequest && drawerTask?.id === task.id) drawerLoading = false;
    }
  }

  function syncDraft(task: Task) {
    draftTitle = task.title;
    draftDescription = task.description || '';
    draftPriority = task.priority;
    draftDueDate = toInputDate(task.due_at);
    draftAssignee = actorId(task.assignee);
    draftLabels = (task.labels || []).map((label) => label.name).join(', ');
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
    taskDetailRequest += 1;
    drawerTask = null;
    drawerError = '';
    comments = [];
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
          label_ids: labelIds
        },
        drawerTask.version
      );
      replaceTask(updated);
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
      tasks = tasks.filter((item) => item.id !== task.id);
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

  async function runTaskAction(action: 'claim' | 'renew' | 'release' | 'complete' | 'block') {
    if (!drawerTask) return;
    taskActionLoading = drawerTask.id;
    try {
      let updated: Task;
      if (action === 'claim') updated = await api.claimTask(drawerTask.id, drawerTask.version);
      else if (action === 'renew') updated = await api.renewTask(drawerTask.id, drawerTask.version);
      else if (action === 'release') updated = await api.releaseTask(drawerTask.id, drawerTask.version);
      else if (action === 'complete') updated = await api.completeTask(drawerTask.id, drawerTask.version);
      else updated = await api.blockTask(drawerTask.id, drawerTask.version);
      replaceTask(updated);
      toast('success', `${updated.key} ${action === 'renew' ? 'claim renewed' : `${action}d`}.`);
    } catch (error) {
      toast('error', friendlyError(error, 'That task action could not be completed.'));
    } finally {
      taskActionLoading = '';
    }
  }

  async function postComment() {
    if (!drawerTask || !commentBody.trim()) return;
    commentSending = true;
    try {
      const comment = await api.postComment(drawerTask.id, commentBody.trim());
      comments = [...comments, comment];
      commentBody = '';
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

  function taskDueClass(task: Task): string {
    if (isOverdue(task.due_at)) return 'overdue';
    if (isDueSoon(task.due_at)) return 'due-soon';
    return '';
  }

  function authorName(value: Comment['author'] | Comment['actor'] | ActivityEvent['actor'] | string | null | undefined): string {
    return typeof value === 'string' ? value : value?.name || 'Unknown actor';
  }

  function commentAuthor(comment: Comment): string {
    return authorName(comment.author || comment.actor || comment.actor_id);
  }

  function eventAuthor(event: ActivityEvent): string {
    return authorName(event.actor || event.actor_id);
  }

  function claimAction(task: Task | null): 'claim' | 'renew' {
    return task?.claimed_by && actorId(task.claimed_by) === user?.id ? 'renew' : 'claim';
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
        <button class="text-button auth-switch" type="button" on:click={() => { authView = authView === 'setup' ? 'login' : 'setup'; authError = ''; }}>
          {authView === 'setup' ? 'Already have an account? Sign in' : 'First time here? Set up your workspace'}
        </button>
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
        <button class:active={view === 'board'} type="button" aria-label="Board" aria-current={view === 'board' ? 'page' : undefined} on:click={() => setView('board')}><span class="mobile-nav-icon" aria-hidden="true">▦</span><span>Board</span></button>
        <button class:active={view === 'my-work'} type="button" aria-label="My work" aria-current={view === 'my-work' ? 'page' : undefined} on:click={() => setView('my-work')}><span class="mobile-nav-icon" aria-hidden="true">◌</span><span>My Work</span></button>
        <button class:active={view === 'roadmap'} type="button" aria-label="Roadmap" aria-current={view === 'roadmap' ? 'page' : undefined} on:click={() => setView('roadmap')}><span class="mobile-nav-icon" aria-hidden="true">◒</span><span>Roadmap</span></button>
        <button class:active={view === 'settings'} type="button" aria-label="Settings" aria-current={view === 'settings' ? 'page' : undefined} on:click={() => setView('settings')}><span class="mobile-nav-icon" aria-hidden="true">⚙</span><span>Settings</span></button>
      </nav>

      <main class="content">
        {#if view === 'board'}
          {#if activeProject}
            <section class="page-heading board-heading">
              <div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{activeProject.key}</span></div><div class="heading-title-row"><span class="heading-project-dot" style={`--project-color: ${activeProject.color || '#6d5efc'}`}></span><h1>{activeProject.name}</h1><button class="icon-button favorite-heading" class:starred={activeProject.favorite} type="button" aria-label={activeProject.favorite ? 'Remove from favorites' : 'Add to favorites'} on:click={(event) => toggleFavorite(event, activeProject)}>{activeProject.favorite ? '★' : '☆'}</button></div><p>{activeProject.description || 'A focused space for turning ideas into shipped work.'}</p></div>
              <div class="heading-actions"><button class="button quiet-button" type="button" on:click={openProjectRoadmap}><span aria-hidden="true">◒</span> Progress</button><button class="button primary" type="button" data-task-modal-trigger on:click={openTaskModal}><span aria-hidden="true">＋</span> New task</button></div>
            </section>

            <section class="board-toolbar" aria-label="Board filters">
              <div class="filter-search"><span aria-hidden="true">⌕</span><input aria-label="Search tasks" bind:value={filters.query} placeholder="Search tasks…" /><kbd>/</kbd></div>
              <div class="filter-group"><select aria-label="Filter by state" bind:value={filters.state}><option value="all">All states</option>{#each sortedColumns as column}<option value={column.semantic_state}>{stateLabels[column.semantic_state] || column.name}</option>{/each}</select><select aria-label="Filter by priority" bind:value={filters.priority}><option value="all">All priorities</option><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select><select aria-label="Filter by label" bind:value={filters.label}><option value="all">All labels</option>{#each labels as label}<option value={label.id}>{label.name}</option>{/each}</select><select aria-label="Filter by assignee" bind:value={filters.assignee}><option value="all">All assignees</option>{#each Array.from(new Map(tasks.map((task) => [actorId(task.assignee), task.assignee])).entries()).filter(([id]) => id) as pair}<option value={pair[0]}>{actorName(pair[1]) || pair[0]}</option>{/each}</select></div>
              {#if filters.query || filters.priority !== 'all' || filters.label !== 'all' || filters.assignee !== 'all' || filters.state !== 'all'}<button class="clear-filters" type="button" on:click={clearFilters}>Clear filters</button>{/if}
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
                    <div class="column-cards" aria-live="polite">
                      {#if !tasksByColumn[column.id].length}
                        <div class="column-empty"><span>Nothing here yet</span><button class="text-button" type="button" on:click={() => quickAddColumn = column.id}>Add the first task</button></div>
                      {:else}
                        {#each tasksByColumn[column.id] as task (task.id)}
                          <article class="task-card" class:dragging={draggingTaskId === task.id} draggable="true" on:dragstart={(event) => dragStart(event, task)} on:dragend={() => draggingTaskId = ''}>
                            <button class="task-main" type="button" data-task-trigger on:click={() => openTask(task)} on:keydown={(event) => keyboardMove(event, task)}>
                              <span class="task-card-top"><span class="task-key">{task.key}</span><span class={`priority-dot priority-${task.priority}`} title={`${priorityLabels[task.priority]} priority`}></span>{#if task.claimed_by}<span class="claim-mini" title={`Claimed by ${actorName(task.claimed_by) || 'another actor'}`}>●</span>{/if}</span>
                              <strong class="task-title">{task.title}</strong>
                              {#if task.description}<span class="task-excerpt">{task.description.replace(/[#*_`]/g, '').slice(0, 92)}{task.description.length > 92 ? '…' : ''}</span>{/if}
                              {#if task.labels?.length}<span class="task-labels">{#each task.labels.slice(0, 3) as label}<span class="label-chip" style={`--label-color: ${label.color || '#8b7cf6'}`}>{label.name}</span>{/each}{#if task.labels.length > 3}<span class="label-more">+{task.labels.length - 3}</span>{/if}</span>{/if}
                            </button>
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
            <div class="empty-state welcome-state"><div class="welcome-orbit"><span>R</span></div><span class="eyebrow">Your workspace is ready</span><h1>Start with a project.</h1><p>Projects give your ideas a home. Create one, invite your agents, and keep the next step clear.</p><button class="button primary button-large" type="button" on:click={openProjectModal}>＋ Create your first project</button></div>
          {/if}
        {:else if view === 'my-work'}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Personal</span></div><h1>My work</h1><p>Everything assigned or claimed by you, across projects.</p></div><div class="heading-actions"><button class="button quiet-button" type="button" on:click={loadMyWork}>↻ Refresh</button></div></section>
          {#if myWorkError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{myWorkError}<button class="text-button" type="button" on:click={loadMyWork}>Retry</button></div>{/if}
          {#if myWorkLoading}<div class="list-skeleton">{#each [1, 2, 3] as item}<div></div>{/each}</div>{:else if !myWorkTasks.length}<div class="empty-state"><div class="empty-icon">◌</div><h2>No work assigned yet</h2><p>Tasks claimed or assigned to you will show up here.</p><button class="button primary" type="button" on:click={() => activeProject && setView('board')}>Browse the board</button></div>{:else}<section class="work-list">{#each myWorkTasks as task (task.id)}<button class="work-row" type="button" on:click={() => openWorkTask(task)}><span class="work-project-dot" style={`--project-color: ${projectForTask(task)?.color || '#6d5efc'}`}></span><span class="work-main"><span class="work-row-top"><span class="task-key">{task.key}</span><span class={`priority-pill priority-${task.priority}`}>{priorityLabels[task.priority]}</span></span><strong>{task.title}</strong><span class="work-project-name">{projectForTask(task)?.name || 'Project'}</span></span><span class="work-column">{columns.find((column) => column.id === task.column_id)?.name || stateLabels[columns.find((column) => column.id === task.column_id)?.semantic_state || ''] || 'In progress'}</span><span class={`work-due ${taskDueClass(task)}`}>{task.due_at ? `${isOverdue(task.due_at) ? 'Overdue · ' : ''}${formatDate(task.due_at)}` : 'No due date'}</span><span class="row-arrow">→</span></button>{/each}</section>{/if}
        {:else if view === 'roadmap'}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{roadmapProject ? roadmapProject.key : 'Overview'}</span></div><h1>{roadmapProject ? `${roadmapProject.name} progress` : 'Roadmap overview'}</h1><p>{roadmapProject ? 'A focused view of delivery, deadlines, and recent activity for this project.' : 'A high-level pulse on every project and what needs attention next.'}</p></div><div class="heading-actions">{#if roadmapProject}<button class="button quiet-button" type="button" on:click={() => setView('roadmap')}>All projects</button>{/if}<button class="button quiet-button" type="button" on:click={() => loadRoadmap(roadmapProjectId)}>↻ Refresh</button></div></section>
          {#if roadmapError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{roadmapError}<button class="text-button" type="button" on:click={() => loadRoadmap(roadmapProjectId)}>Retry</button></div>{/if}
          {#if roadmapLoading}<div class="roadmap-skeleton"><div></div><div></div><div></div></div>{:else}<section class="roadmap-content"><div class="roadmap-hero"><div class="hero-copy"><span class="eyebrow">Workspace pulse</span><h2>Momentum, at a glance.</h2><p>Progress is calculated from each project's semantic board state.</p></div><div class="hero-progress"><div class="progress-ring" style={`--progress: ${roadmapCompletion}%`}><span>{Math.round(roadmapCompletion)}<small>%</small></span></div><div><strong>{roadmapTotal} total tasks</strong><span>{roadmap?.completed_count ?? Math.round(roadmapTotal * roadmapCompletion / 100)} completed</span></div></div></div><div class="metric-grid"><div class="metric-card"><span class="metric-icon purple">◒</span><span class="metric-label">Completion</span><strong>{Math.round(roadmapCompletion)}%</strong><span class="metric-note">Across all projects</span></div><div class="metric-card"><span class="metric-icon red">!</span><span class="metric-label">Overdue</span><strong>{roadmap?.overdue_count ?? 0}</strong><span class="metric-note">Need attention</span></div><div class="metric-card"><span class="metric-icon amber">◷</span><span class="metric-label">Due soon</span><strong>{roadmap?.due_soon_count ?? 0}</strong><span class="metric-note">Next 7 days</span></div><div class="metric-card"><span class="metric-icon green">✓</span><span class="metric-label">Completed</span><strong>{roadmap?.completed_count ?? 0}</strong><span class="metric-note">Shipped so far</span></div></div><div class="roadmap-columns"><section class="roadmap-panel project-progress-panel"><div class="panel-heading"><div><h2>Project progress</h2><p>Where each project stands today.</p></div><button class="icon-button" type="button" aria-label="Refresh progress" on:click={() => loadRoadmap(roadmapProjectId)}>↻</button></div>{#if roadmapProjectRows.length}{#each roadmapProjectRows as row}<button class="project-progress-row" type="button" on:click={() => selectProject(row.project)}><span class="project-dot" style={`--project-color: ${row.project.color || '#6d5efc'}`}>{projectInitials(row.project)}</span><span class="project-progress-name"><strong>{row.project.name}</strong><small>{row.project.key}</small></span><span class="progress-track"><span style={`width: ${row.total_tasks ? (row.completed_tasks / row.total_tasks) * 100 : 0}%; --project-color: ${row.project.color || '#6d5efc'}`}></span></span><span class="progress-number">{row.total_tasks ? Math.round((row.completed_tasks / row.total_tasks) * 100) : 0}%</span><span>→</span></button>{/each}{:else}<div class="panel-empty">Create a project to see progress here.</div>{/if}</section><section class="roadmap-panel upcoming-panel"><div class="panel-heading"><div><h2>Coming up</h2><p>Tasks with the nearest due dates.</p></div></div>{#if roadmap?.upcoming_tasks?.length}{#each roadmap.upcoming_tasks.slice(0, 5) as task}<button class="upcoming-row" type="button" on:click={() => openWorkTask(task)}><span class="upcoming-key">{task.key}</span><span class="upcoming-title">{task.title}</span><span class={`upcoming-date ${taskDueClass(task)}`}>{formatDate(task.due_at)}</span></button>{/each}{:else}<div class="panel-empty">No upcoming deadlines. Nice breathing room.</div>{/if}</section></div></section>{/if}
        {:else}
          <section class="page-heading"><div><div class="breadcrumbs"><span>Workspace</span><span>/</span><span>Preferences</span></div><h1>Settings</h1><p>Manage the agents and tokens that help your workspace move.</p></div></section>
          {#if agentsError}<div class="inline-alert error content-alert" role="alert"><span>!</span>{agentsError}<button class="text-button" type="button" on:click={loadAgents}>Retry</button></div>{/if}
          <section class="settings-layout"><div class="settings-main"><div class="settings-section"><div class="settings-section-heading"><div><span class="eyebrow">Coordination</span><h2>Agents &amp; tokens</h2><p>Give software agents scoped access without sharing a human login.</p></div><button class="button primary" type="button" on:click={() => showAgentForm = !showAgentForm}>＋ Add agent</button></div>{#if showAgentForm}<div class="settings-form"><label>Agent name<input bind:value={agentNameDraft} placeholder="Release assistant" /></label><label>Description <span class="optional">Optional</span><textarea rows="2" bind:value={agentDescriptionDraft} placeholder="What is this agent responsible for?"></textarea></label><div class="form-actions"><button class="text-button" type="button" on:click={() => showAgentForm = false}>Cancel</button><button class="button primary" type="button" disabled={!agentNameDraft.trim()} on:click={createAgent}>Create agent</button></div></div>{/if}{#if agentsLoading}<div class="list-skeleton">{#each [1, 2] as item}<div></div>{/each}</div>{:else if !agents.length}<div class="empty-state compact-empty"><div class="empty-icon">✦</div><h3>No agents yet</h3><p>Create a scoped identity for the tools that collaborate with you.</p><button class="button quiet-button" type="button" on:click={() => showAgentForm = true}>Create your first agent</button></div>{:else}<div class="agent-list">{#each agents as agent}<article class="agent-card"><div class="agent-card-header"><span class="agent-avatar">✦</span><div><h3>{agent.name}</h3><p>{agent.description || 'No description'}</p></div><button class="button quiet-button compact-button" type="button" data-token-trigger on:click={() => { selectedAgentId = agent.id; showTokenForm = selectedAgentId === agent.id && !showTokenForm; }}>＋ Token</button></div>{#if agent.tokens?.length}<div class="token-list">{#each agent.tokens as token}<div class="token-row"><span class="token-icon">⌘</span><span class="token-info"><strong>{token.name}</strong><small>{token.scopes.join(' · ')}</small></span><span class="token-date">{token.expires_at ? `Expires ${formatDate(token.expires_at)}` : 'No expiry'}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Revoke ${token.name}`} on:click={() => deleteToken(token)}>×</button></div>{/each}</div>{:else}<div class="agent-no-tokens">No active tokens</div>{/if}{#if showTokenForm && selectedAgentId === agent.id}<div class="token-form"><div class="settings-form"><label>Token name<input bind:value={tokenNameDraft} placeholder="CI deployment token" /></label><fieldset><legend>Scopes</legend><div class="scope-grid">{#each scopeOptions as scope}<label class="check-label"><input type="checkbox" checked={tokenScopes.includes(scope)} on:change={() => toggleScope(scope)} /><span>{scope}</span></label>{/each}</div></fieldset><fieldset><legend>Limit to projects <span class="optional">Optional</span></legend><div class="scope-grid project-checks">{#each projects as project}<label class="check-label"><input type="checkbox" checked={tokenProjectIds.includes(project.id)} on:change={() => toggleTokenProject(project.id)} /><span>{project.name}</span></label>{/each}</div></fieldset><div class="form-actions"><button class="text-button" type="button" on:click={() => showTokenForm = false}>Cancel</button><button class="button primary" type="button" disabled={!tokenNameDraft.trim() || !tokenScopes.length || tokenCreating} on:click={createToken}>{#if tokenCreating}<span class="button-spinner"></span>{/if}Create token</button></div></div></div>{/if}</article>{/each}</div>{/if}</div><div class="settings-section appearance-section"><div class="settings-section-heading"><div><span class="eyebrow">Workspace</span><h2>Appearance</h2><p>Choose how Roadmap feels on this device.</p></div></div><div class="theme-options"><button class:chosen={theme === 'light'} type="button" on:click={() => { theme = 'light'; localStorage.setItem('roadmap.theme', theme); applyTheme(); }}><span class="theme-preview light-preview">☼</span><span><strong>Light</strong><small>Clear and airy</small></span>{#if theme === 'light'}<span class="theme-check">✓</span>{/if}</button><button class:chosen={theme === 'dark'} type="button" on:click={() => { theme = 'dark'; localStorage.setItem('roadmap.theme', theme); applyTheme(); }}><span class="theme-preview dark-preview">☾</span><span><strong>Dark</strong><small>Focused and low-glare</small></span>{#if theme === 'dark'}<span class="theme-check">✓</span>{/if}</button></div></div></div><aside class="settings-aside"><div class="settings-aside-card"><span class="aside-icon">◎</span><h3>Built for safe handoffs</h3><p>Every mutation records its actor. Scoped agent tokens and optimistic versions keep collaboration predictable.</p><span class="aside-rule"></span><span class="aside-caption">Roadmap v1 · API-connected</span></div></aside></section>
        {/if}
      </main>
    </div>

    {#if drawerTask}
      <div class="drawer-backdrop" role="presentation" on:click={closeDrawer}></div>
      <div class="task-drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title" use:focusTrap>
        <div class="drawer-header"><div><span class="drawer-key">{drawerTask.key}</span><span class={`priority-pill priority-${drawerTask.priority}`}>{priorityLabels[drawerTask.priority]}</span></div><button class="icon-button" type="button" aria-label="Close task details" on:click={closeDrawer}>×</button></div>
        {#if drawerLoading}<div class="drawer-loading"><span class="spinner"></span><span>Loading task details…</span></div>{/if}
        {#if drawerError}<div class="inline-alert error drawer-alert" role="alert"><span>!</span>{drawerError}</div>{/if}
        <div class="drawer-scroll"><label class="drawer-title-label"><span class="sr-only">Task title</span><input id="drawer-title" class="drawer-title-input" data-dialog-initial-focus bind:value={draftTitle} /></label><div class="drawer-meta"><span class="task-project-marker" style={`--project-color: ${projectForTask(drawerTask)?.color || '#6d5efc'}`}></span><span>{projectForTask(drawerTask)?.name || 'Project'}</span><span>·</span><span>Updated {formatRelative(drawerTask?.updated_at)}</span></div><div class="drawer-actions"><button class="button quiet-button" type="button" disabled={taskActionLoading === drawerTask?.id} on:click={() => runTaskAction(claimAction(drawerTask))}>{drawerTask.claimed_by ? (actorId(drawerTask.claimed_by) === user?.id ? '↻ Renew claim' : `Claimed by ${actorName(drawerTask.claimed_by) || 'agent'}`) : '⚑ Claim task'}</button>{#if drawerTask.claimed_by && actorId(drawerTask.claimed_by) === user?.id}<button class="button quiet-button" type="button" disabled={taskActionLoading === drawerTask?.id} on:click={() => runTaskAction('release')}>Release</button>{/if}<button class="button complete-button" type="button" disabled={Boolean(drawerTask.completed_at) || taskActionLoading === drawerTask?.id} on:click={() => runTaskAction('complete')}>{drawerTask.completed_at ? '✓ Completed' : '✓ Complete'}</button></div><section class="drawer-section"><div class="drawer-field-grid"><label>Priority<select bind:value={draftPriority}><option value="urgent">Urgent</option><option value="high">High</option><option value="normal">Normal</option><option value="low">Low</option></select></label><label>Due date<input type="date" bind:value={draftDueDate} /></label></div><label>Assignee<input bind:value={draftAssignee} placeholder="Actor ID (optional)" /></label><label>Labels <span class="optional">Comma separated</span><input bind:value={draftLabels} placeholder="frontend, design" /></label>{#if labels.filter((label) => label.project_id === drawerTask?.project_id).length}<div class="drawer-label-picker"><span class="optional">Project labels</span><div class="drawer-label-options">{#each labels.filter((label) => label.project_id === drawerTask?.project_id) as label (label.id)}<span class="drawer-label-option" style={`--label-color: ${label.color || '#8b7cf6'}`}><span>{label.name}</span><button class="icon-button tiny danger-button" type="button" aria-label={`Delete label ${label.name}`} disabled={labelDeleting === label.id} on:click|stopPropagation={() => deleteProjectLabel(label)}>×</button></span>{/each}</div></div>{/if}</section><section class="drawer-section description-section"><div class="section-heading-inline"><h2>Description</h2><span class="markdown-hint">Markdown supported</span></div><textarea class="description-input" rows="7" bind:value={draftDescription} placeholder="What does success look like?"></textarea></section><button class="button primary save-task-button" type="button" disabled={drawerSaving || !draftTitle.trim()} on:click={saveTask}>{#if drawerSaving}<span class="button-spinner"></span>{/if}Save changes</button><section class="drawer-section activity-section"><div class="section-heading-inline"><h2>Comments &amp; activity</h2><span class="activity-count">{comments.length + drawerActivity.length}</span></div><form class="comment-form" on:submit|preventDefault={postComment}><span class="avatar mini-user-avatar">{projectInitials({ name: user.name, key: user.name })}</span><textarea rows="2" bind:value={commentBody} placeholder="Leave a note for your team…"></textarea><button class="icon-button comment-send" type="submit" disabled={!commentBody.trim() || commentSending} aria-label="Add comment">↑</button></form><div class="activity-list">{#if !comments.length && !drawerActivity.length}<div class="activity-empty">No updates yet. Be the first to leave context.</div>{:else}{#each comments as comment}<article class="activity-item"><span class="activity-avatar" class:agent={typeof comment.author !== 'object' && typeof comment.actor !== 'object'}>{(commentAuthor(comment).slice(0, 1) || '?').toUpperCase()}</span><div><p><strong>{commentAuthor(comment)}</strong><span> commented</span></p><div class="comment-body">{comment.body}</div><time datetime={comment.created_at}>{formatRelative(comment.created_at)}</time></div></article>{/each}{#each drawerActivity as event}<article class="activity-item system-activity"><span class="activity-avatar event-avatar">✦</span><div><p><strong>{eventAuthor(event)}</strong><span> {displayEvent(event).toLowerCase()}</span></p><time datetime={event.created_at}>{formatRelative(event.created_at)}</time></div></article>{/each}{/if}</div></section></div>
        <div class="drawer-delete-wrap"><button class="button danger-button" type="button" disabled={drawerSaving || taskActionLoading === drawerTask?.id} on:click={deleteDrawerTask}>Delete task</button></div>
      </div>
    {/if}

    {#if commandOpen}
      <div class="modal-backdrop command-backdrop" role="presentation" on:click={closeCommandPalette}></div>
      <div class="command-menu" role="dialog" aria-modal="true" aria-label="Search Roadmap" use:focusTrap>
        <div class="command-input-wrap"><span aria-hidden="true">⌕</span><input bind:this={commandInput} data-dialog-initial-focus bind:value={commandQuery} on:keydown={commandKeydown} placeholder="Jump to a project or view…" aria-label="Search projects and views" /><kbd>ESC</kbd></div>
        <div class="command-results">{#if commandChoices.length}{#each commandChoices as choice, index}<button class:selected={index === commandIndex} class="command-row" type="button" on:mouseenter={() => commandIndex = index} on:click={() => selectCommand(choice)}><span class={`command-icon ${choice.kind}`}>{choice.kind === 'project' ? (choice.project ? projectInitials(choice.project) : 'P') : choice.view === 'my-work' ? '◌' : choice.view === 'roadmap' ? '◒' : '⚙'}</span><span><strong>{choice.label}</strong><small>{choice.hint}</small></span><span class="command-enter">↵</span></button>{/each}{:else}<div class="command-empty">No projects or views match “{commandQuery}”</div>{/if}</div><div class="command-footer"><span><kbd>↑</kbd><kbd>↓</kbd> Navigate</span><span><kbd>↵</kbd> Open</span><span><kbd>ESC</kbd> Close</span></div>
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

    {#if showProjectModal}
      <div class="modal-backdrop" role="presentation" on:click={closeProjectModal}></div>
      <div class="modal project-modal" role="dialog" aria-modal="true" aria-labelledby="project-modal-title" use:focusTrap><div class="modal-header"><div><span class="eyebrow">New workspace</span><h2 id="project-modal-title">Create a project</h2></div><button class="icon-button" type="button" aria-label="Close" on:click={closeProjectModal}>×</button></div>{#if projectFormError}<div class="inline-alert error" role="alert"><span>!</span>{projectFormError}</div>{/if}<form on:submit|preventDefault={createProject}><div class="project-form-title"><span class="project-dot huge" style={`--project-color: ${projectColorDraft}`}>{projectInitials({ name: projectNameDraft || 'New project', key: projectKeyDraft || 'NP' })}</span><div><label>Project name<input data-dialog-initial-focus bind:value={projectNameDraft} placeholder="Product launch" /></label></div></div><div class="form-row"><label>Project key<input maxlength="16" bind:value={projectKeyDraft} placeholder="PROD" /></label><label>Accent color<input class="color-input" type="color" bind:value={projectColorDraft} /></label></div><label>Description <span class="optional">Optional</span><textarea rows="3" bind:value={projectDescriptionDraft} placeholder="A short note about what this project is for."></textarea><span class="field-hint">Roadmap will add Backlog, Ready, In progress, Blocked, and Done columns automatically.</span></label><div class="modal-actions"><button class="text-button" type="button" on:click={closeProjectModal}>Cancel</button><button class="button primary" type="submit" disabled={projectCreating || !projectNameDraft.trim() || !projectKeyDraft.trim()}>{#if projectCreating}<span class="button-spinner"></span>{/if}Create project</button></div></form></div>
    {/if}

    {#if revealedToken}
      <div class="modal-backdrop" role="presentation"></div>
      <div class="modal token-reveal-modal" role="alertdialog" aria-modal="true" aria-labelledby="token-reveal-title" use:focusTrap><div class="token-reveal-icon">✓</div><span class="eyebrow">One-time secret</span><h2 id="token-reveal-title">Copy your token now</h2><p>For your security, the token will not be shown again after closing this dialog.</p><div class="token-value"><code>{revealedToken.plaintext || revealedToken.token || ''}</code><button class="icon-button" type="button" aria-label="Copy token" on:click={() => void copyRevealedToken()}>⧉</button></div><button class="button primary button-large" type="button" data-dialog-initial-focus on:click={closeTokenReveal}>I’ve copied it</button></div>
    {/if}

    <div class="toast-stack" aria-live="polite" aria-atomic="true">{#each toasts as item (item.id)}<div class={`toast ${item.kind}`}><span>{item.kind === 'success' ? '✓' : item.kind === 'error' ? '!' : 'i'}</span>{item.message}<button class="icon-button tiny" type="button" aria-label="Dismiss notification" on:click={() => toasts = toasts.filter((toastItem) => toastItem.id !== item.id)}>×</button></div>{/each}</div>
  </div>
{/if}
