import { describe, expect, it } from 'vitest';
import {
  buildCommandChoices,
  commandChoiceId,
  filterCommandChoices,
  nextCommandIndex
} from './commandPalette';
import type { Project, Task } from './types';

const project: Project = {
  id: 'project-1',
  key: 'OPS',
  slug: 'ops',
  name: 'Operations',
  description: '',
  color: '#6d5efc',
  favorite: true
};

function task(id: string, key: string, title: string, projectId = project.id): Task {
  return {
    id,
    number: Number(key.split('-').at(-1)),
    key,
    project_id: projectId,
    column_id: 'backlog',
    title,
    priority: 'normal',
    position: 0,
    version: 1
  };
}

describe('command palette index', () => {
  it('indexes loaded board tasks by title and key while retaining actions, views, projects, and other-project issues', () => {
    const boardTask = task('task-1', 'OPS-42', 'Rotate API credentials');
    const boardBug = { ...task('bug-1', 'OPS-43', 'Save button fails'), kind: 'bug' as const, bug: { actual_behavior: 'No-op', expected_behavior: 'Saved', reproduction_steps: 'Click', environment: 'Chrome', affected_version: 'dev', reporter_id: 'person-1' } };
    const remoteIssue = { ...task('issue-1', 'WEB-8', 'Preview session expires', 'project-2'), kind: 'bug' as const, bug: { actual_behavior: 'Signed out', expected_behavior: 'Stay signed in', reproduction_steps: 'Reload', environment: 'Chrome', affected_version: 'dev', reporter_id: 'person-2', severity: 's2' as const } };

    const choices = buildCommandChoices({
      projects: [project],
      tasks: [boardTask, boardBug],
      issueTasks: [boardBug, remoteIssue],
      theme: 'light'
    });

    expect(choices.filter((choice) => choice.kind === 'task').map((choice) => choice.label)).toEqual([
      'Rotate API credentials',
      'Save button fails'
    ]);
    expect(choices.filter((choice) => choice.kind === 'issue').map((choice) => choice.label)).toEqual(['Preview session expires']);
    expect(choices.filter((choice) => choice.kind === 'action').map((choice) => choice.label)).toEqual([
      'New task',
      'Report bug',
      'Toggle theme'
    ]);
    expect(choices.some((choice) => choice.kind === 'project' && choice.label === 'Operations')).toBe(true);
    expect(choices.some((choice) => choice.kind === 'view' && choice.view === 'settings')).toBe(true);
  });

  it('matches a task by either title or key', () => {
    const choices = buildCommandChoices({ projects: [project], tasks: [task('task-1', 'OPS-42', 'Rotate API credentials')], issueTasks: [], theme: 'dark' });

    expect(filterCommandChoices(choices, 'rotate').map((choice) => choice.id)).toEqual(['task-1']);
    expect(filterCommandChoices(choices, 'ops-42').map((choice) => choice.id)).toEqual(['task-1']);
    expect(filterCommandChoices(choices, 'no such command')).toEqual([]);
  });

  it('wraps keyboard movement and gives each result a stable option id', () => {
    expect(nextCommandIndex(0, 3, -1)).toBe(2);
    expect(nextCommandIndex(2, 3, 1)).toBe(0);
    expect(nextCommandIndex(4, 0, 1)).toBe(0);
    expect(commandChoiceId({ kind: 'action', id: 'new-task', action: 'new-task', label: 'New task', hint: 'Capture work' })).toBe('command-option-action-new-task');
  });
});
