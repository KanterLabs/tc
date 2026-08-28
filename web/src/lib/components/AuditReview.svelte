<script context="module" lang="ts">
  import type {
    AuditRun as ModuleAuditRun
  } from '../types';

  export type AuditNoticeKind = 'success' | 'error' | 'info';
  export type AuditNavigation = (path: string) => void;
  export type AuditTaskUpdate = (task: Task) => void;
  export type AuditNotice = (kind: AuditNoticeKind, message: string) => void;

  /** Keep collection/detail payload handling testable without mounting the UI. */
  export function auditStatusLabel(status: string | undefined): string {
    switch ((status || '').toLowerCase()) {
      case 'queued': return 'Queued';
      case 'running': return 'Running';
      case 'partial': return 'Partial';
      case 'failed': return 'Failed';
      case 'finalized':
      case 'complete': return 'Complete';
      default: return status ? status.replace(/[_-]/g, ' ') : 'Unknown';
    }
  }

  export function auditFindingCount(audit: ModuleAuditRun): number {
    if (typeof audit.finding_count === 'number') return audit.finding_count;
    if (typeof audit.findings_count === 'number') return audit.findings_count;
    if (audit.findings?.length) return audit.findings.length;
    return Object.values(audit.counts || {}).reduce((total, count) => total + (typeof count === 'number' ? count : 0), 0);
  }
</script>

