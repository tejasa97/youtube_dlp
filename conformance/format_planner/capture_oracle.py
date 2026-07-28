#!/usr/bin/env python3
"""PR 5 planner conformance oracle generator.

Generates internal/format/testdata/planner_conformance.json using the pinned
Python yt-dlp runtime. Run with:

    /Users/tejas/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \\
        conformance/format_planner/capture_oracle.py \\
        --reference /Users/tejas/projects/yt-dlp-reference \\
        --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 \\
        --output internal/format/testdata/planner_conformance.json

The output mirrors the JSON schema consumed by internal/format's
planner_conformance_test.go loader.
"""

import argparse
import copy
import json
import platform
import subprocess
import sys
from pathlib import Path

PINNED_COMMIT = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"
YoutubeDL = None
get_compatible_ext = None


def _select_with_ytdlp(formats, selector, *, allow_multiple_video=False,
                       allow_multiple_audio=False, prefer_free_formats=False):
    ydl = YoutubeDL({
        "quiet": True,
        "no_color": True,
        "simulate": True,
        "allow_multiple_video_streams": allow_multiple_video,
        "allow_multiple_audio_streams": allow_multiple_audio,
        "prefer_free_formats": prefer_free_formats,
    })
    sorted_formats = copy.deepcopy(formats)
    # Apply the pinned FormatSorter so the canonical worst-to-best order
    # matches the Go side; this is what real yt-dlp does at format selection.
    ydl.sort_formats({"formats": sorted_formats})
    ctx = {
        "formats": sorted_formats,
        "has_merged_format": any(
            f.get("acodec") != "none" and f.get("vcodec") != "none"
            for f in sorted_formats
        ),
        "incomplete_formats": (
            all(f.get("vcodec") == "none" for f in sorted_formats)
            or all(f.get("acodec") == "none" for f in sorted_formats)
        ),
    }
    selector_fn = ydl.build_format_selector(selector)
    return list(selector_fn(ctx))


def _build_case(case_id, formats, selector, *, options=None, **extra):
    options = options or {}
    plans = _select_with_ytdlp(
        formats,
        selector,
        allow_multiple_video=options.get("allow_multiple_video_streams", False),
        allow_multiple_audio=options.get("allow_multiple_audio_streams", False),
        prefer_free_formats=options.get("prefer_free_formats", False),
    )
    # yt-dlp returns one merged dict per output plan; flatten into
    # plan/tracks shape so the Go side can compare structure.
    flat = []
    for plan in plans:
        requested = plan.get("requested_formats") or [plan]
        tracks = [{"format_id": rf.get("format_id")} for rf in requested]
        flat.append({"tracks": tracks, "merged": plan})
    case = {
        "id": case_id,
        "formats": copy.deepcopy(formats),
        "selector": selector,
        "options": options,
        "plans": flat,
    }
    case.update(extra)
    return case


def _fmt(id, ext, vcodec, acodec, **extra):
    base = {
        "format_id": id,
        "url": f"https://example.invalid/{id}",
        "ext": ext,
        "vcodec": vcodec,
        "acodec": acodec,
    }
    base.update(extra)
    return base


def _audio_only(id, abr):
    return _fmt(id, "m4a", "none", "aac", abr=abr, tbr=abr)


def _video_only(id, height, vcodec="avc1"):
    return _fmt(id, "mp4", vcodec, "none", height=height, tbr=height * 2)


