<script lang="ts">
  import type { JobSnapshot } from '../types.js';
  import { formatEta, formatProgress, formatSpeed, progressOf } from '../format.js';
  import { api } from '../api.js';
  import { showBanner, showError } from '../stores.js';
  import StatusBadge from './StatusBadge.svelte';

  export let job: JobSnapshot;
  export let index = 1;
  $: progress = progressOf(job);
  $: progressLabel = job.status === 'complete' ? '100%' : job.total > 0 ? formatProgress(progress) : '—';
  let pointerAction = '';

  function activateOnPointer(name: string, action: () => Promise<void>) {
    pointerAction = name;
    void action();
  }

  function activateOnClick(name: string, action: () => Promise<void>) {
    if (pointerAction === name) {
      pointerAction = '';
      return;
    }
    void action();
  }

  async function cancelJob() {
    try { await api.jobs.cancel(job.id); }
    catch (err) { showError(err, 'Could not cancel the download'); }
  }

  async function retryJob() {
    try { await api.jobs.retry(job.id); showBanner('info', 'Retry added'); }
    catch (err) { showError(err, 'Could not retry the download'); }
  }

  async function removeJob() {
    try { await api.jobs.remove(job.id); }
    catch (err) { showError(err, 'Could not remove the download'); }
  }

  async function openJob() {
    if (!job.absolutePath) return;
    try { await api.fs.open(job.absolutePath); }
    catch (err) { showError(err, 'Could not open the file'); }
  }
</script>

<div class="queue-row">
  <div class="number">{index}</div>
  <div>
    <div class="title-cell">
      <div class="thumb">{#if job.thumbnail}<img src={job.thumbnail} alt="" referrerpolicy="no-referrer" />{/if}</div>
      <div class="title-copy">
        <strong title={job.title || job.filename}>{job.title || job.filename || 'Untitled video'}</strong>
        <span>{job.channel || 'YouTube'}{job.durationLabel ? ' · ' + job.durationLabel : ''}</span>
      </div>
    </div>
  </div>
  <div><span class="quality">{job.qualityLabel || job.quality}</span></div>
  <div><StatusBadge status={job.status} compact /></div>
  <div>
    <div class="progress-cell">
      <span>{progressLabel}</span>
      {#if job.total > 0 || job.status === 'complete'}
        <div class="track"><div class:complete={job.status === 'complete'} style="width:{Math.round(progress * 100)}%"></div></div>
      {:else if job.status === 'failed'}
        <small title={job.message}>{job.message || 'Download failed'}</small>
      {/if}
    </div>
  </div>
  <div>
    <div class="speed">
      {#if job.status === 'active' && job.speedBps > 0}<span>{formatSpeed(job.speedBps)}</span><small>{job.etaSeconds > 0 ? `ETA ${formatEta(job.etaSeconds)}` : '—'}</small>{:else}<span>—</span>{/if}
    </div>
  </div>
  <div class="actions-cell">
    <div class="actions">
      {#if job.status === 'active' || job.status === 'pending'}
        <button class="row-action" type="button" aria-label="Cancel download" on:pointerdown|preventDefault|stopPropagation={() => activateOnPointer('cancel', cancelJob)} on:click|stopPropagation={() => activateOnClick('cancel', cancelJob)}>Cancel</button>
      {:else if job.status === 'complete'}
        <button class="row-action" type="button" aria-label="Open downloaded file" on:pointerdown|preventDefault|stopPropagation={() => activateOnPointer('open', openJob)} on:click|stopPropagation={() => activateOnClick('open', openJob)}>Open</button>
        <button class="row-action" type="button" aria-label="Remove completed download" on:pointerdown|preventDefault|stopPropagation={() => activateOnPointer('remove-complete', removeJob)} on:click|stopPropagation={() => activateOnClick('remove-complete', removeJob)}>Remove</button>
      {:else if job.status === 'failed' || job.status === 'canceled'}
        <button class="row-action" type="button" aria-label="Retry download" on:pointerdown|preventDefault|stopPropagation={() => activateOnPointer('retry', retryJob)} on:click|stopPropagation={() => activateOnClick('retry', retryJob)}>Retry</button>
        <button class="row-action" type="button" aria-label="Remove download" on:pointerdown|preventDefault|stopPropagation={() => activateOnPointer('remove-terminal', removeJob)} on:click|stopPropagation={() => activateOnClick('remove-terminal', removeJob)}>Remove</button>
      {/if}
    </div>
  </div>
</div>

<style>
  .queue-row { display: grid; grid-template-columns: 34px minmax(250px,1.45fr) 86px 120px minmax(130px,.8fr) 104px 152px; gap: 12px; min-width: 960px; align-items: center; min-height: 88px; padding: 10px 12px; border-top: 1px solid var(--border-subtle); }
  .number { font-weight: 600; text-align: center; }
  .title-cell { display: grid; grid-template-columns: 62px minmax(0,1fr); align-items: center; gap: 12px; min-width: 0; }
  .thumb { width: 62px; aspect-ratio: 16/10; border-radius: 7px; overflow: hidden; background: var(--surface-sunken); }
  .thumb img { width: 100%; height: 100%; object-fit: cover; }
  .title-copy { min-width: 0; }
  .title-copy strong, .title-copy span { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .title-copy strong { font-size: 14px; font-weight: 500; }
  .title-copy span { color: var(--text-secondary); margin-top: 6px; font-size: 12px; }
  .quality { display: inline-flex; padding: 5px 8px; color: var(--accent-400); background: rgba(47,114,237,.1); border: 1px solid rgba(96,165,250,.24); border-radius: 6px; }
  .progress-cell { min-width: 0; }
  .progress-cell > span { display: block; margin-bottom: 7px; }
  .progress-cell small { color: var(--status-danger); display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .track { width: 100%; height: 6px; border-radius: 99px; background: #253143; overflow: hidden; }
  .track > div { height: 100%; border-radius: inherit; background: var(--accent-500); }
  .track > div.complete { background: #59c96b; }
  .speed span, .speed small { display: block; } .speed small { color: var(--text-secondary); margin-top: 5px; }
  .actions-cell { min-width: 0; }
  .actions { display: flex; justify-content: flex-end; gap: 6px; }
  .row-action { position: relative; z-index: 1; min-height: 36px; padding: 0 11px; color: var(--text-primary); background: linear-gradient(180deg,#283445,#1d2734); border: 1px solid var(--border-default); border-radius: 7px; pointer-events: auto; }
  .row-action:hover { background: var(--surface-active); border-color: var(--border-strong); }
</style>