<script lang="ts">
  import { onDestroy, tick } from 'svelte';
  import { api } from '../api';
  import {
    ApiError,
    type AuditDetail,
    type AuditFinding,
    type AuditFindingPatch,
    type AuditReviewState,
    type AuditRun,
    type AuditVerdict,
    type Column,
    type Project,
    type SemanticState,
    type Task
  } from '../types';

  export let project: Project;
  export let columns: Column[] = [];
  export let tasks: Task[] = [];
  export let initialAuditId = '';
  export let sessionGeneration = 0;
  /** Parent increments this on its liveness cadence to recalculate drift. */
  export let refreshToken = 0;
  export let onNavigate: AuditNavigation = () => undefined;
  export let onNotice: AuditNotice = () => undefined;
  export let onTaskUpdated: AuditTaskUpdate = () => undefined;

  const stateLabels: Record<SemanticState, string> = {
    backlog: 'Backlog',
    ready: 'Ready',
    active: 'In progress',
    blocked: 'Blocked',
    completed: 'Done'
  };
  const verdictLabels: Record<AuditVerdict, string> = {
    correct: 'Looks correct',
    needs_attention: 'Needs attention',
    move_proposed: 'Move proposed'
  };
  const reviewLabels: Record<AuditReviewState, string> = {
    pending: 'Pending review',
    approved: 'Approved',
    dismissed: 'Dismissed'
  };
  const semanticStates: SemanticState[] = ['backlog', 'ready', 'active', 'blocked', 'completed'];
  const verdicts: AuditVerdict[] = ['correct', 'needs_attention', 'move_proposed'];
  const focusableSelector = [
    'a[href]',
    'area[href]',
    'button:not(:disabled)',
    'input:not(:disabled):not([type="hidden"])',
    'select:not(:disabled)',
    'textarea:not(:disabled)',
    '[contenteditable="true"]',
    '[tabindex]:not([tabindex="-1"])'
  ].join(',');

  let audits: AuditRun[] = [];
  let selectedAudit: AuditDetail | null = null;
  let selectedAuditId = '';
  let listLoading = false;
  let detailLoading = false;
  let listError = '';
  let detailError = '';
  let creating = false;
  let activeMutation = '';
  let listRequest = 0;
  let detailRequest = 0;
  let mutationRequest = 0;
  let loadKey = '';
  let editingFindingId = '';
  let destinationDraft: SemanticState | '' = '';
  let previewFinding: AuditFinding | null = null;
  let applyFinding: AuditFinding | null = null;
  let applyOriginFinding: AuditFinding | null = null;
  let applyError = '';
  let applying = false;
  let modalReturnFocus: HTMLElement | null = null;
  let destroyTimer: number | undefined;

  $: currentProjectId = project?.id || '';
  $: routeKey = `${currentProjectId}:${initialAuditId || ''}:${sessionGeneration}:${refreshToken}`;
  $: if (currentProjectId && routeKey !== loadKey) {
    loadKey = routeKey;
    void loadAuditList(initialAuditId);
  }
  $: findings = selectedAudit?.findings || [];
  $: groupedFindings = verdicts.map((verdict) => ({
    verdict,
    label: verdictLabels[verdict],
    items: findings.filter((finding) => finding.verdict === verdict)
  }));
  $: approvedCount = findings.filter((finding) => finding.review_state === 'approved').length;
  $: pendingCount = findings.filter((finding) => finding.review_state === 'pending').length;
  $: changedCount = findings.filter((finding) => changedSinceAudit(finding)).length;

  function normalizeAudit(value: unknown): AuditRun {
    const raw = unwrapPayload(value) as Partial<AuditRun>;
    return {
      ...raw,
      id: String(raw.id || ''),
      project_id: String(raw.project_id || currentProjectId),
      status: raw.status || 'unknown'
    } as AuditRun;
  }

  function normalizeFinding(value: unknown): AuditFinding {
    const raw = unwrapPayload(value) as Partial<AuditFinding>;
    return {
      ...raw,
      id: String(raw.id || ''),
      audit_id: String(raw.audit_id || selectedAuditId),
      task_id: String(raw.task_id || ''),
      captured_version: numberOr(raw.captured_version, 0),
      source_column: raw.source_column || '',
      verdict: raw.verdict || 'needs_attention',
      confidence: numberOr(raw.confidence, 0),
      reason: String(raw.reason || ''),
      review_state: raw.review_state || 'pending'
    } as AuditFinding;
  }

  function normalizeDetail(value: unknown): AuditDetail {
    const raw = unwrapPayload(value) as Partial<AuditDetail> & { data?: unknown; findings?: unknown };
    const rawFindings = Array.isArray(raw.findings)
      ? raw.findings
      : Array.isArray((raw.data as { findings?: unknown[] } | undefined)?.findings)
        ? (raw.data as { findings: unknown[] }).findings
        : [];
    return {
      ...raw,
      id: String(raw.id || selectedAuditId),
      project_id: String(raw.project_id || currentProjectId),
      status: raw.status || 'unknown',
      findings: rawFindings.map(normalizeFinding)
    } as AuditDetail;
  }

  function unwrapPayload(value: unknown): unknown {
    if (!value || typeof value !== 'object') return value;
    const record = value as Record<string, unknown>;
    if (record.audit && typeof record.audit === 'object') return record.audit;
    if (record.finding && typeof record.finding === 'object') return record.finding;
    if (record.data && !Array.isArray(record.data) && typeof record.data === 'object') return record.data;
    return value;
  }

  function numberOr(value: unknown, fallback: number): number {
    return typeof value === 'number' && Number.isFinite(value) ? value : fallback;
  }

  function isCurrent(requestedSession: number, requestId: number, kind: 'list' | 'detail' | 'mutation'): boolean {
    if (!project?.id || project.id !== currentProjectId || sessionGeneration !== requestedSession) return false;
    if (kind === 'list') return requestId === listRequest;
    if (kind === 'detail') return requestId === detailRequest;
    return requestId === mutationRequest;
  }

  async function loadAuditList(preferredId = ''): Promise<void> {
    const requestId = ++listRequest;
    const requestedSession = sessionGeneration;
    listLoading = true;
    listError = '';
    try {
      const result = await api.listAllAudits(project.id);
      if (!isCurrent(requestedSession, requestId, 'list')) return;
      audits = result.data.map(normalizeAudit).filter((audit) => audit.id);
      const targetId = preferredId || selectedAuditId;
      if (targetId) {
        const target = audits.find((audit) => audit.id === targetId);
        if (target) await loadAuditDetail(target.id);
        else if (targetId === selectedAuditId) {
          selectedAudit = null;
          selectedAuditId = '';
          onNavigate(`/p/${encodeURIComponent(project.slug)}/audits`);
        }
      }
    } catch (error) {
      if (isCurrent(requestedSession, requestId, 'list')) listError = friendlyError(error, 'Audit runs could not be loaded.');
    } finally {
      if (requestId === listRequest) listLoading = false;
    }
  }

  async function loadAuditDetail(auditId: string): Promise<void> {
    if (!auditId) return;
    const requestId = ++detailRequest;
    const requestedSession = sessionGeneration;
    selectedAuditId = auditId;
    detailLoading = true;
    detailError = '';
    try {
      const result = await api.getAudit(auditId);
      if (!isCurrent(requestedSession, requestId, 'detail')) return;
      const detail = normalizeDetail(result);
      selectedAudit = detail;
      audits = audits.map((audit) => audit.id === detail.id ? { ...audit, ...detail, findings: detail.findings } : audit);
    } catch (error) {
      if (isCurrent(requestedSession, requestId, 'detail')) detailError = friendlyError(error, 'This audit could not be loaded.');
    } finally {
      if (requestId === detailRequest) detailLoading = false;
    }
  }

  async function refresh(): Promise<void> {
    if (selectedAuditId) {
      const requestedId = selectedAuditId;
      await Promise.all([loadAuditList(requestedId), loadAuditDetail(requestedId)]);
    } else {
      await loadAuditList();
    }
  }

  async function createAudit(): Promise<void> {
    if (!project?.id || creating) return;
    creating = true;
    listError = '';
    try {
      // `scope` is required by the current audit table. The backend may add
      // more scope choices later; this UI intentionally starts a board audit.
      const result = normalizeAudit(await api.createAudit(project.id, { scope: 'board', status: 'queued' }));
      audits = [result, ...audits.filter((audit) => audit.id !== result.id)];
      onNotice('success', `Audit ${auditStatusLabel(result.status).toLowerCase()} — recommendations are review-only until you apply one.`);
      announce(`Audit ${auditStatusLabel(result.status)}. No task moves were applied.`);
      if (result.id) {
        selectedAuditId = result.id;
        onNavigate(`/p/${encodeURIComponent(project.slug)}/audits/${encodeURIComponent(result.id)}`);
        await loadAuditDetail(result.id);
      }
    } catch (error) {
      listError = friendlyError(error, 'A new audit could not be started.');
    } finally {
      creating = false;
    }
  }

  function openAudit(audit: AuditRun): void {
    if (!audit.id) return;
    selectedAuditId = audit.id;
    onNavigate(`/p/${encodeURIComponent(project.slug)}/audits/${encodeURIComponent(audit.id)}`);
    void loadAuditDetail(audit.id);
  }

  function showList(): void {
    detailRequest += 1;
    selectedAudit = null;
    selectedAuditId = '';
    detailError = '';
    onNavigate(`/p/${encodeURIComponent(project.slug)}/audits`);
  }

  function findingTask(finding: AuditFinding): Task | undefined {
    return finding.current_task || finding.task || finding.current?.task || tasks.find((task) => task.id === finding.task_id);
  }

  function currentVersion(finding: AuditFinding): number | undefined {
    const task = findingTask(finding);
    return finding.current_version
      ?? finding.current?.version
      ?? task?.version
      ?? undefined;
  }

  function mutationVersion(finding: AuditFinding): number {
    // Review versions were added after the append-only snapshot schema. Until
    // every server emits one, the captured version remains the safest guard.
    return finding.version && finding.version > 0 ? finding.version : Math.max(1, finding.captured_version);
  }

  function columnIdentifier(value: unknown): string {
    if (typeof value === 'string') return value;
    if (!value || typeof value !== 'object') return '';
    const record = value as Record<string, unknown>;
    return String(record.id || record.name || record.semantic_state || '');
  }

  function columnLabel(value: unknown): string {
    if (typeof value === 'string') {
      const column = columns.find((item) => item.id === value || item.name === value || item.semantic_state === value);
      return column?.name || stateLabels[value as SemanticState] || value;
    }
    if (!value || typeof value !== 'object') return 'Unknown column';
    const record = value as Record<string, unknown>;
    return String(record.name || stateLabels[record.semantic_state as SemanticState] || record.id || 'Unknown column');
  }

  function currentColumnValue(finding: AuditFinding): unknown {
    const task = findingTask(finding);
    return finding.current_column
      ?? finding.current?.column
      ?? finding.current_column_id
      ?? task?.column_id
      ?? '';
  }

  function changedSinceAudit(finding: AuditFinding): boolean {
    // A backend `true` is authoritative. When the board task is also present
    // in the parent, still compare that live snapshot so an in-session edit
    // cannot be approved during the gap before the next audit refresh.
    if (finding.changed_since_audit === true) return true;
    const version = currentVersion(finding);
    if (version !== undefined && finding.captured_version > 0 && version !== finding.captured_version) return true;
    const source = columnIdentifier(finding.source_column);
    const current = columnIdentifier(currentColumnValue(finding));
    if (!source || !current) return false;
    const sourceColumn = columns.find((column) => column.id === source || column.name === source || column.semantic_state === source);
    const currentColumn = columns.find((column) => column.id === current || column.name === current || column.semantic_state === current);
    if (sourceColumn && currentColumn && sourceColumn.id !== currentColumn.id) return true;
    return false;
  }

  function sourceLabel(finding: AuditFinding): string {
    return columnLabel(finding.source_column);
  }

  function currentLabel(finding: AuditFinding): string {
    const value = currentColumnValue(finding);
    return value ? columnLabel(value) : 'Current column unavailable';
  }

  function destinationState(finding: AuditFinding): SemanticState | '' {
    return semanticStates.includes(finding.proposed_semantic_destination as SemanticState)
      ? finding.proposed_semantic_destination as SemanticState
      : '';
  }

  function destinationLabel(finding: AuditFinding): string {
    const destination = destinationState(finding);
    if (!destination) return 'No move suggested';
    return columns.find((column) => column.semantic_state === destination)?.name || stateLabels[destination];
  }

  function taskLabel(finding: AuditFinding): string {
    const task = findingTask(finding);
    return task?.key || task?.title || finding.task_id || 'Task';
  }

  function confidenceLabel(value: number): string {
    const percent = Math.round(Math.max(0, Math.min(1, Number(value) || 0)) * 100);
    return `${percent}% confidence`;
  }

  function evidenceFor(finding: AuditFinding): string[] {
    return (finding.evidence_refs?.length ? finding.evidence_refs : finding.evidence || []).filter(Boolean);
  }

  function formatDate(value?: string | null): string {
    if (!value) return 'Unknown date';
    const parsed = new Date(value);
    if (Number.isNaN(parsed.getTime())) return value;
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(parsed);
  }

  function ageLabel(audit: AuditRun): string {
    const value = audit.started_at || audit.created_at;
    if (!value) return 'Age unavailable';
    const elapsed = Date.now() - Date.parse(value);
    if (!Number.isFinite(elapsed) || elapsed < 0) return 'Just now';
    const minutes = Math.floor(elapsed / 60000);
    if (minutes < 1) return 'Just now';
    if (minutes < 60) return `${minutes}m old`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h old`;
    return `${Math.floor(hours / 24)}d old`;
  }

  function statusClass(status: string | undefined): string {
    const normalized = (status || '').toLowerCase();
    return ['queued', 'running', 'complete', 'partial', 'failed', 'finalized'].includes(normalized) ? normalized : 'unknown';
  }

  function auditReadyForReview(): boolean {
    return Boolean(selectedAudit && ['complete', 'partial'].includes(selectedAudit.status));
  }

  function reviewClass(state: string | undefined): string {
    return ['pending', 'approved', 'dismissed'].includes((state || '').toLowerCase()) ? (state || '').toLowerCase() : 'pending';
  }

  function verdictClass(verdict: string | undefined): string {
    return verdicts.includes(verdict as AuditVerdict) ? verdict || 'needs_attention' : 'needs_attention';
  }

  function findingVersionLabel(finding: AuditFinding): string {
    return finding.captured_version > 0 ? `v${finding.captured_version}` : 'unknown';
  }

  function currentVersionLabel(finding: AuditFinding): string {
    const version = currentVersion(finding);
    return version === undefined ? 'unknown' : `v${version}`;
  }

  function editDestination(finding: AuditFinding): void {
    editingFindingId = finding.id;
    destinationDraft = destinationState(finding) || '';
    void tick().then(() => document.querySelector<HTMLSelectElement>(`[data-audit-destination="${CSS.escape(finding.id)}"]`)?.focus());
  }

  function cancelDestination(): void {
    editingFindingId = '';
    destinationDraft = '';
  }

  async function saveDestination(finding: AuditFinding): Promise<void> {
    const proposed = destinationDraft || null;
    await updateFinding(finding, {
      review_state: (finding.review_state as AuditReviewState) || 'pending',
      proposed_semantic_destination: proposed
    }, 'destination updated');
    if (!activeMutation) cancelDestination();
  }

  async function approveFinding(finding: AuditFinding): Promise<void> {
    if (changedSinceAudit(finding)) {
      detailError = 'This finding changed since the audit and cannot be approved. Refresh the board and review the current task.';
      return;
    }
    await updateFinding(finding, { review_state: 'approved' }, 'approved');
  }

  async function dismissFinding(finding: AuditFinding): Promise<void> {
    await updateFinding(finding, { review_state: 'dismissed' }, 'dismissed');
  }

  async function updateFinding(finding: AuditFinding, patch: AuditFindingPatch, actionLabel: string): Promise<void> {
    if (!selectedAudit || !finding.id || activeMutation) return;
    if (!auditReadyForReview()) {
      detailError = 'The agent must finish this audit before findings can be reviewed.';
      return;
    }
    const requestId = ++mutationRequest;
    const requestedSession = sessionGeneration;
    activeMutation = finding.id;
    detailError = '';
    try {
      const result = await api.patchAuditFinding(finding.id, patch, mutationVersion(finding));
      if (!isCurrent(requestedSession, requestId, 'mutation')) return;
      const updated = normalizeFinding(result);
      selectedAudit = {
        ...selectedAudit,
        findings: selectedAudit.findings.map((item) => item.id === updated.id ? updated : item)
      };
      audits = audits.map((audit) => audit.id === selectedAudit?.id
        ? { ...audit, findings: selectedAudit?.findings }
        : audit);
      onNotice('success', `${taskLabel(updated)} ${actionLabel}.`);
      announce(`${taskLabel(updated)} ${actionLabel}.`);
    } catch (error) {
      if (isCurrent(requestedSession, requestId, 'mutation')) {
        detailError = friendlyError(error, 'The finding changed elsewhere. Refresh and try again.');
        if (error instanceof ApiError && error.details.current) {
          const current = normalizeFinding(error.details.current);
          selectedAudit = {
            ...selectedAudit,
            findings: selectedAudit.findings.map((item) => item.id === current.id ? current : item)
          };
          detailError = 'This finding changed elsewhere. The authoritative review state is shown below.';
        }
      }
    } finally {
      if (requestId === mutationRequest) activeMutation = '';
    }
  }

  function canApply(finding: AuditFinding): boolean {
    const task = findingTask(finding);
    const destination = destinationState(finding);
    const destinationColumn = columns.find((column) => column.semantic_state === destination);
    return auditReadyForReview()
      && finding.review_state === 'approved'
      && finding.verdict === 'move_proposed'
      && !changedSinceAudit(finding)
      && (destination === 'backlog' || destination === 'ready')
      && Boolean(task?.id && task.version && task.column_id && destinationColumn);
  }

  function applyBlockReason(finding: AuditFinding): string {
    if (!auditReadyForReview()) return 'Wait for the agent to finish this audit before review or apply';
    if (changedSinceAudit(finding)) return 'Changed since audit — refresh before applying';
    if (!findingTask(finding)) return 'Current task unavailable';
    if (!columns.find((column) => column.semantic_state === destinationState(finding))) return 'Destination column unavailable';
    if (!['backlog', 'ready'].includes(destinationState(finding))) return 'Use the explicit claim, block, or complete lifecycle action for this destination';
    if (finding.review_state !== 'approved') return 'Approve to preview';
    return '';
  }

  function openPreview(finding: AuditFinding): void {
    if (finding.review_state !== 'approved' || !auditReadyForReview()) return;
    if (changedSinceAudit(finding)) {
      detailError = 'This finding changed since the audit and cannot be previewed or applied.';
      return;
    }
    rememberModalFocus();
    previewFinding = finding;
    applyFinding = null;
    applyOriginFinding = null;
    applyError = '';
    void tick().then(() => document.querySelector<HTMLElement>('[data-audit-preview-initial-focus]')?.focus());
  }

  function closePreview(): void {
    if (!previewFinding) return;
    previewFinding = null;
    applyFinding = null;
    applyOriginFinding = null;
    applyError = '';
    restoreModalFocus();
  }

  function openApplyConfirmation(finding: AuditFinding): void {
    if (!canApply(finding)) {
      applyError = applyBlockReason(finding) || 'This recommendation is not ready to apply.';
      return;
    }
    previewFinding = null;
    applyFinding = finding;
    applyOriginFinding = finding;
    applyError = '';
    void tick().then(() => document.querySelector<HTMLElement>('[data-audit-apply-initial-focus]')?.focus());
  }

  function cancelApply(): void {
    const origin = applyOriginFinding;
    applyFinding = null;
    applyOriginFinding = null;
    applyError = '';
    if (origin) {
      previewFinding = origin;
      void tick().then(() => document.querySelector<HTMLElement>('[data-audit-preview-initial-focus]')?.focus());
    }
  }

  async function applyApprovedFinding(): Promise<void> {
    const finding = applyFinding;
    if (!finding || !canApply(finding) || applying) return;
    const task = findingTask(finding);
    const destination = columns.find((column) => column.semantic_state === destinationState(finding));
    if (!task || !destination) return;
    const requestId = ++mutationRequest;
    const requestedSession = sessionGeneration;
    applying = true;
    applyError = '';
    try {
      const updated = await api.moveTask(task.id, {
        destination_column_id: destination.id,
        expected_source_column_id: columnIdentifier(finding.source_column),
        source: 'board_audit',
        reason: finding.reason
      }, task.version);
      if (!isCurrent(requestedSession, requestId, 'mutation')) return;
      onTaskUpdated(updated);
      onNotice('success', `${taskLabel(finding)} moved to ${destination.name}.`);
      announce(`${taskLabel(finding)} moved to ${destination.name}.`);
      applyFinding = null;
      previewFinding = null;
      applyOriginFinding = null;
      restoreModalFocus();
    } catch (error) {
      if (isCurrent(requestedSession, requestId, 'mutation')) {
        applyError = friendlyError(error, 'The recommendation could not be applied. Refresh and try again.');
        if (error instanceof ApiError && error.details.current) {
          onTaskUpdated(error.details.current as Task);
          applyError = 'The task changed before apply. It was not moved; review the current task and audit finding.';
        }
      }
    } finally {
      if (requestId === mutationRequest) applying = false;
    }
  }

  function rememberModalFocus(): void {
    if (typeof document === 'undefined') return;
    const active = document.activeElement;
    modalReturnFocus = active instanceof HTMLElement ? active : null;
  }

  function restoreModalFocus(): void {
    const target = modalReturnFocus;
    modalReturnFocus = null;
    if (!target) return;
    void tick().then(() => {
      if (target.isConnected && !target.hasAttribute('disabled')) target.focus();
    });
  }

  function isVisible(element: HTMLElement): boolean {
    if (!element.isConnected || element.hasAttribute('hidden') || element.getAttribute('aria-hidden') === 'true') return false;
    const style = window.getComputedStyle(element);
    return style.display !== 'none' && style.visibility !== 'hidden';
  }

  function focusTrap(node: HTMLElement) {
    const handleKeydown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return;
      const focusable = Array.from(node.querySelectorAll<HTMLElement>(focusableSelector)).filter(isVisible);
      if (!focusable.length) {
        event.preventDefault();
        node.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    node.addEventListener('keydown', handleKeydown);
    return { destroy: () => node.removeEventListener('keydown', handleKeydown) };
  }

  function handleKeydown(event: KeyboardEvent): void {
    if (event.key !== 'Escape') return;
    if (applyFinding) {
      cancelApply();
    } else if (previewFinding) {
      closePreview();
    }
  }

  function announce(message: string): void {
    const node = document.querySelector<HTMLElement>('[data-audit-live-region]');
    if (!node) return;
    node.textContent = '';
    window.clearTimeout(destroyTimer);
    destroyTimer = window.setTimeout(() => { node.textContent = message; }, 20);
  }

  function friendlyError(error: unknown, fallback: string): string {
    if (error instanceof ApiError) return error.message;
    if (error instanceof Error) return error.message;
    return fallback;
  }

  onDestroy(() => window.clearTimeout(destroyTimer));
</script>

<svelte:window on:keydown={handleKeydown} />

<section class="audit-review" aria-labelledby="audit-heading">
  {#if selectedAuditId && selectedAudit}
    <section class="page-heading audit-heading">
      <div>
        <div class="breadcrumbs"><button class="text-button audit-back-link" type="button" on:click={showList}>Audits</button><span>/</span><span>{auditStatusLabel(selectedAudit.status)}</span></div>
        <div class="heading-title-row"><span class="audit-heading-icon" aria-hidden="true">◎</span><h1 id="audit-heading">Board audit</h1><span class={`audit-status ${statusClass(selectedAudit.status)}`}>{auditStatusLabel(selectedAudit.status)}</span></div>
        <p>Review captured board recommendations before any task is reconciled. Opening and refreshing this audit never changes the board.</p>
      </div>
      <div class="heading-actions"><button class="button quiet-button" type="button" on:click={refresh} disabled={detailLoading || listLoading}>↻ Refresh</button><button class="button quiet-button" type="button" on:click={showList}>All audits</button></div>
    </section>

    {#if detailError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{detailError}</span><button class="text-button" type="button" on:click={() => loadAuditDetail(selectedAuditId)}>Retry</button></div>{/if}
    {#if !auditReadyForReview()}<div class="inline-alert content-alert" role="status"><span>…</span><span>{selectedAudit.status === 'failed' ? 'This audit failed. Run a fresh audit before reviewing or applying findings.' : 'An agent is still processing this audit. Review and apply unlock only after it finishes.'}</span></div>{/if}
    {#if detailLoading && !findings.length}
      <div class="audit-loading" aria-label="Loading audit findings"><div></div><div></div><div></div></div>
    {:else}
      <section class="audit-summary" aria-label="Audit summary">
        <div><span class="eyebrow">{project.name} · {selectedAudit.scope || 'Board scope'}</span><h2>{findings.length} {findings.length === 1 ? 'finding' : 'findings'} to review</h2><p>Captured {formatDate(selectedAudit.started_at || selectedAudit.created_at)} · {ageLabel(selectedAudit)}{#if selectedAudit.finalized_at} · Finalized {formatDate(selectedAudit.finalized_at)}{/if}</p></div>
        <div class="audit-summary-metrics"><span><strong>{approvedCount}</strong> approved</span><span><strong>{pendingCount}</strong> pending</span>{#if changedCount}<span class="changed-metric"><strong>{changedCount}</strong> changed</span>{/if}</div>
      </section>
      {#each groupedFindings as group}
        <section class={`audit-group audit-group-${group.verdict}`} aria-labelledby={`audit-group-${group.verdict}`}>
          <div class="audit-group-heading"><div><h2 id={`audit-group-${group.verdict}`}>{group.label}</h2><p>{group.verdict === 'correct' ? 'No board action was suggested.' : group.verdict === 'needs_attention' ? 'Review the evidence and decide whether this finding is useful.' : 'Approve a destination, then preview and explicitly apply the move.'}</p></div><span>{group.items.length}</span></div>
          {#if group.items.length}
            <div class="audit-finding-list">
              {#each group.items as finding (finding.id)}
                <article class="audit-finding" class:changed={changedSinceAudit(finding)}>
                  <div class="audit-finding-main">
                    <div class="audit-finding-top"><span class="task-key">{taskLabel(finding)}</span><span class={`audit-verdict ${verdictClass(finding.verdict)}`}>{verdictLabels[finding.verdict as AuditVerdict] || finding.verdict}</span><span class={`audit-review-state ${reviewClass(finding.review_state)}`}>{reviewLabels[finding.review_state as AuditReviewState] || finding.review_state}</span></div>
                    <h3>{findingTask(finding)?.title || finding.task_id}</h3>
                    <div class="audit-route"><span>{sourceLabel(finding)}</span><span aria-hidden="true">→</span><strong>{destinationLabel(finding)}</strong></div>
                    <p class="audit-reason">{finding.reason || 'No reason provided.'}</p>
                    {#if evidenceFor(finding).length}<div class="audit-evidence"><span class="optional">Evidence</span>{#each evidenceFor(finding) as evidence}<span>{evidence}</span>{/each}</div>{/if}
                    <div class="audit-finding-meta"><span>{confidenceLabel(finding.confidence)}</span><span>Captured <strong>{findingVersionLabel(finding)}</strong></span><span>Current <strong>{currentVersionLabel(finding)}</strong></span><span>{changedSinceAudit(finding) ? 'Changed since audit' : `Current: ${currentLabel(finding)}`}</span></div>
                    {#if changedSinceAudit(finding)}<div class="audit-changed-warning" role="alert"><span aria-hidden="true">!</span><span>This task changed after the audit. Approval and apply are locked until a fresh audit captures the current version.</span></div>{/if}
                  </div>
                  <div class="audit-finding-actions">
                    {#if editingFindingId === finding.id}
                      <label class="audit-destination-editor">Suggested column<select data-audit-destination={finding.id} aria-label={`Suggested column for ${taskLabel(finding)}`} bind:value={destinationDraft}>{#each semanticStates as state}<option value={state}>{columns.find((column) => column.semantic_state === state)?.name || stateLabels[state]}</option>{/each}<option value="">No move suggested</option></select></label>
                      <div class="audit-action-row"><button class="button primary compact-button" type="button" disabled={activeMutation === finding.id} on:click={() => void saveDestination(finding)}>Save destination</button><button class="button quiet-button compact-button" type="button" disabled={activeMutation === finding.id} on:click={cancelDestination}>Cancel</button></div>
                    {:else}
                      <button class="button quiet-button compact-button" type="button" disabled={!auditReadyForReview()} on:click={() => editDestination(finding)}>Edit destination</button>
                    {/if}
                    <div class="audit-action-row"><button class="button quiet-button compact-button" type="button" disabled={!auditReadyForReview() || activeMutation === finding.id || changedSinceAudit(finding)} on:click={() => void approveFinding(finding)}>{finding.review_state === 'approved' ? 'Approved' : 'Approve'}</button><button class="button quiet-button compact-button" type="button" disabled={!auditReadyForReview() || activeMutation === finding.id || finding.review_state === 'dismissed'} on:click={() => void dismissFinding(finding)}>{finding.review_state === 'dismissed' ? 'Dismissed' : 'Dismiss'}</button></div>
                    {#if finding.review_state === 'approved'}<button class="button primary compact-button" type="button" disabled={!auditReadyForReview() || changedSinceAudit(finding)} title={applyBlockReason(finding)} on:click={() => openPreview(finding)}>Preview approved finding</button>{/if}
                  </div>
                </article>
              {/each}
            </div>
          {:else}<div class="audit-group-empty">No findings in this group.</div>{/if}
        </section>
      {/each}
    {/if}
  {:else}
    <section class="page-heading audit-heading">
      <div>
        <div class="breadcrumbs"><span>Workspace</span><span>/</span><span>{project.key}</span></div>
        <div class="heading-title-row"><span class="audit-heading-icon" aria-hidden="true">◎</span><h1 id="audit-heading">Board audits</h1></div>
        <p>Review the board as it was captured, understand recommendations, and decide deliberately what should change.</p>
      </div>
      <div class="heading-actions"><button class="button quiet-button" type="button" on:click={refresh} disabled={listLoading}>↻ Refresh</button><button class="button primary" type="button" on:click={() => void createAudit()} disabled={creating}>{#if creating}<span class="button-spinner"></span>{/if}Run board audit</button></div>
    </section>
    {#if listError}<div class="inline-alert error content-alert" role="alert"><span>!</span><span>{listError}</span><button class="text-button" type="button" on:click={refresh}>Retry</button></div>{/if}
    {#if listLoading && !audits.length}<div class="audit-loading" aria-label="Loading audits"><div></div><div></div><div></div></div>{:else if !audits.length}<div class="empty-state audit-empty"><div class="empty-icon">◎</div><h2>No board audits yet</h2><p>Run an audit to capture a read-only snapshot of this project. Recommendations will wait for your review.</p><button class="button primary" type="button" on:click={() => void createAudit()} disabled={creating}>Run first audit</button></div>{:else}<section class="audit-list" aria-label={`${project.name} board audits`}>
      {#each audits as audit (audit.id)}<button class="audit-run-row" type="button" on:click={() => openAudit(audit)}><span class={`audit-run-icon ${statusClass(audit.status)}`} aria-hidden="true">{statusClass(audit.status) === 'failed' ? '!' : statusClass(audit.status) === 'running' ? '◌' : '✓'}</span><span class="audit-run-main"><span class="audit-run-top"><span class="task-key">{audit.id}</span><span class={`audit-status ${statusClass(audit.status)}`}>{auditStatusLabel(audit.status)}</span></span><strong>{audit.scope || 'Board audit'}</strong><small>{formatDate(audit.started_at || audit.created_at)} · {ageLabel(audit)}</small></span><span class="audit-run-count"><strong>{auditFindingCount(audit)}</strong><small>findings</small></span><span class="row-arrow">→</span></button>{/each}
    </section>{/if}
  {/if}
</section>

{#if previewFinding}
  <div class="modal-backdrop audit-modal-backdrop" role="presentation" on:click|self={closePreview}></div>
  <div class="modal audit-preview-modal" role="dialog" aria-modal="true" aria-labelledby="audit-preview-title" tabindex="-1" use:focusTrap>
    <div class="modal-header"><div><span class="eyebrow">No task changes yet</span><h2 id="audit-preview-title">Preview approved finding</h2></div><button class="icon-button" type="button" aria-label="Close preview" on:click={closePreview}>×</button></div>
    <div class="audit-preview-route"><span>{sourceLabel(previewFinding)}</span><span aria-hidden="true">→</span><strong>{destinationLabel(previewFinding)}</strong></div>
    <p class="audit-preview-copy">{findingTask(previewFinding)?.title || previewFinding.task_id}</p>
    <dl class="audit-preview-details"><div><dt>Current column</dt><dd>{currentLabel(previewFinding)}</dd></div><div><dt>Captured version</dt><dd>{findingVersionLabel(previewFinding)}</dd></div><div><dt>Current version</dt><dd>{currentVersionLabel(previewFinding)}</dd></div><div><dt>Confidence</dt><dd>{confidenceLabel(previewFinding.confidence)}</dd></div></dl>
    <p class="audit-preview-reason">{previewFinding.reason}</p>
    {#if previewFinding.verdict === 'move_proposed'}<p class="audit-preview-note">Applying will call the guarded task move API. The move will only happen after the separate confirmation below.</p>{:else}<p class="audit-preview-note">This finding is approved for review only; it has no move to apply.</p>{/if}
    <div class="modal-actions"><button class="text-button" type="button" on:click={closePreview}>Close</button>{#if previewFinding.verdict === 'move_proposed'}<button class="button primary" type="button" data-audit-preview-initial-focus disabled={!canApply(previewFinding)} on:click={() => openApplyConfirmation(previewFinding as AuditFinding)}>Continue to apply</button>{:else}<button class="button primary" type="button" data-audit-preview-initial-focus on:click={closePreview}>Done</button>{/if}</div>
  </div>
{/if}

{#if applyFinding}
  <div class="modal-backdrop audit-modal-backdrop" role="presentation"></div>
  <div class="modal audit-apply-modal" role="alertdialog" aria-modal="true" aria-labelledby="audit-apply-title" tabindex="-1" use:focusTrap>
    <div class="modal-header"><div><span class="eyebrow">Explicit confirmation</span><h2 id="audit-apply-title">Apply this move?</h2></div><button class="icon-button" type="button" aria-label="Close apply confirmation" on:click={cancelApply}>×</button></div>
    <p>Move <strong>{taskLabel(applyFinding)}</strong> from <strong>{currentLabel(applyFinding)}</strong> to <strong>{destinationLabel(applyFinding)}</strong>?</p>
    <div class="audit-confirm-warning"><span aria-hidden="true">!</span><span>This is the only step that changes the task. The captured version is {findingVersionLabel(applyFinding)} and the current version is {currentVersionLabel(applyFinding)}.</span></div>
    {#if applyError}<div class="inline-alert error" role="alert"><span>!</span>{applyError}</div>{/if}
    <div class="modal-actions"><button class="text-button" type="button" on:click={cancelApply}>Cancel</button><button class="button primary" type="button" data-audit-apply-initial-focus disabled={applying || !canApply(applyFinding)} on:click={() => void applyApprovedFinding()}>{#if applying}<span class="button-spinner"></span>{/if}Apply move</button></div>
  </div>
{/if}

<div class="sr-only" data-audit-live-region aria-live="polite" aria-atomic="true"></div>
