<script lang="ts">
  import { onMount, createEventDispatcher } from 'svelte';
  import { api } from '../lib/api.js';
  import { ffmpeg, history, jobs, modal, settings, showBanner } from '../lib/stores.js';
  import { formatRelative, qualityLabel } from '../lib/format.js';
  import { QUALITIES, type InfoSummary, type Quality, type UrlCheckResult } from '../lib/types.js';

  const dispatch = createEventDispatcher<{ goto: 'home' | 'queue' | 'downloads' | 'settings' }>();
  let url = '';
  let busy = false;
  let preview: InfoSummary | null = null;
  let unsupported: { url: string; reason: string } | null = null;
  let quality: Quality = 'best';
  let folder = '';

  const qualityName = (value: Quality) => value === '4k' ? '4K' : value === 'audio' ? 'Audio only' : value === 'best' ? 'Best' : value;
  $: folder = $settings.downloadFolder || folder;
  $: recent = $history.slice(0, 3);
  $: activeJob = $jobs.find((job) => job.status === 'active');

  onMount(async () => {
    folder = $settings.downloadFolder || '';
    const status = await api.ffmpeg.status();
    if (!status.available) {
      modal.set({
        kind: 'ffmpeg-missing',
        title: 'FFmpeg Required',
        message: 'FFmpeg is required for certain downloads and audio extraction or conversion.',
        actions: [
          { label: 'Locate FFmpeg', primary: true, action: () => dispatch('goto', 'settings') },
          { label: 'Installation Guide', action: () => window.runtime.BrowserOpenURL('https://ffmpeg.org/download.html') },
        ],
      });
    }
  });

  async function analyse() {
    if (!url.trim()) return;
    busy = true;
    preview = null;
    unsupported = null;
    try {
      let result: UrlCheckResult;
      try {
        result = await api.validation.url(url);
      } catch {
        unsupported = { url: url.trim(), reason: 'Unsupported website' };
        return;
      }
      url = result.url;
      try {
        preview = await api.analyse.url(result.url);
      } catch (err) {
        modal.set({
          kind: 'error',
          title: 'We could not analyze this video',
          message: err instanceof Error ? err.message : 'The video could not be analyzed. Try again in a moment.',
        });
      }
    } finally {
      busy = false;
    }
  }

  async function pickFolder() {
    const path = await api.folder.pick();
    if (!path) return;
    const updated = await api.settings.update({ ...$settings, downloadFolder: path });
    settings.set(updated);
    folder = updated.downloadFolder;
  }

  async function start(mode: 'now' | 'queue') {
    if (!preview || !folder) return;
    try {
      await api.jobs.start({
        url: preview.url, videoId: preview.videoId, title: preview.title,
        channel: preview.channel, quality, outputDir: folder,
        duration: preview.duration, thumbnail: preview.thumbnail,
      });
      showBanner('success', mode === 'now' ? 'Download started' : 'Added to queue');
      if (mode === 'now') dispatch('goto', 'queue');
    } catch (err) {
      modal.set({
        kind: 'error', title: 'Download could not start',
        message: err instanceof Error ? err.message : 'Could not start the download.',
      });
    }
  }

  function reset() {
    url = '';
    preview = null;
    unsupported = null;
  }

  async function copyDiagnostics() {
    await api.diagnostics.copy();
    showBanner('info', 'Diagnostics copied to clipboard');
  }
</script>

