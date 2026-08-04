import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const read = (path) => readFile(new URL(path, import.meta.url), 'utf8');

test('approved navigation and window branding are used', async () => {
  const [sidebar, main] = await Promise.all([
    read('../src/lib/components/Sidebar.svelte'),
    read('../../main.go'),
  ]);
  for (const label of ['Home', 'Queue', 'Downloads', 'Settings']) assert.match(sidebar, new RegExp(`label: '${label}'`));
  for (const rejected of ['v0 · single video', 'Single public YouTube videos only', 'brand-name', 'class="logo"']) assert.doesNotMatch(sidebar, new RegExp(rejected));
  assert.match(main, /Title:\s+"YTDLP Go Desktop"/);
  assert.doesNotMatch(await read('../index.html'), /<title>ytdlp-desktop<\/title>/);
});

test('page titles and subtitles match the approved V0 copy', async () => {
  const [home, queue, downloads, settings] = await Promise.all([
    read('../src/pages/Home.svelte'), read('../src/pages/Queue.svelte'),
    read('../src/pages/Downloads.svelte'), read('../src/pages/Settings.svelte'),
  ]);
  assert.match(home, /<h1 id="home-title">Home<\/h1>/);
  assert.match(home, /Paste a public YouTube URL and choose how to download it\./);
  assert.match(home, /'Analyze'/);
  assert.match(queue, /Manage your download queue\. Items will download in order from top to bottom\./);
  assert.match(queue, /Clear Completed/);
  assert.match(downloads, /View your recently downloaded items\./);
  assert.match(downloads, /placeholder="Search downloads…"/);
  assert.match(settings, /Configure download and tool settings\./);
  assert.match(settings, />Default download folder</);
  assert.match(settings, />FFmpeg path</);
  assert.match(settings, />Diagnostics</);
  assert.match(settings, />Copy Diagnostics</);
  assert.match(settings, /await api\.diagnostics\.copy\(\)/);
});

test('accepted URLs with analysis failures use the generic error modal', async () => {
  const home = await read('../src/pages/Home.svelte');
  assert.match(home, /title: 'We could not analyze this video'/);
  assert.match(home, /kind: 'error'/);
  assert.doesNotMatch(home, /catch \(err\) \{\s*unsupported = \{\s*url: result\.url/);
});

test('unsupported and FFmpeg-required states retain accurate V0 copy', async () => {
  const [home, modal] = await Promise.all([
    read('../src/pages/Home.svelte'), read('../src/lib/components/Modal.svelte'),
  ]);
  assert.match(home, /We couldn’t analyze this URL/);
  assert.match(home, /This app currently supports single public YouTube videos\./);
  assert.doesNotMatch(home, /supports YouTube videos and playlists/);
  assert.match(home, /class="unsupported-illustration"/);
  assert.match(home, /aria-hidden="true"/);
  assert.match(home, /id="warning-fill"/);
  assert.match(home, /M147 214c7-8 17-8 24 0/);
  assert.equal((home.match(/<div><svg viewBox="0 0 24 24" aria-hidden="true">/g) || []).length, 3);
  for (const label of ['Try another URL', 'Copy Diagnostics', 'Open in Browser']) {
    assert.match(home, new RegExp(`<button[^>]*>[\\s\\S]*?<svg[^>]+aria-hidden="true"[\\s\\S]*?${label}</button>`));
  }
  for (const copy of ['FFmpeg Required', 'What’s affected without FFmpeg?', 'Higher-quality merged downloads', 'Audio extraction &amp; conversion', 'FFmpeg is free, safe, and open source.', 'Back']) {
    assert.ok(modal.includes(copy), `missing modal copy: ${copy}`);
  }
  assert.match(modal, /action\.label === 'Locate FFmpeg'/);
  assert.match(modal, /action\.label === 'Installation Guide'/);
  assert.match(modal, /<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 18l-6-6 6-6"\/><\/svg>Back/);
});

test('terminal queue rows expose recovery and removal actions', async () => {
  const row = await read('../src/lib/components/ProgressRow.svelte');
  assert.match(row, /<div class="queue-row">/);
  assert.doesNotMatch(row, /role="cell"/);
  assert.doesNotMatch(row, /<t[rd][^>]*>/);
  for (const action of ['Cancel download', 'Open downloaded file', 'Retry download', 'Remove download']) {
    assert.match(row, new RegExp(`<button[^>]+type="button"[^>]+aria-label="${action}"`));
  }
  assert.match(row, /on:pointerdown\|preventDefault\|stopPropagation=/);
  assert.match(row, /on:click\|stopPropagation=/);
  assert.match(row, /await api\.jobs\.cancel\(job\.id\)/);
  assert.match(row, /await api\.jobs\.retry\(job\.id\)/);
  assert.match(row, /await api\.jobs\.remove\(job\.id\)/);
  assert.match(row, />Retry</);
  assert.match(row, />Remove</);
  assert.match(row, /job\.status === 'failed'/);
});

test('download history actions remain native accessible buttons', async () => {
  const downloads = await read('../src/pages/Downloads.svelte');
  assert.doesNotMatch(downloads, /role="(?:table|row|cell)"/);
  assert.match(downloads, /<button[^>]+aria-label="Open downloaded file"/);
  assert.match(downloads, /<button[^>]+aria-label="Show in Finder"/);
  assert.match(downloads, /await api\.fs\.open\(entry\.absolutePath\)/);
  assert.match(downloads, /await api\.fs\.reveal\(entry\.absolutePath\)/);
});
