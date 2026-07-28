#!/usr/bin/env python3
"""Maintainer-only oracle capture for format-selector foundation fixtures.

Requires Python >= 3.10 against the pinned yt-dlp checkout. Go tests, builds,
Docker images, and production never invoke this script.

Example:

  python3 conformance/compat/format_selector/capture_oracle.py \\
    --reference /Users/tejas/projects/yt-dlp-reference \\
    --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8
"""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
import sys
from pathlib import Path

INT64_MIN = - (1 << 63)
INT64_MAX = (1 << 63) - 1
PINNED_COMMIT = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"
REPO_ROOT = Path(__file__).resolve().parents[3]


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    digest.update(path.read_bytes())
    return digest.hexdigest()


def go_expected(int_or_none, value):
    result = int_or_none(value)
    if result is None:
        return None
    if isinstance(result, int) and (result > INT64_MAX or result < INT64_MIN):
        return None
    return result


def build_int_or_none_cases(int_or_none):
    cases = []

    def add(case_id: str, payload: dict, py_value):
        cases.append(
            {
                "id": case_id,
                "input": payload,
                "expected": go_expected(int_or_none, py_value),
            }
        )

    add("null", {"type": "null"}, None)
    add("bool_true", {"type": "bool", "bool": True}, True)
    add("bool_false", {"type": "bool", "bool": False}, False)
    add("int", {"type": "int", "value": 42}, 42)
    add("neg", {"type": "int", "value": -7}, -7)
    add("float_trunc", {"type": "float", "float": 3.9}, 3.9)
    add("float_neg", {"type": "float", "float": -2.1}, -2.1)
    add("str_plain", {"type": "string", "text": "42"}, "42")
    add("str_spaces", {"type": "string", "text": "  42  "}, "  42  ")
    add("str_plus", {"type": "string", "text": "+5"}, "+5")
    add("str_minus", {"type": "string", "text": "-5"}, "-5")
    add("str_underscore", {"type": "string", "text": "1_000"}, "1_000")
    add("str_multi_underscore", {"type": "string", "text": "1_2_3"}, "1_2_3")
    add("str_double_underscore", {"type": "string", "text": "1__0"}, "1__0")
    add("str_unicode_arabic", {"type": "string", "text": "١٢٣"}, "١٢٣")
    add("str_unicode_fullwidth", {"type": "string", "text": "１２３"}, "１２３")
    add("str_unicode_underscore", {"type": "string", "text": "١_٢"}, "١_٢")
    add("str_float", {"type": "string", "text": "42.5"}, "42.5")
    add("str_hex", {"type": "string", "text": "0x10"}, "0x10")
    add("str_empty", {"type": "string", "text": ""}, "")
    add("str_bad", {"type": "string", "text": "nope"}, "nope")
    add("int64_max", {"type": "string", "text": str(INT64_MAX)}, str(INT64_MAX))
    add("int64_max_plus", {"type": "string", "text": str(INT64_MAX + 1)}, str(INT64_MAX + 1))
    add("int64_min", {"type": "string", "text": str(INT64_MIN)}, str(INT64_MIN))
    add("int64_min_minus", {"type": "string", "text": str(INT64_MIN - 1)}, str(INT64_MIN - 1))
    return cases


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", type=Path, required=True, help="pinned yt-dlp checkout")
    parser.add_argument("--commit", default=PINNED_COMMIT, help="expected reference SHA")
    parser.add_argument(
        "--write",
        action="store_true",
        help="write int_or_none_oracle.json into the repository",
    )
    args = parser.parse_args()

    import subprocess

    sha = subprocess.check_output(
        ["git", "-C", str(args.reference), "rev-parse", "HEAD"],
        text=True,
    ).strip()
    if sha != args.commit:
        print(f"reference HEAD {sha} != expected {args.commit}", file=sys.stderr)
        return 2
    if sys.version_info < (3, 10):
        print(f"Python >= 3.10 required; got {platform.python_version()}", file=sys.stderr)
        return 2

    sys.path.insert(0, str(args.reference))
    from yt_dlp.utils import int_or_none  # noqa: WPS433

    doc = {
        "schema_version": 1,
        "reference": {
            "repository": "https://github.com/yt-dlp/yt-dlp",
            "commit": args.commit,
            "source": "yt_dlp/utils/_utils.py:2029-2038 int_or_none",
            "python_version": f"CPython {platform.python_version()}",
        },
        "cases": build_int_or_none_cases(int_or_none),
    }
    text = json.dumps(doc, ensure_ascii=False, indent=2) + "\n"
    out = REPO_ROOT / "internal" / "format" / "testdata" / "int_or_none_oracle.json"
    if args.write:
        out.write_text(text, encoding="utf-8")

    corpus = REPO_ROOT / "internal" / "format" / "testdata" / "selector_conformance.json"
    report = {
        "reference_sha": sha,
        "interpreter": f"CPython {platform.python_version()}",
        "int_or_none_oracle_sha256": sha256_text(text),
        "selector_conformance_sha256": sha256_file(corpus) if corpus.exists() else None,
        "wrote": bool(args.write),
        "output": str(out.relative_to(REPO_ROOT)),
    }
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
