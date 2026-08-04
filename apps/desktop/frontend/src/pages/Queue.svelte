<script lang="ts">
  import { jobs, showError } from '../lib/stores.js';
  import { api } from '../lib/api.js';
  import ProgressRow from '../lib/components/ProgressRow.svelte';

  $: ordered = [...($jobs || [])].sort((a, b) => {
    const rank = { active: 0, pending: 1, complete: 2, failed: 3, canceled: 4 };
    return rank[a.status] - rank[b.status];
  });
  $: downloading = ordered.filter((job) => job.status === 'active').length;
  $: completed = ordered.filter((job) => job.status === 'complete').length;
  $: terminal = ordered.filter((job) => ['complete', 'failed', 'canceled'].includes(job.status)).length;

  async function clearCompleted() { try { await api.jobs.clearCompleted(); } catch (err) { showError(err); } }
</script>

<section class="page" aria-labelledby="queue-title">
  <header class="page-header">
    <div><h1 id="queue-title">Queue</h1><p>Manage your download queue. Items will download in order from top to bottom.</p></div>
    <div class="summary" aria-label="Queue summary">
      <div><span>Total</span><strong>{ordered.length}</strong><small>{ordered.length === 1 ? 'item' : 'items'}</small></div>
      <div><span>Downloading</span><strong>{downloading}</strong><small>{downloading === 1 ? 'item' : 'items'}</small></div>
      <div><span>Completed</span><strong>{completed}</strong><small>{completed === 1 ? 'item' : 'items'}</small></div>
    </div>
  </header>

  <section class="table-wrap" aria-label="Download queue">
    <div class="thead" aria-hidden="true"><span>#</span><span>Title</span><span>Quality</span><span>Status</span><span>Progress</span><span>Speed / ETA</span><span>Actions</span></div>
    {#if ordered.length}
      {#each ordered as job, index (job.id)}
        <ProgressRow {job} index={index + 1} />
      {/each}
    {:else}
      <div class="empty">Your queue is empty. Add a video from Home to get started.</div>
    {/if}
  </section>
  <footer class="table-footer">
    <span>{ordered.length} {ordered.length === 1 ? 'item' : 'items'}</span>
    <button type="button" on:click={clearCompleted} disabled={terminal === 0}>⌫&nbsp;&nbsp; Clear Completed</button>
  </footer>
</section>

<style>
  .page { width: min(100%, 1120px); margin: 0 auto; padding: 28px 28px 40px; }
  .page-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 28px; margin-bottom: 22px; }
  h1 { margin: 0; font-size: 30px; letter-spacing: -.02em; }
  .page-header p { margin: 8px 0 0; color: var(--text-secondary); font-size: 16px; }
  .summary { display: grid; grid-template-columns: repeat(3,108px); background: linear-gradient(145deg,#151f2c,#101823); border: 1px solid var(--border-default); border-radius: 10px; padding: 17px 4px; flex-shrink: 0; }
  .summary div { display: flex; flex-direction: column; align-items: center; border-left: 1px solid var(--border-subtle); } .summary div:first-child { border-left: 0; }
  .summary span,.summary small { color: var(--text-secondary); font-size: 12px; } .summary strong { font-size: 24px; margin: 6px 0 2px; }
  .table-wrap { overflow-x: auto; background: linear-gradient(145deg,rgba(20,29,41,.97),rgba(13,21,31,.97)); border: 1px solid var(--border-default); border-radius: 10px; }
  .thead { display: grid; grid-template-columns: 34px minmax(250px,1.45fr) 86px 120px minmax(130px,.8fr) 104px 152px; gap: 12px; min-width: 960px; min-height: 45px; padding: 0 12px; align-items: center; color: var(--text-secondary); font-size: 13px; }
  .thead span:first-child { text-align: center; } .thead span:last-child { text-align: right; }
  .empty { min-width: 720px; min-height: 156px; padding: 64px 24px; text-align: center; color: var(--text-muted); border-top: 1px solid var(--border-subtle); }
  .table-footer { display: flex; justify-content: space-between; align-items: center; margin-top: 16px; color: var(--text-secondary); }
  .table-footer button { min-height: 40px; padding: 0 18px; color: var(--text-primary); background: linear-gradient(180deg,#283445,#1d2734); border: 1px solid var(--border-default); border-radius: 8px; }
  @media (max-width: 900px) { .page-header { flex-direction: column; } }
</style>
