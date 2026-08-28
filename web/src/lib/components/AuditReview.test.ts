import { describe, expect, it } from 'vitest';
import { auditFindingCount, auditStatusLabel } from './AuditReview.svelte';

describe('audit review presentation helpers', () => {
  it('keeps every server lifecycle state human-readable', () => {
    expect(auditStatusLabel('queued')).toBe('Queued');
    expect(auditStatusLabel('running')).toBe('Running');
    expect(auditStatusLabel('complete')).toBe('Complete');
    expect(auditStatusLabel('partial')).toBe('Partial');
    expect(auditStatusLabel('failed')).toBe('Failed');
    expect(auditStatusLabel('finalized')).toBe('Complete');
    expect(auditStatusLabel('awaiting_worker')).toBe('awaiting worker');
  });

  it('uses authoritative list counts before fallback aggregates', () => {
    expect(auditFindingCount({ id: 'a', project_id: 'p', status: 'complete', finding_count: 4 })).toBe(4);
    expect(auditFindingCount({ id: 'a', project_id: 'p', status: 'complete', counts: { correct: 2, move_proposed: 1 } })).toBe(3);
    expect(auditFindingCount({ id: 'a', project_id: 'p', status: 'complete', findings: [{ id: 'f' } as never] })).toBe(1);
  });
});
