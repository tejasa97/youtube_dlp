<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../lib/api.js';
  import { settings, ffmpeg, showBanner, showError } from '../lib/stores.js';

  let folder = '';
  let ffmpegPath = '';
  let pickingFolder = false;
  let locating = false;

  onMount(() => {
    folder = $settings.downloadFolder || '';
    ffmpegPath = $settings.ffmpegPath || $ffmpeg.path || '';
  });
  $: displayedFFmpegPath = ffmpegPath || $ffmpeg.path || '';

  async function pickFolder() {
    pickingFolder = true;
    try {
      const path = await api.folder.pick();
      if (!path) return;
      const updated = await api.settings.update({ ...$settings, downloadFolder: path });
      settings.set(updated);
      folder = updated.downloadFolder;
      showBanner('success', 'Default download folder updated');
    } catch (err) { showError(err, 'Could not choose folder'); }
    finally { pickingFolder = false; }
  }

  async function locateFFmpeg() {
    locating = true;
    try {
      const path = await api.ffmpeg.pickPath();
      if (!path) return;
      const status = await api.ffmpeg.configure(path);
      ffmpeg.set(status);
      ffmpegPath = status.path;
      showBanner('success', 'FFmpeg configured');
    } catch (err) { showError(err, 'Could not configure FFmpeg'); }
    finally { locating = false; }
  }

  async function copyDiagnostics() {
    try {
      await api.diagnostics.copy();
      showBanner('info', 'Diagnostics copied to clipboard');
    } catch (err) { showError(err, 'Could not copy diagnostics'); }
  }
</script>

<section class="page" aria-labelledby="settings-title">
  <header class="page-header"><h1 id="settings-title">Settings</h1><p>Configure download and tool settings.</p></header>

  <section class="card" aria-labelledby="folder-title">
    <h2 id="folder-title">Default download folder</h2>
    <p>Downloads will be saved to this folder by default.</p>
    <div class="control-row"><input readonly value={folder} title={folder} aria-label="Default download folder" /><button type="button" on:click={pickFolder} disabled={pickingFolder}>Choose…</button></div>
  </section>

  <section class="card status-card" aria-labelledby="ffmpeg-title">
    <h2 id="ffmpeg-title">FFmpeg</h2>
    <p>Required for some downloads and audio extraction.</p>
    <strong class:detected={$ffmpeg.available} class:missing={!$ffmpeg.available}>{$ffmpeg.available ? '●  Detected' : '●  Required'}</strong>
    <span>{$ffmpeg.available ? 'FFmpeg is installed and ready to use.' : 'FFmpeg is required for certain downloads and audio conversion.'}</span>
  </section>

  <section class="card" aria-labelledby="path-title">
    <h2 id="path-title">FFmpeg path</h2>
    <p>Path to the FFmpeg executable.</p>
    <div class="control-row"><input readonly value={displayedFFmpegPath} title={displayedFFmpegPath} aria-label="FFmpeg path" placeholder="FFmpeg has not been located" /><button type="button" on:click={locateFFmpeg} disabled={locating}>Locate…</button></div>
  </section>

  <section class="card diagnostics-card" aria-labelledby="diagnostics-title">
    <div><h2 id="diagnostics-title">Diagnostics</h2><p>Copy a privacy-safe system summary for troubleshooting.</p></div>
    <button type="button" on:click={copyDiagnostics}>Copy Diagnostics</button>
  </section>
</section>

<style>
  .page { width:min(100%,1040px);margin:0 auto;padding:36px 30px 48px;display:flex;flex-direction:column;gap:18px; }
  h1 { margin:0;font-size:30px;letter-spacing:-.02em; } .page-header p { margin:8px 0 8px;color:var(--text-secondary);font-size:16px; }
  .card { padding:22px;background:linear-gradient(145deg,#151f2c,#101823);border:1px solid var(--border-default);border-radius:10px; }
  .card h2 { margin:0;font-size:18px; } .card > p { margin:6px 0 16px;color:var(--text-secondary);font-size:15px; }
  .control-row { display:grid;grid-template-columns:minmax(0,1fr) 120px;gap:14px;min-width:0; }
  .control-row input { min-width:0;height:48px;font-size:16px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;background:rgba(13,20,30,.7); }
  .control-row button { color:var(--text-primary);background:linear-gradient(180deg,#2a3545,#1d2734);border:1px solid var(--border-default);border-radius:8px;font-size:16px; }
  .status-card strong { display:block;margin:4px 0 10px;font-size:18px; }.status-card .detected{color:#59c96b}.status-card .missing{color:var(--status-danger)}
  .status-card > span { color:var(--text-secondary);font-size:16px; }
  .diagnostics-card { display:flex;align-items:center;justify-content:space-between;gap:20px; }
  .diagnostics-card > div { min-width:0; }
  .diagnostics-card p { margin:6px 0 0;color:var(--text-secondary);font-size:15px; }
  .diagnostics-card button { min-height:44px;padding:0 18px;white-space:nowrap;color:var(--text-primary);background:linear-gradient(180deg,#2a3545,#1d2734);border:1px solid var(--border-default);border-radius:8px;font-size:15px; }
</style>
