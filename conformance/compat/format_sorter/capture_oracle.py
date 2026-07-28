#!/usr/bin/env python3
"""Capture pinned FormatSorter canonical order for the dedicated PR 4 sorter
oracle corpus.

This script is a maintainer-only artifact. It runs with the pinned CPython
interpreter and only writes the fixture file when invoked with --write.
Production Go tests load the committed JSON and never invoke Python.
"""
import argparse
import copy
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path

REFERENCE_COMMIT = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"
EXPECTED_PYTHON = "CPython 3.12.13"
REFERENCE_DIR = Path("/Users/tejas/projects/yt-dlp-reference")
WORKTREE_DIR = Path(__file__).resolve().parents[3]

sys.path.insert(0, str(REFERENCE_DIR))

from yt_dlp.utils import (  # noqa: E402
    FormatSorter,
    determine_ext,
    determine_protocol,
    int_or_none,
)

BASE_FORMAT_SORTER_SETTINGS = copy.deepcopy(FormatSorter.settings)

NUMERIC_FIELDS = (
    "width", "height", "asr", "audio_channels", "fps",
    "tbr", "abr", "vbr", "filesize", "filesize_approx",
    "timestamp", "release_timestamp", "available_at", "duration",
    "view_count", "like_count", "dislike_count", "repost_count", "save_count",
    "average_rating", "comment_count", "age_limit", "start_time", "end_time",
    "chapter_number", "season_number", "episode_number", "track_number",
    "disc_number", "release_year",
)


class FakeYDL:
    def __init__(self, prefer_free=False, sort_force=False, allow_drm=False,
                 sort_user=None):
        self.params = {
            "format_sort": list(sort_user or []),
            "prefer_free_formats": prefer_free,
            "format_sort_force": sort_force,
            "allow_unplayable_formats": allow_drm,
        }
        self.write_debug_calls = []

    def write_debug(self, msg):
        self.write_debug_calls.append(msg)

    def deprecated_feature(self, msg):
        pass


def coerce_format(fmt):
    for field in NUMERIC_FIELDS:
        if field in fmt:
            value = int_or_none(fmt[field])
            if value is None:
                fmt[field] = None
            else:
                fmt[field] = value


def fill_sorting_fields(fmt):
    if not fmt.get("protocol"):
        fmt["protocol"] = determine_protocol(fmt)
    if not fmt.get("ext") and "url" in fmt:
        fmt["ext"] = determine_ext(fmt["url"]).lower()
    if fmt.get("vcodec") == "none":
        fmt["audio_ext"] = fmt["ext"] if fmt.get("acodec") != "none" else "none"
        fmt["video_ext"] = "none"
    else:
        fmt["video_ext"] = fmt["ext"]
        fmt["audio_ext"] = "none"

    if fmt.get("preference") is None and fmt.get("ext") == "flv" and re.match(
        "[hx]265|he?vc?", fmt.get("vcodec") or ""
    ):
        fmt["preference"] = -100

    if fmt.get("vcodec") == "none":
        fmt["vbr"] = 0
    if fmt.get("acodec") == "none":
        fmt["abr"] = 0
    if not fmt.get("vbr") and fmt.get("vcodec") != "none":
        try:
            if fmt.get("tbr") is not None and fmt.get("abr") is not None:
                diff = fmt["tbr"] - fmt["abr"]
                fmt["vbr"] = diff if diff else None
        except TypeError:
            pass
    if not fmt.get("abr") and fmt.get("acodec") != "none":
        try:
            if fmt.get("tbr") is not None and fmt.get("vbr") is not None:
                diff = fmt["tbr"] - fmt["vbr"]
                fmt["abr"] = diff if diff else None
        except TypeError:
            pass
    if not fmt.get("tbr"):
        try:
            if fmt.get("vbr") is not None and fmt.get("abr") is not None:
                total = fmt["vbr"] + fmt["abr"]
                fmt["tbr"] = total if total else None
        except TypeError:
            pass


