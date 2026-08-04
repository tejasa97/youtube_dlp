<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api } from './lib/api.js';
  import { errorMessage, ffmpeg, history, jobs, route, settings, modal } from './lib/stores.js';
  import type { JobSnapshot } from './lib/types.js';
  import Sidebar from './lib/components/Sidebar.svelte';
  import Modal from './lib/components/Modal.svelte';
  import Banner from './lib/components/Banner.svelte';
  import Home from './pages/Home.svelte';
  import Queue from './pages/Queue.svelte';
  import Downloads from './pages/Downloads.svelte';
  import Settings from './pages/Settings.svelte';

  let unsubAll: Array<() => void> = [];

  onMount(async () => {
    unsubAll = [
      api.events.onJobUpdate(updateJobInList),
      api.events.onQueue((list) => jobs.set(list ?? [])),
      api.events.onHistory((entries) => history.set(entries ?? [])),
      api.events.onSettings((value) => settings.set(value)),
      api.events.onFFmpeg((status) => ffmpeg.set(status)),
    ].filter(Boolean) as Array<() => void>;

    try {
      const [savedSettings, initialJobs, savedHistory, ffmpegStatus] = await Promise.all([
        api.settings.get(),
        api.jobs.list(),
        api.downloads.list(),
        api.ffmpeg.status(),
      ]);
      settings.set(savedSettings);
      jobs.set(initialJobs ?? []);
      history.set(savedHistory ?? []);
      ffmpeg.set(ffmpegStatus);
    } catch (err) {
      modal.set({
        kind: 'error',
        title: 'The app could not finish starting',
        message: errorMessage(err, 'Close and reopen the app. If the problem continues, copy the diagnostic details.'),
      });
    }
  });

  onDestroy(() => {
    for (const off of unsubAll) off();
  });

  function updateJobInList(updated: JobSnapshot) {
    jobs.update((list) => {
      const idx = list.findIndex((j) => j.id === updated.id);
      if (idx === -1) return [updated, ...list];
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
