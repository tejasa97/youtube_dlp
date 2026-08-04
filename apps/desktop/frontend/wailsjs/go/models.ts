export namespace ffmpegdetect {

	export class Status {
	    available: boolean;
	    path: string;
	    version: string;
	    ffprobePath: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.path = source["path"];
	        this.version = source["version"];
	        this.ffprobePath = source["ffprobePath"];
	        this.message = source["message"];
	    }
	}

}

export namespace jobs {

	export class InfoSummary {
	    title: string;
	    channel: string;
	    duration: string;
	    thumbnail: string;
	    videoId: string;
	    url: string;

	    static createFrom(source: any = {}) {
	        return new InfoSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.channel = source["channel"];
	        this.duration = source["duration"];
	        this.thumbnail = source["thumbnail"];
	        this.videoId = source["videoId"];
	        this.url = source["url"];
	    }
	}
	export class JobSnapshot {
	    id: string;
	    url: string;
	    videoID: string;
	    title: string;
	    channel: string;
	    quality: string;
	    qualityLabel: string;
	    outputDir: string;
	    durationLabel: string;
	    thumbnail: string;
	    status: string;
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

	    static createFrom(source: any = {}) {
	        return new JobSnapshot(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.url = source["url"];
	        this.videoID = source["videoID"];
	        this.title = source["title"];
	        this.channel = source["channel"];
	        this.quality = source["quality"];
	        this.qualityLabel = source["qualityLabel"];
	        this.outputDir = source["outputDir"];
	        this.durationLabel = source["durationLabel"];
	        this.thumbnail = source["thumbnail"];
	        this.status = source["status"];
	        this.createdAt = source["createdAt"];
	        this.startedAt = source["startedAt"];
	        this.completedAt = source["completedAt"];
	        this.bytes = source["bytes"];
	        this.total = source["total"];
	        this.progress = source["progress"];
	        this.speedBps = source["speedBps"];
	        this.etaSeconds = source["etaSeconds"];
	        this.filename = source["filename"];
	        this.absolutePath = source["absolutePath"];
	        this.message = source["message"];
	        this.errorReason = source["errorReason"];
	    }
	}
	export class Request {
	    url: string;
	    videoId: string;
	    title: string;
	    channel: string;
	    quality: string;
	    outputDir: string;
	    duration: string;
	    thumbnail: string;

	    static createFrom(source: any = {}) {
	        return new Request(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.videoId = source["videoId"];
	        this.title = source["title"];
	        this.channel = source["channel"];
	        this.quality = source["quality"];
	        this.outputDir = source["outputDir"];
	        this.duration = source["duration"];
	        this.thumbnail = source["thumbnail"];
	    }
	}

}

export namespace store {

	export class HistoryEntry {
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
	    thumbnail?: string;

	    static createFrom(source: any = {}) {
	        return new HistoryEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.videoId = source["videoId"];
	        this.title = source["title"];
	        this.channel = source["channel"];
	        this.quality = source["quality"];
	        this.filename = source["filename"];
	        this.absolutePath = source["absolutePath"];
	        this.sizeBytes = source["sizeBytes"];
	        this.completedAt = source["completedAt"];
	        this.durationLabel = source["durationLabel"];
	        this.thumbnail = source["thumbnail"];
	    }
	}
	export class Settings {
	    downloadFolder: string;
	    ffmpegPath: string;
	    windowWidth: number;
	    windowHeight: number;

	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.downloadFolder = source["downloadFolder"];
	        this.ffmpegPath = source["ffmpegPath"];
	        this.windowWidth = source["windowWidth"];
	        this.windowHeight = source["windowHeight"];
	    }
	}

}

export namespace urlcheck {

	export class Result {
	    url: string;
	    videoId: string;
	    kind: string;

	    static createFrom(source: any = {}) {
	        return new Result(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.videoId = source["videoId"];
	        this.kind = source["kind"];
	    }
	}

}
