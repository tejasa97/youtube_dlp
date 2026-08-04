// Reactive stores hold the latest snapshots from the backend. Pages select
// the slices they need; transport and error normalization stay shared.

import { writable, derived } from 'svelte/store';
import type { FFmpegStatus, HistoryEntry, JobSnapshot, Settings } from './types.js';

export const settings = writable<Settings>({
  downloadFolder: '',
  ffmpegPath: '',
  windowWidth: 1180,
  windowHeight: 760,
});

export const ffmpeg = writable<FFmpegStatus>({
  available: false,
  path: '',
  version: '',
  ffprobePath: '',
  message: 'Checking ffmpeg…',
});

export const jobs = writable<JobSnapshot[]>([]);
export const history = writable<HistoryEntry[]>([]);

// Derived views used by the sidebar counters.
export const counts = derived(jobs, ($jobs) => {
  let active = 0;
  let pending = 0;
  let complete = 0;
  let failed = 0;
  for (const job of $jobs) {
    switch (job.status) {
      case 'active': active++; break;
      case 'pending': pending++; break;
      case 'complete': complete++; break;
      case 'failed': failed++; break;
    }
  }
  return { active, pending, complete, failed };
});

export const route = writable<'home' | 'queue' | 'downloads' | 'settings'>('home');

// Modal state — only one modal at a time.
export interface ModalState {
  kind: 'unsupported' | 'ffmpeg-missing' | 'error';
  title: string;
  message: string;
  reason?: string;
  detail?: string;
  actions?: Array<{ label: string; action: () => void; primary?: boolean }>;
}
export const modal = writable<ModalState | null>(null);

// Toast-style banner state. Auto-clears on a timer.
export interface Banner {
  id: number;
  kind: 'success' | 'info' | 'warning' | 'danger';
  message: string;
}
export const banner = writable<Banner | null>(null);
let bannerSeq = 0;

export function showBanner(kind: Banner['kind'], message: string, ttl = 3500) {
  bannerSeq += 1;
  const id = bannerSeq;
  banner.set({ id, kind, message });
  setTimeout(() => {
    banner.update((current) => (current?.id === id ? null : current));
  }, ttl);
}

export function showError(err: unknown, fallback = 'Something went wrong') {
	showBanner('danger', errorMessage(err, fallback));
}

export function errorMessage(err: unknown, fallback: string): string {
	if (err instanceof Error && err.message.trim()) return err.message;
	if (typeof err === 'string' && err.trim()) return err;
	if (err && typeof err === 'object' && 'message' in err) {
		const message = String((err as { message?: unknown }).message ?? '').trim();
		if (message) return message;
	}
	return fallback;
}
