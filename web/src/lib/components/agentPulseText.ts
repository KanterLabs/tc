import type { AgentWork, Task } from '../types';

export type AgentWorkLike = Partial<AgentWork>;

const staleAfterMs = 15 * 60 * 1000;
const stateLabels: Record<string, string> = {
  working: 'Working',
  waiting: 'Waiting',
  verifying: 'Verifying',
  handoff: 'Handoff'
};

function parseDate(value: string): number {
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? Number.POSITIVE_INFINITY : parsed;
}

function staleByAge(value: string, now: number): boolean {
  if (!value) return false;
  const age = now - parseDate(value);
  return Number.isFinite(age) && age >= staleAfterMs;
}

function firstNumber(...values: Array<number | null | undefined>): number | undefined {
  return values.find((value): value is number => typeof value === 'number' && Number.isFinite(value));
}

function progressLabel(work: AgentWorkLike | null | undefined): string {
  if (!work) return '';
  const completed = firstNumber(work.checkpoint_completed);
  const total = firstNumber(
    work.checkpoint_total,
    Array.isArray(work.checkpoint_refs) && work.checkpoint_refs.length ? work.checkpoint_refs.length : undefined
  );
  if (completed === undefined || total === undefined || total <= 0) return '';
  return `${Math.max(0, Math.min(total, completed))} of ${total} checkpoints`;
}

function actorFromTask(task: Task): string {
  const owner = task.claimed_by;
  if (!owner) return '';
  return typeof owner === 'string' ? owner : owner.name || owner.id;
}

function formatCountdown(value: string, now: number): string {
  const difference = parseDate(value) - now;
  if (!Number.isFinite(difference)) return '';
  if (difference <= 0) return 'expired';
  const minutes = Math.ceil(difference / 60000);
  if (minutes < 60) return `${minutes}m left`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  if (hours < 24) return `${hours}h${remainder ? ` ${remainder}m` : ''} left`;
  const days = Math.floor(hours / 24);
  return `${days}d${hours % 24 ? ` ${hours % 24}h` : ''} left`;
}

/**
 * Build the short accessible description shared by board cards, the drawer,
 * and component tests. It intentionally only treats agent_work.updated_at as
 * a live check-in; a generic task edit must not make a missing pulse appear
 * fresh.
 */
export function agentPulseAccessibleLabel(task: Task, now: number, actorLabel = ''): string {
  const work = task.agent_work;
  const state = work?.state || '';
  const missing = !work;
  const baseLabel = stateLabels[state] || (missing ? 'No live pulse' : 'Agent update');
  const updatedAt = work?.updated_at || '';
  const stale = Boolean(work && (work.stale || staleByAge(updatedAt, now)));
  const stateLabel = stale && state !== 'waiting' ? 'Stale' : baseLabel;
  const actionNeeded = Boolean(work && (work.action_needed || stale || state === 'waiting' || state === 'handoff'));
  const progress = progressLabel(work);
  const owner = actorFromTask(task);
  const agent = actorLabel || work?.actor_id || '';
  const countdown = task.claim_expires_at ? formatCountdown(task.claim_expires_at, now) : '';
  const claimExpired = Boolean(task.claim_expires_at && parseDate(task.claim_expires_at) <= now);
  const parts = [
    stateLabel,
    stale ? (state === 'waiting' ? 'Waiting update is stale' : `${baseLabel} update is stale`) : '',
    actionNeeded && !stale ? 'action needed' : '',
    work?.phase || '',
    progress,
    agent ? `agent ${agent}` : '',
    owner ? `claimed by ${owner}` : '',
    countdown ? (claimExpired ? 'claim expired' : `claim ${countdown}`) : '',
    missing ? 'no live update reported' : ''
  ];
  return parts.filter(Boolean).join(', ');
}
