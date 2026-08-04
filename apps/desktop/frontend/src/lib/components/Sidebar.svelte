<script lang="ts">
  import { route, counts } from '../stores.js';
  type Route = 'home' | 'queue' | 'downloads' | 'settings';

  let current: Route;
  route.subscribe((value) => { current = value; });

  $: c = $counts;

  const items: Array<{ key: Route; label: string; icon: string }> = [
    { key: 'home',      label: 'Home',      icon: 'home' },
    { key: 'queue',     label: 'Queue',     icon: 'queue' },
    { key: 'downloads', label: 'Downloads', icon: 'downloads' },
    { key: 'settings',  label: 'Settings',  icon: 'settings' },
  ];

  function go(target: Route) {
    route.set(target);
  }
</script>

<aside class="sidebar" aria-label="Primary">
  <nav>
    <ul>
      {#each items as item}
        <li>
          <button
            type="button"
            class="nav-item"
            class:active={current === item.key}
            aria-current={current === item.key ? 'page' : undefined}
            on:click={() => go(item.key)}
          >
            <span class="nav-icon" aria-hidden="true">
              {#if item.icon === 'home'}
                <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" d="M3 11l9-7 9 7v9a2 2 0 0 1-2 2h-4v-7H9v7H5a2 2 0 0 1-2-2z"/></svg>
              {:else if item.icon === 'queue'}
                <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" d="M4 6h16M4 12h16M4 18h10"/></svg>
              {:else if item.icon === 'downloads'}
                <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" d="M12 4v12m0 0l-4-4m4 4l4-4M5 20h14"/></svg>
              {:else}
                <svg viewBox="0 0 24 24" width="18" height="18"><path fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" d="M12 9.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></svg>
              {/if}
            </span>
            <span class="nav-label">{item.label}</span>
            {#if item.key === 'queue' && (c.active + c.pending) > 0}
              <span class="nav-badge" aria-label={`${c.active + c.pending} jobs`}>{c.active + c.pending}</span>
            {/if}
            {#if item.key === 'downloads' && c.complete > 0}
              <span class="nav-badge subtle" aria-label={`${c.complete} completed`}>{c.complete}</span>
            {/if}
          </button>
        </li>
      {/each}
    </ul>
  </nav>

</aside>

<style>
  .sidebar {
    width: var(--sidebar-w);
    flex-shrink: 0;
    background: var(--surface-sidebar);
    border-right: 1px solid var(--border-subtle);
    display: flex;
    flex-direction: column;
    padding: 20px 14px;
  }

  nav { flex: 1; padding-top: 2px; }
  nav ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .nav-item {
    width: 100%;
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    min-height: 46px;
    padding: 0 15px;
    border-radius: var(--r-md);
    color: var(--text-secondary);
    font-size: 16px;
    transition: background 120ms ease, color 120ms ease;
  }

  .nav-item:hover {
    background: var(--surface-hover);
    color: var(--text-primary);
  }

  .nav-item.active {
    background: linear-gradient(135deg, #2f72ed, #245ac4);
    color: #fff;
    box-shadow: 0 8px 22px rgba(18, 74, 173, 0.24);
  }

  .nav-icon { display: inline-flex; }

  .nav-label { flex: 1; text-align: left; }

  .nav-badge {
    background: var(--accent-500);
    color: var(--text-on-accent);
    font-size: var(--fs-xs);
    font-weight: 600;
    padding: 1px 8px;
    border-radius: var(--r-full);
    min-width: 22px;
    text-align: center;
  }
  .nav-badge.subtle {
    background: var(--border-default);
    color: var(--text-secondary);
  }

</style>