def filter_drm(formats, allow_drm):
    if allow_drm:
        return list(formats)
    return [f for f in formats if not f.get("has_drm") or f.get("has_drm") == "maybe"]


def filter_url(formats):
    out = []
    for f in formats:
        url = f.get("url")
        if not url:
            continue
        if isinstance(url, bytes):
            url = url.decode("utf-8", "replace")
            f["url"] = url
        out.append(f)
    return out


def assign_format_id(formats):
    by_id = {}
    sanitized = []
    for f in formats:
        f = dict(f)
        if not f.get("format_id"):
            f["format_id"] = str(len(sanitized))
        else:
            f["format_id"] = re.sub(r"[\s,/+\[\]()]", "_", f["format_id"])
        sanitized.append(f)
        by_id.setdefault(f["format_id"], []).append(f)
    for fid, members in by_id.items():
        if len(members) > 1:
            for ordinal, member in enumerate(members):
                member["format_id"] = f"{fid}-{ordinal}"
    common_exts = {"mp4", "m4a", "webm", "mp3", "ogg", "opus", "aac", "flac", "wav", "ts", "mkv", "mov"}
    for f in sanitized:
        if f["format_id"] != f.get("ext") and f["format_id"] in common_exts:
            f["format_id"] = "f" + f["format_id"]
    return sanitized


def prepare_pinned(formats, sort_user=None, sort_extractor=None,
                   prefer_free=False, sort_force=False, allow_drm=False):
    # FormatSorter mutates its class-level settings while resolving aliases,
    # limits, and mixed scalar types. Each oracle case must start from the
    # pristine pinned settings so case order cannot contaminate results.
    FormatSorter.settings = copy.deepcopy(BASE_FORMAT_SORTER_SETTINGS)
    formats = copy.deepcopy(formats)
    for source_index, fmt in enumerate(formats):
        fmt["__oracle_source_index"] = source_index
    formats = filter_drm(formats, allow_drm)
    formats = filter_url(formats)
    for f in formats:
        coerce_format(f)
        fill_sorting_fields(f)
    sorter = FormatSorter(
        FakeYDL(prefer_free=prefer_free, sort_force=sort_force, allow_drm=allow_drm,
                sort_user=sort_user),
        list(sort_extractor or []),
    )
    formats = sorted(formats, key=sorter.calculate_preference)
    formats = assign_format_id(formats)
    return formats


def verify_environment():
    if sys.version_info < (3, 10):
        raise SystemExit("Python >= 3.10 is required")
    head = subprocess.check_output(
        ["git", "-C", str(REFERENCE_DIR), "rev-parse", "HEAD"],
        text=True,
    ).strip()
    if head != REFERENCE_COMMIT:
        raise SystemExit(f"reference HEAD {head!r} != pinned {REFERENCE_COMMIT!r}")


def render_payload(cases):
    return {
        "schema_version": 1,
        "reference": {
            "repository": "https://github.com/yt-dlp/yt-dlp",
            "commit": REFERENCE_COMMIT,
            "python_version": EXPECTED_PYTHON,
            "source": [
                "yt_dlp/utils/_utils.py:5367-5666 FormatSorter",
                "yt_dlp/utils/_utils.py:1769 lookup_unit_table (parse_bytes)",
                "yt_dlp/utils/_utils.py:1311 determine_ext",
                "yt_dlp/utils/_utils.py:3190 determine_protocol",
            ],
        },
        "cases": cases,
    }


def basic_formats():
    return [
        {"format_id": "360", "url": "https://example.invalid/360", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 360, "tbr": 500},
        {"format_id": "720", "url": "https://example.invalid/720", "ext": "webm",
         "vcodec": "vp9", "acodec": "none", "height": 720, "tbr": 1500},
        {"format_id": "audio_low", "url": "https://example.invalid/audio-low",
         "ext": "m4a", "vcodec": "none", "acodec": "aac", "tbr": 64},
        {"format_id": "audio_high", "url": "https://example.invalid/audio-high",
         "ext": "m4a", "vcodec": "none", "acodec": "aac", "tbr": 128},
    ]


