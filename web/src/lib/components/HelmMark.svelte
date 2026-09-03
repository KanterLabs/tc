<script context="module" lang="ts">
  /** A CSS length or a pixel value used for the rendered mark. */
  export type HelmMarkSize = number | string;

  export type HelmMarkAccessibility = {
    role: 'presentation' | 'img';
    ariaHidden?: 'true';
    label?: string;
  };

  /**
   * Keep the standalone mark named even when a caller passes an empty label.
   * Adjacent wordmarks should use `decorative` so the text remains the one
   * accessible name exposed to assistive technology.
   */
  export function helmMarkAccessibleLabel(label?: string): string {
    return label?.trim() || 'Helm';
  }

  /** Keep decorative and standalone semantics explicit and testable. */
  export function helmMarkAccessibility(
    decorative = false,
    label?: string
  ): HelmMarkAccessibility {
    if (decorative) return { role: 'presentation', ariaHidden: 'true' };
    return { role: 'img', label: helmMarkAccessibleLabel(label) };
  }

  /** Translate numeric sizes into a CSS length while retaining CSS sizes. */
  export function helmMarkCssSize(size: HelmMarkSize): string {
    if (typeof size === 'number') return Number.isFinite(size) && size > 0 ? `${size}px` : '32px';
    return size.trim() || '32px';
  }
</script>

<script lang="ts">
  /** Render at 20, 32, or 46px by default, while allowing a CSS length. */
  export let size: HelmMarkSize = 32;
  /** Hide the mark from assistive technology when nearby text names it. */
  export let decorative = false;
  /** Accessible name used when the mark is standalone. */
  export let label = 'Helm';
  /** Optional class hook for a consuming layout. */
  export let className = '';

  $: dimension = helmMarkCssSize(size);
  $: accessibility = helmMarkAccessibility(decorative, label);
  $: svgClass = ['helm-mark', className].filter(Boolean).join(' ');
</script>

<svg
  class={svgClass}
  style={`--helm-mark-size: ${dimension};`}
  width={size}
  height={size}
  viewBox="0 0 48 48"
  fill="none"
  xmlns="http://www.w3.org/2000/svg"
  role={accessibility.role}
  aria-hidden={accessibility.ariaHidden}
  aria-label={accessibility.label}
  focusable="false"
  shape-rendering="geometricPrecision"
>
  {#if accessibility.label}<title>{accessibility.label}</title>{/if}

  <!-- Eight spokes and their grips keep the helm recognizable at 20px. -->
  <g stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
    <path d="M24 24V4" />
    <path d="M24 24V44" />
    <path d="M24 24H4" />
    <path d="M24 24H44" />
    <path d="M24 24 9.86 9.86" />
    <path d="M24 24 38.14 9.86" />
    <path d="M24 24 9.86 38.14" />
    <path d="M24 24 38.14 38.14" />
  </g>

  <circle cx="24" cy="24" r="14" stroke="currentColor" stroke-width="3" />

  <g fill="currentColor">
    <circle cx="24" cy="4" r="2.6" />
    <circle cx="24" cy="44" r="2.6" />
    <circle cx="4" cy="24" r="2.6" />
    <circle cx="44" cy="24" r="2.6" />
    <circle cx="9.86" cy="9.86" r="2.6" />
    <circle cx="38.14" cy="9.86" r="2.6" />
    <circle cx="9.86" cy="38.14" r="2.6" />
    <circle cx="38.14" cy="38.14" r="2.6" />
    <circle cx="24" cy="24" r="4.8" />
  </g>
</svg>

<style>
  /*
   * The mark inherits its surrounding color by default. Consumers can set
   * --helm-mark-color when the SVG needs a color independent of its context.
   */
  .helm-mark {
    display: inline-block;
    width: var(--helm-mark-size, 2rem);
    height: var(--helm-mark-size, 2rem);
    flex: 0 0 auto;
    color: var(--helm-mark-color, currentColor);
    vertical-align: middle;
  }
</style>
