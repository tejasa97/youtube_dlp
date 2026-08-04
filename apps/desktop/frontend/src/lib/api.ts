// Thin wrappers around the Wails runtime. We intentionally avoid the
// generated `wailsjs/` directory in source — rolldown has trouble
// resolving the generated `.js` files in this environment, and writing
// to the Wails bindings directory at build time is fragile. The runtime
// contract (window.go.* and window.runtime.*) is stable, so we call it
// directly here. Wails injects these globals at app startup.

import type {
  FFmpegStatus,
  HistoryEntry,
  InfoSummary,
  JobSnapshot,
  QueueEvent,
  Settings,
  UrlCheckResult,
} from './types';

declare global {
  interface Window {
    go: {
      main: {
        App: Record<string, (...args: any[]) => Promise<any>>;
      };
    };
    runtime: {
      EventsOn: (event: string, cb: (...args: any[]) => void) => () => void;
      EventsOff: (event: string, ...rest: string[]) => void;
      EventsEmit: (event: string, ...args: any[]) => void;
      OpenDirectoryDialog: (opts: any) => Promise<string>;
      OpenFileDialog: (opts: any) => Promise<string>;
      ClipboardSetText: (text: string) => Promise<void>;
      BrowserOpenURL: (url: string) => void;
      LogError: (msg: string) => void;
    };
  }
}

function call<T>(method: string, ...args: any[]): Promise<T> {
  const fn = window.go?.main?.App?.[method];
  if (typeof fn !== 'function') {
    return Promise.reject(new Error(`Go binding "${method}" is not available yet`));
  }
  return fn(...args);
}

export interface StartRequest {
  url: string;
  videoId: string;
  title: string;
  channel: string;
  quality: JobSnapshot['quality'];
  outputDir: string;
  duration: string;
  thumbnail: string;
}

export const api = {
  settings: {
    get: () => call<Settings>('GetSettings'),
    update: (next: Settings) => call<Settings>('UpdateSettings', next),
  },
  ffmpeg: {
    status: () => call<FFmpegStatus>('GetFFmpegStatus'),
    probe: () => call<FFmpegStatus>('ProbeFFmpeg'),
    configure: (path: string) => call<FFmpegStatus>('ConfigureFFmpeg', path),
    clear: () => call<FFmpegStatus>('ClearFFmpegPath'),
    pickPath: () => call<string>('PickFFmpegPath'),
  },
  folder: {
    pick: () => call<string>('PickDownloadFolder'),
  },
  validation: {
    url: (raw: string) => call<UrlCheckResult>('ValidateURL', raw),
  },
  analyse: {
    url: (raw: string) => call<InfoSummary>('AnalyzeURL', raw),
  },
  jobs: {
    start: (req: StartRequest) => call<string>('StartDownload', req),
    list: () => call<JobSnapshot[]>('ListJobs'),
    cancel: (id: string) => call<void>('CancelJob', id),
    retry: (id: string) => call<void>('RetryJob', id),
    remove: (id: string) => call<void>('RemoveJob', id),
    clearCompleted: () => call<void>('ClearCompletedJobs'),
  },
  downloads: {
    list: () => call<HistoryEntry[]>('ListDownloads'),
    remove: (id: string) => call<void>('RemoveDownload', id),
    clear: () => call<void>('ClearDownloads'),
  },
  fs: {
    open: (path: string) => call<void>('OpenFile', path),
    reveal: (path: string) => call<void>('RevealInFinder', path),
  },
  diagnostics: {
    copy: () => call<string>('CopyDiagnostics'),
  },
  events: {
    onJobUpdate: (cb: (job: JobSnapshot) => void) =>
      window.runtime?.EventsOn?.('job:update', (event: QueueEvent) => {
        if (event?.job) cb(event.job);
      }),
    onQueue: (cb: (jobs: JobSnapshot[]) => void) =>
      window.runtime?.EventsOn?.('queue:update', (event: QueueEvent) => {
        if (event?.queue) cb(event.queue);
      }),
    onHistory: (cb: (entries: HistoryEntry[]) => void) =>
      window.runtime?.EventsOn?.('history:update', (entries: HistoryEntry[]) => cb(entries ?? [])),
    onSettings: (cb: (settings: Settings) => void) =>
      window.runtime?.EventsOn?.('settings:update', (settings: Settings) => cb(settings)),
    onFFmpeg: (cb: (status: FFmpegStatus) => void) =>
      window.runtime?.EventsOn?.('ffmpeg:update', (status: FFmpegStatus) => cb(status)),
    off: (event: string) => window.runtime?.EventsOff?.(event),
  },
};

export {};