def case_record(case_id, info, options, expected):
    if "error" not in expected:
        prepared = prepare_pinned(
            info["formats"],
            sort_user=options.get("sort", []),
            sort_extractor=info.get("_format_sort_fields", []),
            prefer_free=options.get("prefer_free_formats", False),
            sort_force=options.get("sort_force", False),
        )
        expected["worst_to_best_source_indexes"] = [
            fmt["__oracle_source_index"] for fmt in prepared
        ]
        expected["worst_to_best_format_ids"] = [fmt["format_id"] for fmt in prepared]
        if "effective_fields" in expected or "effective_fields_first" in expected:
            FormatSorter.settings = copy.deepcopy(BASE_FORMAT_SORTER_SETTINGS)
            sorter = FormatSorter(
                FakeYDL(
                    prefer_free=options.get("prefer_free_formats", False),
                    sort_force=options.get("sort_force", False),
                    sort_user=options.get("sort", []),
                ),
                list(info.get("_format_sort_fields", [])),
            )
            effective = [
                ("+" if sorter._get_field_setting(field, "reverse") else "") + field
                for field in sorter._order
            ]
            expected["effective_fields"] = effective
            expected.pop("effective_fields_first", None)
    return {
        "id": case_id,
        "info": info,
        "options": options,
        "expected": expected,
    }


def case_zero_value_default():
    info = {"formats": [basic_formats()[0]]}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=[], sort_extractor=[])
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "lang",
                              "quality", "res", "fps", "hdr", "vcodec", "channels",
                              "acodec", "size", "br", "asr", "proto", "ext", "hasaud",
                              "source", "id"],
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
        "derived_fields": {
            "protocol": prepared[0].get("protocol"),
            "video_ext": prepared[0].get("video_ext"),
            "audio_ext": prepared[0].get("audio_ext"),
            "vbr": prepared[0].get("vbr"),
            "abr": prepared[0].get("abr"),
            "tbr": prepared[0].get("tbr"),
        },
    }
    return case_record("zero-value-default", info, options, expected)


def case_default_order_full():
    info = {"formats": basic_formats()}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=[], sort_extractor=[])
    expected = {
        "worst_to_best_source_indexes": [2, 3, 0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in (0, 1, 2, 3)],
    }
    return case_record("default-order-full", info, options, expected)


def case_forced_user():
    info = {"formats": basic_formats()}
    options = {"sort": ["+quality"], "sort_force": True, "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=["+quality"], sort_force=True)
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "+quality", "lang", "quality",
                              "res", "fps", "hdr", "vcodec", "channels", "acodec",
                              "size", "br", "asr", "proto", "ext", "hasaud", "source",
                              "id"],
        "worst_to_best_source_indexes": [2, 3, 0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in (0, 1, 2, 3)],
    }
    return case_record("forced-versus-non-forced-user", info, options, expected)


def case_user_before_extractor():
    info = {"formats": [basic_formats()[1]], "_format_sort_fields": ["lang"]}
    options = {"sort": ["+res"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=["+res"], sort_extractor=["lang"])
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "+res",
                              "lang", "res", "fps", "hdr", "vcodec", "channels",
                              "acodec", "size", "br", "asr", "proto", "ext", "hasaud",
                              "source", "id"],
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
    }
    return case_record("user-before-extractor-precedence", info, options, expected)


def case_duplicate_first_occurrence():
    formats = [
        {"format_id": "x", "url": "https://example.invalid/a", "ext": "webm",
         "vcodec": "vp9", "acodec": "none"},
        {"format_id": "x", "url": "https://example.invalid/b", "ext": "webm",
         "vcodec": "vp9", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": ["quality"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["quality"])
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "lang",
                              "quality", "res", "fps", "hdr", "vcodec", "channels",
                              "acodec", "size", "br", "asr", "proto", "ext", "hasaud",
                              "source", "id"],
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in (0, 1)],
    }
    return case_record("duplicate-first-occurrence", info, options, expected)


