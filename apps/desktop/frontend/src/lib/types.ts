// Shared TypeScript shapes mirror the JSON returned by the Go bindings.
// Keeping them in one place makes it easy to spot drift between the
// app contract and the UI.

export type Quality =
  | 'best'
  | '4k'
  | '1440p'
  | '1080p'
  | '720p'
  | 'audio';

export const QUALITIES: Quality[] = ['best', '4k', '1440p', '1080p', '720p', 'audio'];

export type JobStatus = 'pending' | 'active' | 'complete' | 'failed' | 'canceled';

export interface JobSnapshot {
  id: string;
  url: string;
  videoID: string;
  title: string;
  channel: string;
  quality: Quality;
  qualityLabel: string;
  outputDir: string;
  durationLabel: string;
  thumbnail: string;
  status: JobStatus;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
  bytes: number;
  total: number;
  progress: number;
  speedBps: number;
  etaSeconds: number;
  filename: string;
  absolutePath: string;
  message: string;
  errorReason?: string;
}

export interface HistoryEntry {
  id: string;
  videoId: string;
  title: string;
  channel: string;
  quality: string;
  filename: string;
  absolutePath: string;
  sizeBytes: number;
  completedAt: string;
  durationLabel: string;
  thumbnail: string;
}

export interface FFmpegStatus {
  available: boolean;
  path: string;
  version: string;
  ffprobePath: string;
  message: string;
}

export interface Settings {
  downloadFolder: string;
  ffmpegPath: string;
  windowWidth: number;
  windowHeight: number;
}

export interface UrlCheckResult {
  url: string;
  videoId: string;
  kind: string;
}

export interface InfoSummary {
  title: string;
  channel: string;
  duration: string;
  thumbnail: string;
  videoId: string;
  url: string;
}

export interface QueueEvent {
  name: 'job:update' | 'queue:update';
  job?: JobSnapshot;
  queue?: JobSnapshot[];
}

export interface AppError {
  reason: string;
  message: string;
}
