// Pure formatting helpers. Pure functions keep the pages simple and
// make the helpers trivial to test without rendering anything.

import type { JobSnapshot } from './types.js';

const QUALITY_LABELS: Record<string, string> = {
  best: 'Best',
  '4k': '4K',
  '1440p': '1440p',
  '1080p': '1080p',
  '720p': '720p',
  audio: 'Audio only',
};

export function qualityLabel(q: string): string {
  return QUALITY_LABELS[q] ?? q;
}

export function formatBytes(bytes: number): string {
  if (!bytes || bytes < 0) return '—';
  const kb = 1024;
  const mb = kb * 1024;
  const gb = mb * 1024;
  if (bytes >= gb) return `${(bytes / gb).toFixed(1)} GB`;
  if (bytes >= mb) return `${(bytes / mb).toFixed(1)} MB`;
  if (bytes >= kb) return `${Math.round(bytes / kb)} KB`;
  return `${bytes} B`;
}

export function formatSpeed(bps: number): string {
  if (!bps || bps <= 0) return '';
  return `${formatBytes(bps)}/s`;
}

export function formatEta(seconds: number): string {
  if (!seconds || seconds <= 0 || !Number.isFinite(seconds)) return '';
  const s = Math.round(seconds);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${h}h ${m}m`;
}

export function formatProgress(p: number): string {
  if (!Number.isFinite(p)) return '0%';
  return `${Math.max(0, Math.min(100, Math.round(p * 100)))}%`;
}

export function formatDate(iso: string): string {
  if (!iso) return '';
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });
  } catch {
    return iso;
  }
}

export function formatRelative(iso: string): string {
  if (!iso) return '';
  try {
    const d = new Date(iso).getTime();
    const diff = Date.now() - d;
    if (diff < 60_000) return 'just now';
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
    if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
    if (diff < 7 * 86_400_000) return `${Math.floor(diff / 86_400_000)}d ago`;
    return formatDate(iso);
  } catch {
    return iso;
  }
}

export function progressOf(job: JobSnapshot): number {
  if (job.total > 0 && job.bytes > 0) {
    return Math.max(0, Math.min(1, job.bytes / job.total));
  }
  return Math.max(0, Math.min(1, job.progress || 0));
}
