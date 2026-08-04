<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api } from './lib/api.js';
  import { ffmpeg, history, jobs, route, settings, banner, modal } from './lib/stores.js';
  import Sidebar from './lib/components/Sidebar.svelte';
  import Modal from './lib/components/Modal.svelte';
  import Banner from './lib/components/Banner.svelte';
  import Home from './pages/Home.svelte';
  import Queue from './pages/Queue.svelte';
  import Downloads from './pages/Downloads.svelte';
  import Settings from './pages/Settings.svelte';

  let unsubJob: (() => void) | undefined;
  let unsubQueue: (() => void) | undefined;
  let unsubHistory: (() => void) | undefined;
  let unsubSettings: (() => void) | undefined;
  let unsubFFmpeg: (() => void) | undefined;
  let unsubAll: Array<() => void> = [];

  onMount(async () => {
    const [s, st, jobs0, hist, ff] = await Promise.all([
      api.settings.get(),
      api.ffmpeg.status(),
      api.jobs.list(),
      api.downloads.list(),
      api.ffmpeg.status(),
    ]);
    settings.set(s);
    ffmpeg.set(st);
    jobs.set(jobs0 ?? []);
    history.set(hist ?? []);
    ffmpeg.set(ff);

    unsubJob     = api.events.onJobUpdate((job) => updateJobInList(job));
    unsubQueue   = api.events.onQueue((list) => jobs.set(list ?? []));
    unsubHistory = api.events.onHistory((entries) => history.set(entries ?? []));
    unsubSettings = api.events.onSettings((s) => settings.set(s));
    unsubFFmpeg  = api.events.onFFmpeg((s) => ffmpeg.set(s));
    unsubAll = [unsubJob, unsubQueue, unsubHistory, unsubSettings, unsubFFmpeg].filter(Boolean) as Array<() => void>;
  });

  onDestroy(() => {
    for (const off of unsubAll) off();
  });

  function updateJobInList(updated: { id: string }) {
    jobs.update((list) => {
      const idx = list.findIndex((j) => j.id === updated.id);
      if (idx === -1) return list;
      const copy = list.slice();
      copy[idx] = { ...copy[idx], ...updated };
      return copy;
    });
  }

  function navigate(target: 'home' | 'queue' | 'downloads' | 'settings') {
    route.set(target);
  }
</script>

<Sidebar />

<main class="main">
  <div class="scroll">
    {#if $route === 'home'}
      <Home on:goto={(e) => navigate(e.detail)} />
    {:else if $route === 'queue'}
      <Queue />
    {:else if $route === 'downloads'}
      <Downloads />
    {:else if $route === 'settings'}
      <Settings />
    {/if}
  </div>
</main>

<Modal />
<Banner />

<style>
  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background:
      radial-gradient(800px 400px at 0% 0%, rgba(59,130,246,0.05), transparent 60%),
      var(--surface-bg);
  }
  .scroll {
    flex: 1;
    overflow-y: auto;
  }
</style>