def _combined(id, height):
    return _fmt(id, "mp4", "avc1", "aac", height=height, tbr=height * 3)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--commit", default=PINNED_COMMIT)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()

    reference_sha = subprocess.check_output(
        ["git", "-C", str(args.reference), "rev-parse", "HEAD"], text=True,
    ).strip()
    if reference_sha != args.commit:
        parser.error(f"reference HEAD {reference_sha} != expected {args.commit}")
    if sys.version_info < (3, 10):
        parser.error(f"Python >= 3.10 required; got {platform.python_version()}")

    sys.path.insert(0, str(args.reference))
    global YoutubeDL, get_compatible_ext
    from yt_dlp import YoutubeDL as youtube_dl_class
    from yt_dlp.utils._utils import get_compatible_ext as compatible_ext
    YoutubeDL = youtube_dl_class
    get_compatible_ext = compatible_ext

    cases = []

    # Quality atoms.
    basic = [
        _combined("combined", 360),
        _video_only("video-360", 360, "avc1"),
        _video_only("video-720", 720, "vp9"),
        _audio_only("audio-low", 64),
        _audio_only("audio-high", 128),
    ]
    cases.append(_build_case("quality.basic-best", basic, "best"))
    cases.append(_build_case("quality.basic-worst", basic, "worst"))
    cases.append(_build_case("quality.basic-best.2", basic, "best.2"))
    cases.append(_build_case("quality.basic-worst.2", basic, "worst.2"))
    cases.append(_build_case("quality.basic-b", basic, "b"))
    cases.append(_build_case("quality.basic-w", basic, "w"))
    cases.append(_build_case("quality.basic-bv", basic, "bv"))
    cases.append(_build_case("quality.basic-ba", basic, "ba"))
    cases.append(_build_case("quality.basic-wv", basic, "wv"))
    cases.append(_build_case("quality.basic-wa", basic, "wa"))
    cases.append(_build_case("quality.basic-bv-star", basic, "bv*"))
    cases.append(_build_case("quality.basic-ba-star", basic, "ba*"))
    cases.append(_build_case("quality.basic-b-star", basic, "b*"))

    # Storyboard.
    storyboard_set = [
        _fmt("board", "mhtml", "none", "none"),
        _video_only("video", 720),
    ]
    cases.append(_build_case("quality.storyboard-excluded-by-best",
                             storyboard_set, "best"))

    # Incomplete fallback.
    video_only = [_video_only("v1", 360), _video_only("v2", 720)]
    cases.append(_build_case("incomplete.video-only-best", video_only, "best"))
    cases.append(_build_case("incomplete.video-only-best.2", video_only, "best.2"))
    cases.append(_build_case("incomplete.audio-only-best",
                             [_audio_only("a1", 64), _audio_only("a2", 128)], "best"))
    mixed = [_video_only("v1", 360), _audio_only("a1", 128)]
    cases.append(_build_case("incomplete.mixed-best", mixed, "best"))

    # Expression ordering.
    cases.append(_build_case("expression.all", basic, "all"))
    cases.append(_build_case("expression.all-filtered", basic, "all[height<=720]"))
    cases.append(_build_case("expression.mergeall", basic, "mergeall"))
    cases.append(_build_case("expression.comma-three", basic, "bv,ba,combined"))
    cases.append(_build_case("expression.slash-first-wins", basic, "best/bv"))
    cases.append(_build_case("expression.slash-second-wins",
                             [_video_only("v1", 720)], "best/v1"))
    cases.append(_build_case("expression.plus-cartesian",
                             [_video_only("v1", 720), _audio_only("a1", 128)],
                             "bv+ba"))
    cases.append(_build_case("expression.grouped", basic, "(bv,ba)/best"))
    cases.append(_build_case(
        "expression.all-includes-storyboard", storyboard_set, "all"))
    cases.append(_build_case(
        "expression.group-filter-keeps-context-flags", mixed,
        "(best)[vcodec!=none]"))

    # Extension behaviour.
    cases.append(_build_case("extension.audio-mp3",
                             [_fmt("f", "mp3", "none", "mp3")], "mp3"))
    cases.append(_build_case("extension.video-mp4",
                             [_combined("c", 360)], "mp4"))
    cases.append(_build_case("extension.storyboard-mhtml",
                             [_fmt("board", "mhtml", "none", "none")], "mhtml"))
    cases.append(_build_case("extension.direct-id", basic, "video-720"))

    # Multistream behaviour.
    multi = [_video_only("v1", 720), _video_only("v2", 1080),
             _audio_only("a1", 64), _audio_only("a2", 128)]
    multi_selector = "bv+wv+ba+wa"
    cases.append(_build_case("multistream.default", multi, multi_selector))
    cases.append(_build_case("multistream.allow-video", multi, multi_selector,
                             options={"allow_multiple_video_streams": True}))
    cases.append(_build_case("multistream.allow-audio", multi, multi_selector,
                             options={"allow_multiple_audio_streams": True}))
    cases.append(_build_case("multistream.allow-both", multi, multi_selector,
                             options={
                                 "allow_multiple_video_streams": True,
                                 "allow_multiple_audio_streams": True,
                             }))
    cases.append(_build_case("multistream.duplicate-preserved", multi, "v2+v2",
                             options={"allow_multiple_video_streams": True}))
    cases.append(_build_case("multistream.combined-uses-both-slots",
                             [_combined("c", 360), _audio_only("a1", 64)],
                             "best+bestaudio"))
    cases.append(_build_case("multistream.storyboard-removed",
                             [_fmt("board", "mhtml", "none", "none"),
                              _video_only("v1", 720),
                              _audio_only("a1", 128)],
                             "bv+ba"))

    # Merged metadata.
    merged = [_video_only("video-720", 720),
              _audio_only("audio-high", 128)]
    cases.append(_build_case("metadata.two-track", merged, "bv+ba"))
    cases.append(_build_case("metadata.single-track", merged, "bv"))
    cases.append(_build_case(
        "metadata.prefer-free-extension",
        [_video_only("free-video", 720, "av1"),
         _fmt("free-audio", "m4a", "none", "mp4a", abr=128, tbr=128)],
        "bv+ba", options={"prefer_free_formats": True}))
    cases.append(_build_case(
        "metadata.rich-two-track",
        [_fmt("rich-video", "mp4", "avc1", "none", width=1280, height=720,
              fps=30, dynamic_range="HDR10", vbr=1400, tbr=1400,
              filesize=1000, language="en", format_note="video"),
         _fmt("rich-audio", "m4a", "none", "aac", abr=128, tbr=128,
              asr=48000, audio_channels=2, filesize_approx=500,
              language="en", format_note="audio")],
        "bv+ba"))

    # Compatible extension.
    compat_cases = [
        ("compatible.mp4-codecs",
         ["avc1.640028"], ["mp4a.40.2"], ["mp4"], ["m4a"], None, "mp4"),
        ("compatible.webm-codecs",
         ["vp9"], ["opus"], ["webm"], ["weba"], None, "webm"),
        ("compatible.incompatible-codecs",
         ["avc1.640028"], ["opus"], ["mp4"], ["webm"], None, "mkv"),
        ("compatible.multi-video",
         ["avc1.640028", "avc1.4d401e"], ["mp4a.40.2"],
         ["mp4", "mp4"], ["m4a"], None, "mkv"),
        ("compatible.multi-audio",
         ["avc1.640028"], ["mp4a.40.2", "opus"],
         ["mp4"], ["m4a", "webm"], None, "mkv"),
        ("compatible.homogeneous-mp4",
         ["avc1.640028"], ["mp4a.40.2"], ["mp4"], ["m4a"], None, "mp4"),
        ("compatible.homogeneous-webm",
         ["vp9"], ["opus"], ["webm"], ["webm"], None, "webm"),
        ("compatible.family-mp4",
         ["avc1"], ["mp4a"], ["mp4"], ["m4a"], None, "mp4"),
        ("compatible.preferences-mkv",
         ["avc1"], ["mp4a"], ["mp4"], ["m4a"], ["mkv"], "mkv"),
        ("compatible.preferences-excluding-mkv",
         ["vp9"], ["opus"], ["webm"], ["weba"], ["webm"], "webm"),
        ("compatible.final-preference",
         ["avc1"], ["opus"], ["mp4"], ["webm"], ["mov"], "mov"),
        ("compatible.preference-case-sensitive",
         ["avc1"], ["opus"], ["mp4"], ["webm"], ["MKV"], "MKV"),
        ("compatible.extension-case-sensitive",
         ["avc1"], ["mp4a"], ["MP4"], ["m4a"], None, "mkv"),
    ]
    compat_dicts = []
    for case_id, vcodecs, acodecs, vexts, aexts, prefs, expected in compat_cases:
        result = get_compatible_ext(
            vcodecs=vcodecs, acodecs=acodecs,
            vexts=vexts, aexts=aexts,
            preferences=prefs,
        )
        compat_dicts.append({
            "id": case_id,
            "vcodecs": vcodecs,
            "acodecs": acodecs,
            "vexts": vexts,
            "aexts": aexts,
            "preferences": prefs,
            "expected": result,
            "want": expected,
        })
    cases.append({"id": "compatible_extension.cases", "cases": compat_dicts})

    output = {
        "_comment": "PR 5 planner conformance fixture.",
        "schema_version": 1,
        "reference": {
            "repository": "https://github.com/yt-dlp/yt-dlp",
            "commit": args.commit,
            "python_version": f"CPython {platform.python_version()}",
            "sources": [
                "yt_dlp/YoutubeDL.py:build_format_selector,_merge",
                "yt_dlp/utils/_utils.py:get_compatible_ext",
            ],
        },
        "cases": cases,
    }

    out_path = Path(args.output)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(output, indent=2, sort_keys=True))
    print(f"wrote {out_path} ({len(cases)} cases)")


if __name__ == "__main__":
    main()
