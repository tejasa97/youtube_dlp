// Package sponsorblock implements a bounded, native SponsorBlock segment
// client, normalization layer, chapter marking, and pure cut planning derived
// from the pinned yt-dlp postprocessor reference. FFmpeg execution and public
// request wiring live in internal/media and pkg/ytdlp. CLI flag exposure
// remains out of scope here.
//
// Behavior follows the pinned yt-dlp commit
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8
// (yt_dlp/postprocessor/sponsorblock.py and modify_chapters.py). Local
// checkout provenance for that pin lives only under conformance/sponsorblock/.
//   - The endpoint is the first 4 lowercase hex characters of
//     SHA-256(videoID) at /api/skipSegments/<prefix> with the canonical query
//     service, categories, and actionTypes (skip, poi, chapter).
//   - The matching group is the one whose videoID equals the requested
//     videoID; the prefix may return other videoIDs.
//   - The pinned normalizer discards (0,0) whole-video markers, snaps starts
//     <=1s to zero, extends POI categories by one second, snaps ends within
//     one second of the known duration to the duration, and filters
//     duration-mismatched segments using the <1s or <5s/<5% policy.
//   - Remove planning merges overlapping/adjacent skip ranges, never cuts
//     poi_highlight/chapter, and derives concat inpoint/outpoint keep segments
//     within the ffmpeg concat-range and force-keyframe limits.
//   - Subtitle sidecars are rewritten with deterministic cue remapping for
//     srt/vtt/ass/lrc rather than ffmpeg concat.
//
// The package is internal because its public surface is a bounded enrichment
// and cutting stage wired into pkg/ytdlp. The categories, titles, and action
// types are versioned alongside the reference.
package sponsorblock
