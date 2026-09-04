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

function claimExpiresSoon(value: string, now: number): boolean {
  const expires = parseDate(value);
  return Number.isFinite(expires) && expires > now && expires - now <= 5 * 60 * 1000;
}

/** Return a locale-independent exact timestamp for screen readers and tests. */
export function agentPulseExactTimestamp(value: string): string {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString();
}

/** Keep the relative update wording identical between visible and SR text. */
export function agentPulseRelativeTimestamp(value: string, now: number): string {
  const parsed = parseDate(value);
  if (!Number.isFinite(parsed)) return 'unknown update time';
  const difference = Math.round((parsed - now) / 60000);
  const absolute = Math.abs(difference);
  if (absolute < 1) return 'just now';
  if (absolute < 60) return `${absolute}m ${difference < 0 ? 'ago' : 'from now'}`;
  const hours = Math.round(absolute / 60);
  if (hours < 24) return `${hours}h ${difference < 0 ? 'ago' : 'from now'}`;
  const days = Math.round(hours / 24);
  return `${days}d ${difference < 0 ? 'ago' : 'from now'}`;
}

/**
 * Build the short accessible description shared by board cards, the drawer,
 * and component tests. It intentionally only treats agent_work.updated_at as
 * a live check-in; a generic task edit must not make a missing pulse appear
 * fresh.
 */
export function agentPulseAccessibleLabel(task: Task, now: number, actorLabel = ''): string {
  const work = task.agent_work;
  if (task.completed_at) return 'Completed task, no live agent work';
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
  const updateRelative = updatedAt ? agentPulseRelativeTimestamp(updatedAt, now) : '';
  const updateExact = updatedAt ? agentPulseExactTimestamp(updatedAt) : '';
  const claimExact = task.claim_expires_at ? agentPulseExactTimestamp(task.claim_expires_at) : '';
  const parts = [
    stateLabel,
    stale ? (state === 'waiting' ? 'Waiting update is stale' : `${baseLabel} update is stale`) : '',
    actionNeeded && !stale ? 'action needed' : '',
    work?.phase || '',
    progress,
    agent ? `agent ${agent}` : '',
    owner ? `claimed by ${owner}` : '',
    updateRelative ? `updated ${updateRelative} (${updateExact})` : '',
    countdown ? (claimExpired ? 'claim expired' : `claim ${countdown}`) : '',
    claimExact ? `claim expires ${claimExact}` : '',
    claimExpiresSoon(task.claim_expires_at || '', now) ? 'claim expiring soon' : '',
    task.claimed_by && task.version ? `task version v${task.version}` : '',
    task.claimed_by && !task.claim_expires_at ? 'claim expiry unavailable' : '',
    missing ? 'no live update reported' : ''
  ];
  return parts.filter(Boolean).join(', ');
}
