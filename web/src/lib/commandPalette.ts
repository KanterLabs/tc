import type { Project, SavedView, Task } from './types';

export type CommandView = 'board' | 'timeline' | 'issues' | 'my-work' | 'roadmap' | 'search' | 'audits' | 'settings';
export type CommandAction = 'new-task' | 'report-bug' | 'toggle-theme';

type CommandChoiceBase = {
  id: string;
  label: string;
  hint: string;
};

export type CommandChoice =
  | (CommandChoiceBase & { kind: 'action'; action: CommandAction })
  | (CommandChoiceBase & { kind: 'view'; view: CommandView })
  | (CommandChoiceBase & { kind: 'project'; project: Project })
  | (CommandChoiceBase & { kind: 'task'; task: Task })
  | (CommandChoiceBase & { kind: 'issue'; task: Task })
  | (CommandChoiceBase & { kind: 'saved-view'; savedView: SavedView });

export type CommandTheme = 'light' | 'dark';

export interface CommandChoiceInput {
  projects: Project[];
  tasks: Task[];
  issueTasks: Task[];
  searchProjects?: Project[];
  searchTasks?: Task[];
  savedViews?: SavedView[];
  theme: CommandTheme;
}

/**
 * Build the command palette's combined local and server-backed index. Local
 * board rows win when a global search returns the same object so keyboard
 * navigation never contains duplicate choices. An issue already represented
 * by the current board task index is likewise not repeated as a second row.
 */
export function buildCommandChoices({
  projects,
  tasks,
  issueTasks,
  searchProjects = [],
  searchTasks = [],
  savedViews = [],
  theme
}: CommandChoiceInput): CommandChoice[] {
  const allProjects = Array.from(new Map([...searchProjects, ...projects].map((project) => [project.id, project])).values());
  const boardTaskIds = new Set(tasks.map((task) => task.id));
  const regularTasks = Array.from(new Map(
    [...searchTasks.filter((task) => task.kind !== 'bug'), ...tasks].map((task) => [task.id, task])
  ).values());
  const allIssues = Array.from(new Map(
    [...issueTasks, ...searchTasks.filter((task) => task.kind === 'bug')].map((task) => [task.id, task])
  ).values());
  const choices: CommandChoice[] = [
    { kind: 'action', id: 'new-task', action: 'new-task', label: 'New task', hint: 'Capture work on the current board' },
    { kind: 'action', id: 'report-bug', action: 'report-bug', label: 'Report bug', hint: 'Record a regression or unexpected behavior' },
    { kind: 'action', id: 'toggle-theme', action: 'toggle-theme', label: 'Toggle theme', hint: theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode' },
    { kind: 'view', id: 'search', view: 'search', label: 'Search everything', hint: 'Find tasks across every project' },
    { kind: 'view', id: 'issues', view: 'issues', label: 'Issues', hint: 'Track and triage reported bugs' },
    { kind: 'view', id: 'my-work', view: 'my-work', label: 'My work', hint: 'Assigned and claimed tasks' },
    { kind: 'view', id: 'roadmap', view: 'roadmap', label: 'Roadmap overview', hint: 'Progress across every project' },
    { kind: 'view', id: 'settings', view: 'settings', label: 'Settings', hint: 'Agents, tokens, and appearance' },
    ...allProjects.map((project) => ({ kind: 'project' as const, id: project.id, project, label: project.name, hint: project.key })),
    ...savedViews.map((savedView) => ({ kind: 'saved-view' as const, id: savedView.id, savedView, label: savedView.name, hint: savedView.shared ? 'Shared saved view' : 'Saved view' })),
    ...regularTasks.map((task) => ({ kind: 'task' as const, id: task.id, task, label: task.title, hint: task.key })),
    ...allIssues
      .filter((task) => !boardTaskIds.has(task.id))
      .map((task) => ({ kind: 'issue' as const, id: task.id, task, label: task.title, hint: `${task.key} · ${task.bug?.severity?.toUpperCase() || 'Untriaged'}` }))
  ];
  return choices;
}

export function filterCommandChoices(choices: CommandChoice[], query: string): CommandChoice[] {
  const normalized = query.trim().toLowerCase();
  if (!normalized) return choices;
  return choices.filter((choice) => `${choice.label} ${choice.hint}`.toLowerCase().includes(normalized));
}

export function commandChoiceId(choice: CommandChoice): string {
  return `command-option-${choice.kind}-${choice.id}`;
}

export function nextCommandIndex(current: number, length: number, direction: -1 | 1): number {
  if (length <= 0) return 0;
  const normalized = Number.isFinite(current) ? Math.min(Math.max(current, 0), length - 1) : 0;
  return (normalized + direction + length) % length;
}