def case_extractor_sort_fields():
    info = {"formats": [basic_formats()[1]], "_format_sort_fields": ["quality", "tbr"]}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=[],
                             sort_extractor=["quality", "tbr"])
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "lang",
                              "quality", "tbr", "res", "fps", "hdr", "vcodec",
                              "channels", "acodec", "size", "br", "asr", "proto",
                              "ext", "hasaud", "source", "id"],
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
    }
    return case_record("_format_sort_fields", info, options, expected)


def case_repeated_user_fields():
    info = {"formats": basic_formats()}
    options = {"sort": ["quality", "res", "quality"], "sort_force": False,
               "prefer_free_formats": False}
    prepared = prepare_pinned(info["formats"], sort_user=["quality", "res", "quality"])
    expected = {
        "effective_fields": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "lang",
                              "quality", "res", "fps", "hdr", "vcodec", "channels",
                              "acodec", "size", "br", "asr", "proto", "ext", "hasaud",
                              "source", "id"],
        "worst_to_best_source_indexes": [2, 3, 0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in (0, 1, 2, 3)],
    }
    return case_record("repeated-user-fields-options", info, options, expected)


def case_default_codec_rankings():
    formats = [
        {"format_id": "av1", "url": "https://example.invalid/av1", "ext": "mp4",
         "vcodec": "av01.0.05M.08", "acodec": "none"},
        {"format_id": "vp9.2", "url": "https://example.invalid/vp92", "ext": "webm",
         "vcodec": "vp09.02.10.08", "acodec": "none"},
        {"format_id": "vp9", "url": "https://example.invalid/vp9", "ext": "webm",
         "vcodec": "vp9", "acodec": "none"},
        {"format_id": "h264", "url": "https://example.invalid/h264", "ext": "mp4",
         "vcodec": "h264", "acodec": "none"},
        {"format_id": "avc", "url": "https://example.invalid/avc", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [4, 3, 2, 1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(5)],
    }
    return case_record("default-codec-rankings", info, options, expected)


def case_unknown_codec():
    formats = [
        {"format_id": "known", "url": "https://example.invalid/avc", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "unknown", "url": "https://example.invalid/custom", "ext": "mp4",
         "vcodec": "zzz9", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("unknown-codec", info, options, expected)


def case_normal_ext_order():
    formats = [
        {"format_id": "mp4", "url": "https://example.invalid/mp4", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "webm", "url": "https://example.invalid/webm", "ext": "webm",
         "vcodec": "vp9", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("normal-ext-order", info, options, expected)


def case_free_ext_order():
    formats = [
        {"format_id": "mp4", "url": "https://example.invalid/mp4", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "webm", "url": "https://example.invalid/webm", "ext": "webm",
         "vcodec": "vp9", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": True}
    prepared = prepare_pinned(formats, prefer_free=True)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("free-ext-order", info, options, expected)


def case_hdr_order():
    formats = [
        {"format_id": "sdr", "url": "https://example.invalid/sdr", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "dynamic_range": "SDR"},
        {"format_id": "hdr10", "url": "https://example.invalid/hdr10", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "dynamic_range": "HDR10"},
        {"format_id": "hdr10plus", "url": "https://example.invalid/hdr10p", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "dynamic_range": "HDR10+"},
        {"format_id": "hdr12", "url": "https://example.invalid/hdr12", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "dynamic_range": "HDR12"},
        {"format_id": "dv", "url": "https://example.invalid/dv", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "dynamic_range": "DV"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 3, 2, 1, 4],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(5)],
    }
    return case_record("hdr-order", info, options, expected)


def case_protocol_order():
    formats = [
        {"format_id": "https", "url": "https://example.invalid/https",
         "ext": "mp4", "vcodec": "avc1", "acodec": "none", "protocol": "https"},
        {"format_id": "m3u8", "url": "https://example.invalid/m3u8",
         "ext": "mp4", "vcodec": "avc1", "acodec": "none", "protocol": "m3u8_native"},
        {"format_id": "dash", "url": "https://example.invalid/dash",
         "ext": "mp4", "vcodec": "avc1", "acodec": "none", "protocol": "http_dash_segments"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [2, 1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
    }
    return case_record("protocol-order", info, options, expected)


def case_language_preference():
    formats = [
        {"format_id": "en", "url": "https://example.invalid/en", "ext": "mp4",
         "vcodec": "none", "acodec": "aac", "language_preference": 10},
        {"format_id": "es", "url": "https://example.invalid/es", "ext": "mp4",
         "vcodec": "none", "acodec": "aac", "language_preference": -1},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("language-preference", info, options, expected)


def case_preference_hidden():
    formats = [
        {"format_id": "low", "url": "https://example.invalid/low", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "preference": -10},
        {"format_id": "high", "url": "https://example.invalid/high", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("preference-hidden-behavior", info, options, expected)


def case_quality_source():
    formats = [
        {"format_id": "low", "url": "https://example.invalid/low", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "quality": -1, "source_preference": -1},
        {"format_id": "high", "url": "https://example.invalid/high", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "quality": 5, "source_preference": 10},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("quality-and-source-preference", info, options, expected)


def case_resolution():
    formats = [
        {"format_id": "small", "url": "https://example.invalid/small", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 480, "width": 854},
        {"format_id": "large", "url": "https://example.invalid/large", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 1080, "width": 1920},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("resolution", info, options, expected)


def case_fps():
    formats = [
        {"format_id": "30", "url": "https://example.invalid/30", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "fps": 30.0},
        {"format_id": "60", "url": "https://example.invalid/60", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "fps": 60.0},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("fps", info, options, expected)


def case_channels():
    formats = [
        {"format_id": "2", "url": "https://example.invalid/2", "ext": "mp4",
         "vcodec": "none", "acodec": "aac", "audio_channels": 2},
        {"format_id": "6", "url": "https://example.invalid/6", "ext": "mp4",
         "vcodec": "none", "acodec": "aac", "audio_channels": 6},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("channels", info, options, expected)


def case_codec_combined():
    formats = [
        {"format_id": "v", "url": "https://example.invalid/v", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "none", "acodec": "aac"},
        {"format_id": "va", "url": "https://example.invalid/va", "ext": "mp4",
         "vcodec": "avc1", "acodec": "aac"},
    ]
    info = {"formats": formats}
    options = {"sort": ["codec"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["codec"])
    expected = {
        "worst_to_best_source_indexes": [0, 2, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
    }
    return case_record("codec-combined", info, options, expected)


def case_ext_combined():
    formats = [
        {"format_id": "v", "url": "https://example.invalid/v", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "m4a",
         "vcodec": "none", "acodec": "aac"},
    ]
    info = {"formats": formats}
    options = {"sort": ["ext"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["ext"])
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("ext-combined", info, options, expected)


def case_size_and_bitrate():
    formats = [
        {"format_id": "small", "url": "https://example.invalid/small", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "filesize": 1000},
        {"format_id": "approx", "url": "https://example.invalid/approx", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "filesize_approx": 2000},
        {"format_id": "tbr", "url": "https://example.invalid/tbr", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "tbr": 3000},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1, 2],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
    }
    return case_record("size-and-bitrate-multiple", info, options, expected)


def case_exact_limit():
    formats = [
        {"format_id": "small", "url": "https://example.invalid/small", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 480},
        {"format_id": "large", "url": "https://example.invalid/large", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 1080},
    ]
    info = {"formats": formats}
    options = {"sort": ["height:720"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["height:720"])
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("exact-field-limit", info, options, expected)


def case_closest_limit():
    formats = [
        {"format_id": "small", "url": "https://example.invalid/small", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 720},
        {"format_id": "large", "url": "https://example.invalid/large", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 1080},
    ]
    info = {"formats": formats}
    options = {"sort": ["height~800"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["height~800"])
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("closest-field-limit", info, options, expected)


def case_combined_limit():
    formats = [
        {"format_id": "v", "url": "https://example.invalid/v", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "m4a",
         "vcodec": "none", "acodec": "aac"},
    ]
    info = {"formats": formats}
    options = {"sort": ["ext:mp4:m4a"], "sort_force": False,
               "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["ext:mp4:m4a"])
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("combined-field-limits", info, options, expected)


def case_alias_resolution():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "format_id": "x"},
    ]
    info = {"formats": formats}
    options = {"sort": ["format_id"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["format_id"])
    expected = {
        "effective_fields_first": ["hidden", "aud_or_vid", "hasvid", "ie_pref", "lang",
                                    "quality", "res", "fps", "hdr", "vcodec", "channels",
                                    "acodec", "size", "br", "asr", "proto", "ext",
                                    "hasaud", "source", "id"],
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
    }
    return case_record("alias-resolution", info, options, expected)


def case_deprecated_alias():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": ["dimension"], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats, sort_user=["dimension"])
    expected = {
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
    }
    return case_record("deprecated-alias", info, options, expected)


def case_missing_null_values():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "b", "url": "https://example.invalid/b", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 720},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("missing-null-values", info, options, expected)


def case_numeric_string_mixed():
    formats = [
        {"format_id": "n", "url": "https://example.invalid/n", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "quality": "5"},
        {"format_id": "s", "url": "https://example.invalid/s", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "quality": "high"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("numeric-string-mixed", info, options, expected)


def case_stable_ties():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 720, "tbr": 1500},
        {"format_id": "b", "url": "https://example.invalid/b", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 720, "tbr": 1500},
        {"format_id": "c", "url": "https://example.invalid/c", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "height": 720, "tbr": 1500},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1, 2],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
    }
    return case_record("stable-complete-ties", info, options, expected)


def case_derived_protocol_ext():
    formats = [
        {"format_id": "no-ext", "url": "https://example.invalid/track?token=1",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
        "derived_fields": {
            "protocol": prepared[0].get("protocol"),
            "ext": prepared[0].get("ext"),
            "video_ext": prepared[0].get("video_ext"),
        },
    }
    return case_record("derived-protocol-and-ext", info, options, expected)


def case_derived_audio_video_extensions():
    formats = [
        {"format_id": "v", "url": "https://example.invalid/v", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "m4a",
         "vcodec": "none", "acodec": "aac"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
        "derived_fields": {
            "0_video_ext": next(f for f in prepared if f["__oracle_source_index"] == 0).get("video_ext"),
            "0_audio_ext": next(f for f in prepared if f["__oracle_source_index"] == 0).get("audio_ext"),
            "1_video_ext": next(f for f in prepared if f["__oracle_source_index"] == 1).get("video_ext"),
            "1_audio_ext": next(f for f in prepared if f["__oracle_source_index"] == 1).get("audio_ext"),
        },
    }
    return case_record("derived-audio-video-extensions", info, options, expected)


def case_derived_bitrate_fields():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "aac", "tbr": 1000, "vbr": 700, "abr": 300},
        {"format_id": "b", "url": "https://example.invalid/b", "ext": "mp4",
         "vcodec": "avc1", "acodec": "aac", "tbr": 500, "abr": 200},
        {"format_id": "c", "url": "https://example.invalid/c", "ext": "mp4",
         "vcodec": "avc1", "acodec": "aac", "vbr": 400, "abr": 100},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [2, 1, 0],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
    }
    return case_record("derived-bitrate-fields", info, options, expected)


def case_hevc_over_flv():
    formats = [
        {"format_id": "flv", "url": "https://example.invalid/flv", "ext": "flv",
         "vcodec": "h265", "acodec": "aac"},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0],
        "worst_to_best_format_ids": [prepared[0]["format_id"]],
        "derived_fields": {
            "preference": prepared[0].get("preference"),
        },
    }
    return case_record("hevc-over-flv-preference", info, options, expected)


def case_source_index_preservation():
    formats = [
        {"format_id": "z", "url": "https://example.invalid/z", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "preference": 100},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(2)],
    }
    return case_record("source-index-preservation", info, options, expected)


def case_canonical_infojson_order():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "preference": -10},
        {"format_id": "b", "url": "https://example.invalid/b", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
        {"format_id": "c", "url": "https://example.invalid/c", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none", "preference": 100},
    ]
    info = {"formats": formats}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    prepared = prepare_pinned(formats)
    expected = {
        "worst_to_best_source_indexes": [0, 1, 2],
        "worst_to_best_format_ids": [prepared[i]["format_id"] for i in range(3)],
        "canonical_formats": [
            {"format_id": prepared[i]["format_id"],
             "preference": prepared[i].get("preference")}
            for i in range(3)
        ],
    }
    return case_record("canonical-infojson-order", info, options, expected)


def case_sort_bounds():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats}
    options = {"sort": ["a" * 1024], "sort_force": False, "prefer_free_formats": False}
    expected = {
        "error": "ErrInvalidPreference",
        "reason": "oversized sort field",
    }
    return case_record("sort-bounds-oversized", info, options, expected)


def case_malformed_extractor_fields():
    formats = [
        {"format_id": "a", "url": "https://example.invalid/a", "ext": "mp4",
         "vcodec": "avc1", "acodec": "none"},
    ]
    info = {"formats": formats, "_format_sort_fields": [42]}
    options = {"sort": [], "sort_force": False, "prefer_free_formats": False}
    expected = {
        "error": "ErrInvalidPreference",
        "reason": "non-string extractor sort field",
    }
    return case_record("malformed-extractor-fields", info, options, expected)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--fixture",
        type=Path,
        default=WORKTREE_DIR / "internal/format/testdata/format_sorter_conformance.json",
    )
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--verify", action="store_true",
                        help="print SHA-256 of the fixture and exit")
    args = parser.parse_args()

    verify_environment()

    if args.verify:
        body = args.fixture.read_bytes()
        sha = hashlib.sha256(body).hexdigest()
        print("sha256", sha, "bytes", len(body))
        return

    cases = [
        case_zero_value_default(),
        case_default_order_full(),
        case_forced_user(),
        case_user_before_extractor(),
        case_duplicate_first_occurrence(),
        case_extractor_sort_fields(),
        case_repeated_user_fields(),
        case_default_codec_rankings(),
        case_unknown_codec(),
        case_normal_ext_order(),
        case_free_ext_order(),
        case_hdr_order(),
        case_protocol_order(),
        case_language_preference(),
        case_preference_hidden(),
        case_quality_source(),
        case_resolution(),
        case_fps(),
        case_channels(),
        case_codec_combined(),
        case_ext_combined(),
        case_size_and_bitrate(),
        case_exact_limit(),
        case_closest_limit(),
        case_combined_limit(),
        case_alias_resolution(),
        case_deprecated_alias(),
        case_missing_null_values(),
        case_numeric_string_mixed(),
        case_stable_ties(),
        case_derived_protocol_ext(),
        case_derived_audio_video_extensions(),
        case_derived_bitrate_fields(),
        case_hevc_over_flv(),
        case_source_index_preservation(),
        case_canonical_infojson_order(),
        case_sort_bounds(),
        case_malformed_extractor_fields(),
    ]

    payload = render_payload(cases)
    if args.write:
        body = json.dumps(payload, indent=2, ensure_ascii=False, sort_keys=True)
        body += "\n"
        args.fixture.write_text(body)
        sha = hashlib.sha256(body.encode("utf-8")).hexdigest()
        print("wrote", args.fixture, "sha256", sha)
    else:
        print(json.dumps(payload, indent=2, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
