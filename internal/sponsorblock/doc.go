// Package sponsorblock implements a bounded, native SponsorBlock segment
// client and normalization layer derived from the pinned yt-dlp
// postprocessor reference. It is intentionally limited to metadata fetch and
// per-segment normalization: media cutting, chapter rewriting, FFmpeg
// removal, subtitle synchronization, and CLI surface remain out of scope.
//
// Behavior follows the pinned reference at
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 (yt_dlp/postprocessor/sponsorblock.py):
//   - The endpoint is the first 4 lowercase hex characters of
//     SHA-256(videoID) at /api/skipSegments/<prefix> with the canonical query
//     service, categories, and actionTypes (skip, poi, chapter).
//   - The matching group is the one whose videoID equals the requested
//     videoID; the prefix may return other videoIDs.
//   - The pinned normalizer discards (0,0) whole-video markers, snaps starts
//     <=1s to zero, extends POI categories by one second, snaps ends within
//     one second of the known duration to the duration, and filters
//     duration-mismatched segments using the <1s or <5s/<5% policy.
//
// The package is internal because its public surface is a bounded enrichment
// stage wired into pkg/ytdlp. The categories, titles, and action types are
// versioned alongside the reference.
package sponsorblock
