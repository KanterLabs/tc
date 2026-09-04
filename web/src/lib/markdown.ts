/**
 * Render the small, safe Markdown subset used by activity comments.
 *
 * Comment bodies are intentionally kept as Markdown in the API and database.
 * This renderer escapes the source before applying formatting and only emits
 * links with an explicitly safe URL scheme, so using the result with Svelte's
 * {@html} directive cannot turn a comment into executable markup.
 */
export function renderMarkdown(source: string): string {
  const escaped = escapeHTML(source || '');
  let rendered = escaped;

  rendered = rendered.replace(/^### ([^\n]+)$/gm, '<strong>$1</strong>');
  rendered = rendered.replace(/^## ([^\n]+)$/gm, '<strong>$1</strong>');
  rendered = rendered.replace(/^# ([^\n]+)$/gm, '<strong>$1</strong>');
  rendered = rendered.replace(/`([^`\n]+)`/g, '<code>$1</code>');
  rendered = rendered.replace(/\[([^\]\n]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (match, label: string, href: string) => {
    if (!isSafeURL(href)) return label;
    return `<a href="${escapeHTML(href)}" target="_blank" rel="noopener noreferrer">${label}</a>`;
  });
  rendered = rendered.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
  rendered = rendered.replace(/__([^_\n]+)__/g, '<strong>$1</strong>');
  rendered = rendered.replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, '$1<em>$2</em>');
  rendered = rendered.replace(/(^|[^\w])_([^_\n]+)_(?!\w)/g, '$1<em>$2</em>');
  return rendered.replace(/\n/g, '<br>');
}

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) => {
    switch (character) {
      case '&': return '&amp;';
      case '<': return '&lt;';
      case '>': return '&gt;';
      case '"': return '&quot;';
      case "'": return '&#39;';
      default: return character;
    }
  });
}

function isSafeURL(value: string): boolean {
  try {
    const parsed = new URL(value, 'https://helm.invalid');
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' || parsed.protocol === 'mailto:';
  } catch {
    return false;
  }
}
