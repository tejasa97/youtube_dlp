<script lang="ts">
  import { history, showError } from '../lib/stores.js';
  import { api } from '../lib/api.js';
  import { formatBytes, formatRelative, qualityLabel } from '../lib/format.js';
  import type { HistoryEntry } from '../lib/types.js';

  let query = '';
  let view: 'recent' | 'all' = 'recent';
  let selected: HistoryEntry | null = null;
  $: source = view === 'recent' ? $history.slice(0, 10) : $history;
  $: filtered = source.filter((entry) => [entry.title, entry.channel, entry.filename].some((value) => value.toLowerCase().includes(query.trim().toLowerCase())));
  $: if (!selected || !filtered.some((entry) => entry.id === selected?.id)) selected = filtered[0] || null;

  const open = async (entry: HistoryEntry) => { try { await api.fs.open(entry.absolutePath); } catch (err) { showError(err); } };
  const reveal = async (entry: HistoryEntry) => { try { await api.fs.reveal(entry.absolutePath); } catch (err) { showError(err); } };
</script>

<section class="page" aria-labelledby="downloads-title">
  <header class="page-header"><h1 id="downloads-title">Downloads</h1><p>View your recently downloaded items.</p></header>

  <div class="toolbar">
    <label class="search"><span aria-hidden="true">⌕</span><input type="search" bind:value={query} placeholder="Search downloads…" aria-label="Search downloads" /></label>
    <div class="tabs" role="tablist" aria-label="Download history range">
      <button class:active={view === 'recent'} role="tab" aria-selected={view === 'recent'} on:click={() => view = 'recent'}>Recent</button>
      <button class:active={view === 'all'} role="tab" aria-selected={view === 'all'} on:click={() => view = 'all'}>All</button>
    </div>
  </div>

  <div class="table" aria-label="Downloaded videos">
    <div class="thead"><span>Title</span><span>Format</span><span>Size</span><span>Finished</span><span>Actions</span></div>
    {#each filtered as entry (entry.id)}
      <div class="tr" class:selected={selected?.id === entry.id}>
        <button class="title-cell" type="button" aria-label={`Select ${entry.title}`} on:click={() => selected = entry}>
          <span class="thumb">{#if entry.thumbnail}<img src={entry.thumbnail} alt="" />{/if}</span>
          <span class="copy"><strong title={entry.title}>{entry.title}</strong><small>{entry.channel || 'YouTube'}</small></span>
        </button>
        <span><em>{qualityLabel(entry.quality)}</em></span>
        <span>{formatBytes(entry.sizeBytes)}</span>
        <span>{formatRelative(entry.completedAt)}</span>
        <span class="row-actions">
          <button type="button" aria-label="Open downloaded file" on:click={() => open(entry)}>↗&nbsp; Open</button>
          <button type="button" aria-label="Show in Finder" on:click={() => reveal(entry)}>▭</button>
        </span>
      </div>
    {/each}
    {#if filtered.length === 0}<div class="empty">{query ? 'No downloads match your search.' : 'No downloads yet.'}</div>{/if}
  </div>

  {#if selected}
    <section class="detail-card" aria-label="Selected download">
      <div class="detail-thumb">{#if selected.thumbnail}<img src={selected.thumbnail} alt="" />{/if}{#if selected.durationLabel}<span>{selected.durationLabel}</span>{/if}</div>
      <div class="detail-copy">
        <h2 title={selected.title}>{selected.title}</h2>
        <p>{selected.channel || 'YouTube'}</p>
        <div class="facts"><span>{qualityLabel(selected.quality)}</span><span>·</span><span>{formatBytes(selected.sizeBytes)}</span><span>·</span><span>{formatRelative(selected.completedAt)}</span></div>
        <div class="location"><span class="location-label">File location:</span><span title={selected.absolutePath}>▭ &nbsp;{selected.absolutePath}</span><button type="button" on:click={() => reveal(selected!)}>Show in Finder</button></div>
      </div>
    </section>
  {/if}
</section>

<style>
  .page { width: min(100%,1120px); margin: 0 auto; padding: 28px 28px 38px; }
  h1 { margin: 0; font-size: 30px; letter-spacing: -.02em; } .page-header p { margin: 8px 0 20px; color: var(--text-secondary); font-size: 16px; }
  .toolbar { display: flex; justify-content: space-between; align-items: center; gap: 18px; margin-bottom: 12px; }
  .search { position: relative; width: min(540px, 65%); } .search > span { position: absolute; left: 14px; top: 8px; color: var(--text-secondary); font-size: 22px; z-index: 1; }
  .search input { height: 42px; padding-left: 42px; font-size: 15px; }
  .tabs { display: grid; grid-template-columns: 84px 84px; border: 1px solid var(--border-default); border-radius: 8px; padding: 3px; }
  .tabs button { height: 34px; border-radius: 6px; color: var(--text-secondary); } .tabs button.active { color:#fff; background: linear-gradient(180deg,#347cf4,#2867d8); }
  .table { overflow-x: auto; background: linear-gradient(145deg,rgba(20,29,41,.97),rgba(13,21,31,.97)); border: 1px solid var(--border-default); border-radius: 10px; }
  .thead,.tr { display: grid; grid-template-columns: minmax(320px,1.35fr) 130px 110px 130px 190px; gap: 12px; min-width: 850px; align-items: center; }
  .thead { padding: 10px 14px; color: var(--text-secondary); font-size: 13px; }
  .tr { width: 100%; min-height: 72px; padding: 8px 14px; text-align: left; border-top: 1px solid var(--border-subtle); color: var(--text-secondary); }
  .tr:hover,.tr.selected { background: rgba(42,57,76,.3); }
  .title-cell { display: grid; grid-template-columns: 80px minmax(0,1fr); align-items:center; gap: 12px; min-width:0; }
  .thumb { width: 80px; aspect-ratio: 16/9; border-radius: 6px; overflow:hidden; background:var(--surface-sunken); } .thumb img { width:100%;height:100%;object-fit:cover; }
  .copy { min-width:0; } .copy strong,.copy small { display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; } .copy strong { color:var(--text-primary);font-weight:500; } .copy small { color:var(--accent-400);margin-top:5px; }
  .tr em { font-style:normal; color:var(--text-primary); border:1px solid var(--border-default); border-radius:6px; padding:5px 7px; }
  .row-actions { display:flex; justify-content:flex-end; gap:8px; } .row-actions button,.location button { min-height:34px;padding:0 12px;color:var(--text-primary);background:linear-gradient(180deg,#283445,#1d2734);border:1px solid var(--border-default);border-radius:7px; }
  .empty { padding:70px 20px;text-align:center;color:var(--text-muted);border-top:1px solid var(--border-subtle); }
  .detail-card { display:grid;grid-template-columns:180px minmax(0,1fr);gap:20px;margin-top:14px;padding:10px;background:linear-gradient(145deg,#151f2c,#101823);border:1px solid var(--border-default);border-radius:10px;min-width:0; }
  .detail-thumb { position:relative;aspect-ratio:16/10;border-radius:7px;overflow:hidden;background:var(--surface-sunken); } .detail-thumb img { width:100%;height:100%;object-fit:cover; } .detail-thumb span { position:absolute;right:6px;bottom:6px;background:rgba(0,0,0,.8);padding:2px 6px;border-radius:4px; }
  .detail-copy { min-width:0;padding:4px 2px; } .detail-copy h2 { margin:0;font-size:17px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap; } .detail-copy p { margin:4px 0;color:var(--accent-400); } .facts { display:flex;gap:9px;color:var(--text-secondary);font-size:13px; }
  .location { display:grid;grid-template-columns:auto minmax(0,1fr) auto;gap:10px;align-items:center;margin-top:14px;min-width:0; } .location .location-label { color:var(--text-secondary);padding:0;border:0; } .location > span { min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;padding:7px 10px;border:1px solid var(--border-subtle);border-radius:7px;color:var(--text-secondary); }
  @media(max-width:800px){.detail-card{grid-template-columns:1fr}.detail-thumb{max-width:240px}.location{grid-template-columns:1fr}.search{width:100%}}
</style>
