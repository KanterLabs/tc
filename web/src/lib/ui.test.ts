import { describe, expect, it } from 'vitest';
import {
  isEditableTarget,
  platformIsMac,
  platformShortcut,
  themeFromMediaPreference,
  toastAccessibility
} from './ui';

describe('keyboard and notification UI helpers', () => {
  it('recognizes editable controls without treating ordinary buttons as fields', () => {
    document.body.innerHTML = `
      <input id="input" />
      <textarea id="textarea"></textarea>
      <select id="select"><option>One</option></select>
      <div id="editor" contenteditable="true"><span id="editor-child">Text</span></div>
      <button id="button" type="button">Button</button>
    `;

    expect(isEditableTarget(document.getElementById('input'))).toBe(true);
    expect(isEditableTarget(document.getElementById('textarea'))).toBe(true);
    expect(isEditableTarget(document.getElementById('select'))).toBe(true);
    expect(isEditableTarget(document.getElementById('editor-child'))).toBe(true);
    expect(isEditableTarget(document.getElementById('button'))).toBe(false);
    expect(isEditableTarget(null)).toBe(false);
  });

  it('maps platform identifiers to the matching command shortcut', () => {
    expect(platformIsMac('MacIntel')).toBe(true);
    expect(platformIsMac('macOS')).toBe(true);
    expect(platformIsMac('Linux x86_64')).toBe(false);
    expect(platformShortcut('MacIntel')).toBe('⌘ K');
    expect(platformShortcut('Win32')).toBe('Ctrl K');
    expect(platformShortcut(undefined)).toBe('Ctrl K');
  });

  it('uses the operating-system theme only as a fallback', () => {
    expect(themeFromMediaPreference(true)).toBe('dark');
    expect(themeFromMediaPreference(false)).toBe('light');
  });

  it('keeps error announcements assertive and non-errors polite', () => {
    expect(toastAccessibility('error')).toEqual({ role: 'alert', live: 'assertive' });
    expect(toastAccessibility('success')).toEqual({ role: 'status', live: 'polite' });
    expect(toastAccessibility('info')).toEqual({ role: 'status', live: 'polite' });
  });
});
