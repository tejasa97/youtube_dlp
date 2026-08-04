<script lang="ts">
  import { modal } from '../stores.js';
  function close() { modal.set(null); }
  $: current = $modal;
</script>

<svelte:window on:keydown={(event) => event.key === 'Escape' && close()} />

{#if current}
  <div class="overlay" role="presentation" on:click|self={close}>
    <div class:ffmpeg={current.kind === 'ffmpeg-missing'} class="dialog" role="dialog" aria-modal="true" aria-labelledby="modal-title">
      {#if current.kind === 'ffmpeg-missing'}
        <div class="alert-icon" aria-hidden="true">!</div>
        <h2 id="modal-title">FFmpeg Required</h2>
        <p class="lead">FFmpeg is required for certain downloads and audio<br />extraction or conversion.</p>
        <div class="affected">
          <h3>What’s affected without FFmpeg?</h3>
          <div><b class="video-icon">▦</b><span><strong>Higher-quality merged downloads</strong><small>Best quality videos (e.g., 1080p and above) require<br />FFmpeg to merge video and audio.</small></span></div>
          <div><b class="audio-icon">♫</b><span><strong>Audio extraction &amp; conversion</strong><small>Converting videos to MP3, M4A, AAC, or other<br />audio formats requires FFmpeg.</small></span></div>
        </div>
        <p class="fine-print">FFmpeg is free, safe, and open source.</p>
        <footer>
          {#each current.actions || [] as action}
            <button class:primary={action.primary} type="button" on:click={() => { action.action(); close(); }}>
              {#if action.label === 'Locate FFmpeg'}
                <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7h7l2 2h9v10H3zM8 15h8M13 12l3 3-3 3"/></svg>
              {:else if action.label === 'Installation Guide'}
                <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6M20 4l-9 9M18 13v7H4V6h7"/></svg>
              {/if}
              {action.label}
            </button>
          {/each}
          <button type="button" on:click={close}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 18l-6-6 6-6"/></svg>Back</button>
        </footer>
      {:else}
        <header><div class="small-icon" aria-hidden="true">!</div><h2 id="modal-title">{current.title}</h2><button class="close" type="button" on:click={close} aria-label="Close">×</button></header>
        <p class="lead">{current.message}</p>
        {#if current.detail}<pre>{current.detail}</pre>{/if}
        <footer><button type="button" on:click={close}>Close</button>{#each current.actions || [] as action}<button class:primary={action.primary} type="button" on:click={() => { action.action(); close(); }}>{action.label}</button>{/each}</footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .overlay{position:fixed;inset:0;display:grid;place-items:center;background:rgba(5,10,17,.78);backdrop-filter:blur(3px);z-index:100;padding:20px}
  .dialog{width:min(460px,94vw);padding:24px;background:linear-gradient(145deg,#192536,#111a27);border:1px solid #435267;border-radius:14px;box-shadow:0 28px 90px rgba(0,0,0,.62)}
  .dialog.ffmpeg{width:min(500px,94vw);text-align:center;padding:30px}
  .alert-icon{width:48px;height:48px;margin:0 auto 14px;border:4px solid #ff747b;border-radius:50%;display:grid;place-items:center;color:#ff747b;font-size:27px;font-weight:700}
  h2{margin:0;font-size:24px;letter-spacing:-.01em}.lead{margin:12px 0 20px;color:var(--text-secondary);font-size:16px;line-height:1.5}
  .affected{text-align:left;padding:16px;border:1px solid var(--border-default);border-radius:10px;background:rgba(12,19,29,.38)}.affected h3{margin:0 0 13px;font-size:16px}.affected>div{display:grid;grid-template-columns:34px 1fr;gap:10px;margin-top:13px}.affected b{font-size:25px}.video-icon{color:#6c78ff}.audio-icon{color:#59c96b}.affected strong,.affected small{display:block}.affected small{margin-top:3px;color:var(--text-secondary);line-height:1.45}
  .fine-print{color:var(--text-secondary);font-size:15px;margin:18px 0}
  footer{display:flex;justify-content:center;gap:10px;flex-wrap:wrap}footer button{min-height:40px;padding:0 16px;color:var(--text-primary);background:linear-gradient(180deg,#2a3545,#1d2734);border:1px solid var(--border-default);border-radius:8px;font-size:15px;display:inline-flex;align-items:center;justify-content:center;gap:8px}footer button.primary{color:#fff;background:linear-gradient(180deg,#347cf4,#2867d8);border-color:#3c82f6}footer button svg{width:18px;height:18px;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
  .dialog.ffmpeg footer{gap:8px;flex-wrap:nowrap}.dialog.ffmpeg footer button{padding:0 12px;font-size:14px}
  header{display:grid;grid-template-columns:auto 1fr auto;align-items:center;gap:12px;text-align:left}.small-icon{width:36px;height:36px;display:grid;place-items:center;color:var(--status-danger);border:2px solid currentColor;border-radius:50%;font-weight:700}.close{color:var(--text-secondary);font-size:24px}.dialog:not(.ffmpeg) .lead{text-align:left}.dialog pre{text-align:left;max-height:160px;overflow:auto;white-space:pre-wrap;padding:12px;background:var(--surface-sunken);border-radius:8px;color:var(--text-secondary)}
  @media(max-width:520px){.dialog.ffmpeg footer{flex-wrap:wrap}}
</style>
