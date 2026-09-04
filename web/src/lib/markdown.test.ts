import { describe, expect, it } from 'vitest';
import { renderMarkdown } from './markdown';

describe('renderMarkdown', () => {
  it('escapes HTML while rendering the supported Markdown subset', () => {
    const rendered = renderMarkdown('<script>alert(1)</script> **bold** and *emphasis*\n`code`');

    expect(rendered).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(rendered).toContain('<strong>bold</strong>');
    expect(rendered).toContain('<em>emphasis</em>');
    expect(rendered).toContain('<code>code</code>');
    expect(rendered).not.toContain('<script>');
  });

  it('allows safe links and strips unsafe link targets', () => {
    const rendered = renderMarkdown('[docs](https://example.com/a) [bad](javascript:alert(1))');

    expect(rendered).toContain('href="https://example.com/a"');
    expect(rendered).toContain('>docs</a>');
    expect(rendered).toContain('bad');
    expect(rendered).not.toContain('javascript:');
  });
});
