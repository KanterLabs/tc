<script lang="ts">
  import TaskActivityTimeline from './TaskActivityTimeline.svelte';
  import type { Comment, TaskTimelineItem } from '../types';

  interface HarnessProps {
    items?: TaskTimelineItem[];
    currentActorId?: string;
    canManageComments?: boolean;
    onEditComment?: (comment: Comment, body: string) => void | Promise<void>;
    onDeleteComment?: (comment: Comment) => void | Promise<void>;
  }

  let {
    items = [],
    currentActorId = '',
    canManageComments = false,
    onEditComment = () => undefined,
    onDeleteComment = () => undefined
  }: HarnessProps = $props();
  let currentItems = $state(undefined as TaskTimelineItem[] | undefined);
  $effect(() => {
    currentItems = items;
  });

  /** Test-only host control for exercising a live prop replacement in Svelte 5. */
  export function updateItems(next: TaskTimelineItem[]): void {
    currentItems = next;
  }
</script>

<TaskActivityTimeline
  items={currentItems ?? items}
  filter="all"
  {currentActorId}
  {canManageComments}
  {onEditComment}
  {onDeleteComment}
/>
