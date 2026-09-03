import { describe, expect, it } from 'vitest';
import {
  helmMarkAccessibility,
  helmMarkAccessibleLabel,
  helmMarkCssSize
} from './HelmMark.svelte';

describe('HelmMark', () => {
  it('keeps decorative marks out of the accessibility tree', () => {
    expect(helmMarkAccessibility(true, 'Ignored nearby text')).toEqual({
      role: 'presentation',
      ariaHidden: 'true'
    });
  });

  it('names standalone marks and preserves the requested size', () => {
    expect(helmMarkAccessibility(false, 'Open Helm home')).toEqual({
      role: 'img',
      label: 'Open Helm home'
    });
  });

  it('keeps the wheel geometry code-native and token-friendly', () => {
    expect(helmMarkCssSize('1.25rem')).toBe('1.25rem');
    expect(helmMarkCssSize(20)).toBe('20px');
  });
});

describe('HelmMark helpers', () => {
  it('falls back to a useful standalone name', () => {
    expect(helmMarkAccessibleLabel('  Helm navigation  ')).toBe('Helm navigation');
    expect(helmMarkAccessibleLabel('')).toBe('Helm');
    expect(helmMarkAccessibleLabel()).toBe('Helm');
  });

  it('supports pixel and CSS length sizes', () => {
    expect(helmMarkCssSize(20)).toBe('20px');
    expect(helmMarkCssSize(32)).toBe('32px');
    expect(helmMarkCssSize('1.25rem')).toBe('1.25rem');
    expect(helmMarkCssSize(0)).toBe('32px');
  });
});