<section class="page" aria-labelledby="home-title">
  <header class="page-header">
    <h1 id="home-title">Home</h1>
    <p>Paste a public YouTube URL and choose how to download it.</p>
  </header>

  <div class="url-row">
    <label class="visually-hidden" for="video-url">YouTube URL</label>
    <input id="video-url" type="url" bind:value={url} placeholder="https://www.youtube.com/watch?v=…" on:keydown={(event) => event.key === 'Enter' && analyse()} />
    <button class="btn primary analyze" type="button" on:click={analyse} disabled={busy}>{busy ? 'Analyzing…' : 'Analyze'}</button>
  </div>

  {#if unsupported}
    <section class="unsupported" aria-labelledby="unsupported-title">
      <div class="warning-art" aria-hidden="true">
        <svg class="unsupported-illustration" viewBox="0 0 320 270" focusable="false">
          <defs>
            <linearGradient id="warning-fill" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#FFD3BF"/><stop offset="1" stop-color="#E78368"/></linearGradient>
            <linearGradient id="cloud-fill" x1="0" y1="0" x2="0" y2="1"><stop stop-color="#667284"/><stop offset="1" stop-color="#414B5C"/></linearGradient>
            <filter id="warning-shadow" x="-30%" y="-30%" width="160%" height="180%"><feDropShadow dx="0" dy="10" stdDeviation="8" flood-color="#05080D" flood-opacity=".48"/></filter>
          </defs>
          <ellipse cx="160" cy="241" rx="94" ry="11" fill="#0A1019" opacity=".62"/>
          <path d="M20 143c0-15 12-27 27-27 7-15 20-23 35-23 22 0 39 18 39 40h5c12 0 22 10 22 22H20z" fill="url(#cloud-fill)" opacity=".83"/>
          <path d="M188 86c0-14 11-25 25-25 6-16 20-27 37-27 23 0 42 19 42 42h3c11 0 19 9 19 20h-126z" fill="url(#cloud-fill)" opacity=".9"/>
          <g fill="#A7B1C0">
            <path d="M42 47h3v9h-3zM39 50h9v3h-9z"/><path d="M81 64h2v7h-2zM78.5 66.5h7v2h-7z"/>
            <path d="M252 129h3v10h-3zM248.5 132.5h10v3h-10z"/><path d="M286 165h2v7h-2zM283.5 167.5h7v2h-7z"/>
            <circle cx="29" cy="78" r="2"/><circle cx="64" cy="26" r="1.5"/><circle cx="271" cy="105" r="1.5"/>
          </g>
          <path d="M159 56c6 0 11 3 15 10l76 139c5 10-1 22-13 22H81c-12 0-18-12-13-22l77-139c3-7 8-10 14-10z" fill="#C96352" filter="url(#warning-shadow)"/>
          <path d="M159 65c5 0 9 3 12 8l73 134c3 6-1 13-8 13H82c-7 0-11-7-8-13l73-134c3-5 7-8 12-8z" fill="url(#warning-fill)" stroke="#F29B80" stroke-width="4"/>
          <path d="M159 91c7 0 11 5 10 12l-4 48c0 5-2 8-6 8s-6-3-6-8l-4-48c-1-7 3-12 10-12z" fill="#55271F"/>
          <circle cx="159" cy="174" r="8" fill="#55271F"/>
          <circle cx="131" cy="195" r="7" fill="#55271F"/><circle cx="187" cy="195" r="7" fill="#55271F"/>
          <path d="M147 214c7-8 17-8 24 0" fill="none" stroke="#55271F" stroke-width="5" stroke-linecap="round"/>
        </svg>
      </div>
      <div class="unsupported-copy">
        <h2 id="unsupported-title">We couldn’t analyze this URL</h2>
        <p>This app currently supports single public YouTube videos.</p>
        <p>The URL you entered appears to be unsupported or unavailable.</p>
        <div class="details">
          <h3>Details</h3>
          <div><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M10 13a5 5 0 0 0 7.1.1l2-2a5 5 0 0 0-7.1-7.1l-1.1 1.1M14 11a5 5 0 0 0-7.1-.1l-2 2A5 5 0 0 0 12 20l1.1-1.1"/></svg><span>URL</span><strong title={unsupported.url}>{unsupported.url}</strong></div>
          <div><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7h.01"/></svg><span>Reason</span><strong>{unsupported.reason}</strong></div>
          <div><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg><span>Time</span><strong>{new Date().toLocaleString()}</strong></div>
        </div>
      </div>
      <div class="unsupported-actions">
        <button class="btn primary" type="button" on:click={reset}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.3 5.7M20 5v6h-6"/></svg>Try another URL</button>
        <button class="btn secondary" type="button" on:click={copyDiagnostics}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 5h6v3H9zM7 7H5v14h14V7h-2"/></svg>Copy Diagnostics</button>
        <button class="btn secondary" type="button" on:click={() => window.runtime.BrowserOpenURL(unsupported?.url || '')}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6M20 4l-9 9M18 13v7H4V6h7"/></svg>Open in Browser</button>
      </div>
    </section>
  {:else if preview}
    <div class="home-grid">
      <section class="video-card" aria-label="Analyzed video">
        <div class="video-summary">
          <div class="preview-thumb">
            {#if preview.thumbnail}<img src={preview.thumbnail} alt="" referrerpolicy="no-referrer" />{/if}
            {#if preview.duration}<span>{preview.duration}</span>{/if}
          </div>
          <div class="preview-meta">
            <h2 title={preview.title}>{preview.title}</h2>
            <p class="channel">{preview.channel || 'YouTube'}</p>
            <p class="meta-line">◷ {preview.duration || 'Duration unavailable'}</p>
            <div class="tags"><span>Video</span><span class="public">◉ Public</span></div>
          </div>
        </div>

        <div class="configuration">
          <div class="destination-row">
            <label for="folder">Destination</label>
            <input id="folder" readonly value={folder} title={folder} />
            <button class="btn secondary" type="button" on:click={pickFolder}>Choose…</button>
          </div>
          <div class="quality-row">
            <span>Quality</span>
            <div class="quality-list" role="radiogroup" aria-label="Quality preset">
              {#each QUALITIES as option}
                <button type="button" role="radio" aria-checked={quality === option} class:active={quality === option} on:click={() => quality = option}>{qualityName(option)}</button>
              {/each}
            </div>
          </div>
          <div class="actions">
            <button class="btn primary" type="button" on:click={() => start('now')}>↓ <span>Download</span></button>
            <button class="btn secondary" type="button" on:click={() => start('queue')}>☷ <span>Add to Queue</span></button>
          </div>
        </div>
      </section>

      <aside class="side-stack">
        <section class="side-card">
          <h2>FFmpeg</h2>
          <p class:ok={$ffmpeg.available} class:bad={!$ffmpeg.available}>{$ffmpeg.available ? '● Detected' : '● Required'}</p>
          <span>{$ffmpeg.available ? 'FFmpeg is installed and ready to use.' : 'FFmpeg is required for certain downloads and audio conversion.'}</span>
        </section>
        <section class="side-card activity">
          <h2>Recent Activity</h2>
          <div><span>↧ Analyzed video</span><small>Just now</small></div>
          <div><span>☷ Added to queue</span><small>{activeJob ? 'Active' : '—'}</small></div>
          <div><span>↓ Downloaded</span><small>{recent[0] ? formatRelative(recent[0].completedAt) : '—'}</small></div>
          <button type="button" on:click={() => dispatch('goto', 'queue')}>View all activity</button>
        </section>
      </aside>
    </div>
  {:else}
    <div class="empty-state">Paste a supported YouTube URL above to see its preview.</div>
  {/if}

  <section class="recent-downloads">
    <header><h2>Recent Downloads</h2><button type="button" on:click={() => dispatch('goto', 'downloads')}>View All</button></header>
    {#if recent.length}
      <div class="recent-grid">
        {#each recent as entry}
          <article>
            <div class="recent-thumb">{#if entry.thumbnail}<img src={entry.thumbnail} alt="" />{/if}</div>
            <div><strong title={entry.title}>{entry.title}</strong><span>● Completed&nbsp; · &nbsp;{qualityLabel(entry.quality)}</span></div>
          </article>
        {/each}
      </div>
    {:else}
      <p>No downloads yet. Analyze a supported YouTube URL to get started.</p>
    {/if}
  </section>
</section>

<style>
  .page { width: min(100%, 1120px); margin: 0 auto; padding: 26px 28px 32px; display: flex; flex-direction: column; gap: 18px; }
  .page-header h1 { margin: 0; font-size: 30px; line-height: 1.15; letter-spacing: -.02em; }
  .page-header p { margin: 7px 0 0; color: var(--text-secondary); font-size: 16px; }
  .url-row { display: grid; grid-template-columns: minmax(0, 1fr) 124px; gap: 12px; max-width: 760px; }
  .url-row input { height: 46px; font-size: 16px; }
  .btn { min-height: 42px; padding: 0 18px; border-radius: var(--r-md); display: inline-flex; align-items: center; justify-content: center; gap: 9px; font-weight: 500; white-space: nowrap; }
  .btn.primary { color: #fff; background: linear-gradient(180deg, #347cf4, #2867d8); box-shadow: inset 0 1px rgba(255,255,255,.18); }
  .btn.secondary { color: var(--text-primary); background: linear-gradient(180deg, #263142, #1c2532); border: 1px solid var(--border-default); }
  .home-grid { display: grid; grid-template-columns: minmax(0, 1fr) 270px; gap: 18px; min-width: 0; }
  .video-card, .side-card, .recent-downloads, .unsupported { background: linear-gradient(145deg, rgba(22,31,44,.96), rgba(14,22,33,.96)); border: 1px solid var(--border-default); border-radius: var(--r-lg); }
  .video-card { min-width: 0; overflow: hidden; }
  .video-summary { display: grid; grid-template-columns: 224px minmax(0,1fr); gap: 20px; padding: 16px; }
  .preview-thumb { position: relative; aspect-ratio: 16/9; border-radius: 8px; overflow: hidden; background: var(--surface-sunken); }
  .preview-thumb img { width: 100%; height: 100%; object-fit: cover; }
  .preview-thumb span { position: absolute; right: 6px; bottom: 6px; padding: 2px 6px; border-radius: 4px; background: rgba(0,0,0,.78); }
  .preview-meta { min-width: 0; }
  .preview-meta h2 { margin: 5px 0 14px; font-size: 18px; line-height: 1.35; overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; }
  .preview-meta p { margin: 0 0 12px; }
  .channel { color: var(--accent-400); font-weight: 600; }
  .meta-line { color: var(--text-secondary); }
  .tags { display: flex; gap: 8px; }
  .tags span { padding: 5px 10px; border-radius: 6px; background: #17273b; color: var(--accent-400); }
  .tags .public { color: #57c86b; background: rgba(64,168,84,.12); }
  .configuration { border-top: 1px solid var(--border-subtle); padding: 14px 16px 16px; display: flex; flex-direction: column; gap: 14px; }
  .destination-row { display: grid; grid-template-columns: 76px minmax(0,1fr) auto; gap: 10px; align-items: center; min-width: 0; }
  .destination-row input { min-width: 0; height: 38px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .quality-row { display: grid; grid-template-columns: 66px minmax(0,1fr); gap: 10px; align-items: center; min-width: 0; }
  .quality-list { display: grid; grid-template-columns: repeat(6, minmax(72px,1fr)); min-width: 0; border: 1px solid var(--border-default); border-radius: 9px; padding: 3px; overflow-x: auto; }
  .quality-list button { min-height: 34px; padding: 0 10px; color: var(--text-secondary); border-radius: 6px; white-space: nowrap; }
  .quality-list button.active { background: linear-gradient(180deg, #347cf4, #2867d8); color: #fff; }
  .actions { display: flex; gap: 12px; }
  .actions .btn { min-width: 174px; }
  .side-stack { display: flex; flex-direction: column; gap: 18px; min-width: 0; }
  .side-card { padding: 17px; }
  .side-card h2 { margin: 0 0 10px; font-size: 17px; }
  .side-card p { margin: 0 0 9px; font-weight: 600; }
  .side-card span { color: var(--text-secondary); line-height: 1.5; }
  .side-card .ok { color: #59c96b; } .side-card .bad { color: var(--status-danger); }
  .activity div { display: flex; justify-content: space-between; gap: 10px; padding: 9px 0; }
  .activity small { color: var(--text-secondary); white-space: nowrap; }
  .activity button, .recent-downloads button { color: var(--accent-400); text-align: left; margin-top: 7px; }
  .recent-downloads { padding: 14px 16px; }
  .recent-downloads header { display: flex; align-items: center; justify-content: space-between; }
  .recent-downloads h2 { margin: 0; font-size: 17px; }
  .recent-grid { display: grid; grid-template-columns: repeat(3,minmax(0,1fr)); gap: 12px; margin-top: 14px; }
  .recent-grid article { display: grid; grid-template-columns: 62px minmax(0,1fr); gap: 10px; padding: 10px; border: 1px solid var(--border-subtle); border-radius: 8px; min-width: 0; }
  .recent-thumb { width: 62px; aspect-ratio: 16/11; border-radius: 6px; overflow: hidden; background: var(--surface-sunken); }
  .recent-thumb img { width: 100%; height: 100%; object-fit: cover; }
  .recent-grid strong, .recent-grid span { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .recent-grid span { color: #59c96b; margin-top: 6px; font-size: 12px; }
  .recent-downloads > p { text-align: center; color: var(--text-muted); margin: 20px 0 4px; }
  .empty-state { min-height: 260px; display: grid; place-items: center; color: var(--text-muted); border: 1px dashed var(--border-default); border-radius: var(--r-lg); }
  .unsupported { display: grid; grid-template-columns: 300px minmax(0,1fr); gap: 22px; padding: 36px; }
  .warning-art { min-height: 270px; display: grid; place-items: center; }
  .unsupported-illustration { width: min(100%, 310px); height: auto; overflow: visible; }
  .unsupported-copy h2 { margin: 8px 0 12px; font-size: 26px; }
  .unsupported-copy > p { margin: 7px 0; color: var(--text-secondary); font-size: 16px; }
  .details { margin-top: 24px; border: 1px solid var(--border-subtle); border-radius: 10px; padding: 16px 18px; }
  .details h3 { margin: 0 0 10px; }
  .details div { display: grid; grid-template-columns: 24px 100px minmax(0,1fr); gap: 12px; padding: 11px 0; border-top: 1px solid var(--border-subtle); align-items: center; }
  .details svg { width: 20px; height: 20px; fill: none; stroke: var(--text-secondary); stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
  .details span { color: var(--text-secondary); } .details strong { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 400; text-align: right; }
  .unsupported-actions { grid-column: 1/-1; display: flex; justify-content: center; gap: 14px; }
  .unsupported-actions .btn { min-width: 200px; }
  .unsupported-actions svg { width: 20px; height: 20px; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
  .visually-hidden { position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0; }
  @media (max-width: 1050px) { .home-grid { grid-template-columns: 1fr; } .side-stack { display: grid; grid-template-columns: 1fr 1fr; } .recent-grid { grid-template-columns: 1fr; } }
  @media (max-width: 780px) { .video-summary, .unsupported { grid-template-columns: 1fr; } .quality-list { grid-template-columns: repeat(6, 90px); } .unsupported-actions { flex-wrap: wrap; } }
</style>
