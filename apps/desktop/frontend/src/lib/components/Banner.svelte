<script lang="ts">
  import { banner } from '../stores.js';
</script>

{#if $banner}
  <div class="banner {$banner.kind}" role="status" aria-live="polite">
    <span class="dot" aria-hidden="true"></span>
    <span class="msg">{$banner.message}</span>
  </div>
{/if}

<style>
  .banner {
    position: fixed;
    bottom: var(--sp-6);
    left: 50%;
    transform: translateX(-50%);
    background: var(--surface-raised);
    border: 1px solid var(--border-default);
    border-radius: var(--r-full);
    padding: 10px 16px;
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    box-shadow: var(--shadow-card);
    font-size: var(--fs-sm);
    color: var(--text-primary);
    z-index: 80;
    max-width: min(560px, 92vw);
  }
  .dot {
    width: 8px; height: 8px; border-radius: 50%;
  }
  .success .dot { background: var(--status-success); }
  .info .dot    { background: var(--status-info); }
  .warning .dot { background: var(--status-warning); }
  .danger .dot  { background: var(--status-danger); }

  .success { border-color: rgba(52, 211, 153, 0.32); }
  .warning { border-color: rgba(251, 191, 36, 0.32); }
  .danger  { border-color: rgba(248, 113, 113, 0.32); }
</style>
