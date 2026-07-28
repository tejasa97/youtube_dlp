#!/usr/bin/env python3
"""Maintainer-only oracle capture for format-filter and Python-regex fixtures.

Requires Python >= 3.10 against the pinned yt-dlp checkout. Go tests, builds,
Docker images, and production never invoke this script.

Example:

  /Users/tejas/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \\
    conformance/compat/format_selector/capture_filter_oracle.py \\
    --reference /Users/tejas/projects/yt-dlp-reference \\
    --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 \\
    --write
"""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
import re
import sys
from pathlib import Path

PINNED_COMMIT = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"
PINNED_PYTHON_VERSION = "3.12.13"
REPO_ROOT = Path(__file__).resolve().parents[3]


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def build_filter_cases(ydl):
    objects = [
        {"id": "empty", "fields": {}},
        {"id": "null_missing", "fields": {"missing": None}},
        {"id": "missing_x", "fields": {"missing": "x"}},
        {"id": "missing_y", "fields": {"missing": "y"}},
        {"id": "fid_aa", "fields": {"format_id": "aa"}},
        {"id": "fid_dash", "fields": {"format_id": "dash-low"}},
        {"id": "fid_quote", "fields": {"format_id": 'a"b'}},
        {"id": "fid_slash", "fields": {"format_id": r"a\b"}},
        {"id": "size_1000", "fields": {"filesize": 1000}},
        {"id": "size_1024", "fields": {"filesize": 1024}},
        {"id": "size_1m", "fields": {"filesize": 1_000_000}},
        {"id": "size_1mi", "fields": {"filesize": 1_048_576}},
        {"id": "size_1500", "fields": {"filesize": 1500}},
        {"id": "size_2500", "fields": {"filesize": 2500}},
        {"id": "size_2p53_plus1", "fields": {"filesize": 9_007_199_254_740_993}},
        {"id": "height_720", "fields": {"height": 720}},
        {"id": "height_str", "fields": {"height": "720"}},
        {"id": "dot_field", "fields": {"field.with-dot": "value"}},
    ]
    specs = [
        "filesize <= ? 3000",
        "filesize>?1",
        "filesize<1M",
        "filesize<1MiB",
        "filesize=1KB",
        "filesize=1kB",
        "filesize=1Kb",
        "filesize=1KiB",
        "filesize=1.5GiB",
        "filesize=1.5KB",
        "filesize=2.5KB",
        "filesize=1M",
        "filesize=1Mi",
        "filesize=1YB",
        "filesize=1YiB",
        "filesize=1ZiB",
        "filesize=1e3",
        "filesize=9007199254740993",
        "filesize=-1",
        "format_id!^=abc",
        'format_id="a\\"b"',
        r'format_id="a\\b"',
        "missing != x",
        "missing != ? x",
        "missing = ? x",
        "field.with-dot=value",
        "format_id=123",
        "height=720",
        "height>700",
        'format_id~="^(?P<x>a)(?P=x)$"',
        'format_id!~="^(?!dash)"',
        'format_id~="(?<=dash)-low$"',
    ]
    specs.append("filesize=" + "9" * 400)  # float() overflows to +inf
    cases = []
    for spec in specs:
        case = {"id": f"filter:{spec}", "spec": spec, "results": []}
        try:
            predicate = ydl._build_format_filter(spec)
        except Exception as error:  # noqa: BLE001
            case["compile_error"] = type(error).__name__
            cases.append(case)
            continue
        for obj in objects:
            entry = {"object_id": obj["id"]}
            try:
                entry["matched"] = bool(predicate(obj["fields"]))
            except Exception as error:  # noqa: BLE001
                entry["error"] = type(error).__name__
            case["results"].append(entry)
        cases.append(case)
    return cases, objects


def regex_case(name: str, pattern: str, text: str, expected: bool | None = None) -> dict:
    case = {"id": name, "pattern": pattern, "input": text}
    try:
        expression = re.compile(pattern)
    except Exception as error:  # noqa: BLE001
        case["compile_error"] = type(error).__name__
        return case
    case["matched"] = expression.search(text) is not None
    if expected is not None and case["matched"] != expected:
        raise AssertionError(
            f"{name}: CPython matched={case['matched']}, expected={expected}"
        )
    return case


def build_regex_cases():
    probes: list[tuple[str, str, str, bool | None]] = [
        # Baseline lookaround / backrefs
        ("lookahead", r"foo(?=bar)", "foobar", True),
        ("negative-lookahead", r"foo(?!bar)", "foobaz", True),
        ("lookbehind", r"(?<=foo)bar", "foobar", True),
        ("numeric-backref", r"^(a+)b\1$", "aaabaaa", True),
        ("python-named-group", r"^(?P<word>\w+)$", "héllo", True),
        ("python-named-backref", r"^(?P<word>\w+)-(?P=word)$", "hé-hé", True),
        ("python-named-group-nl", r"^(?P<Ⅳ>a)(?P=Ⅳ)$", "aa", True),
        ("python-named-group-combining", "^(?P<á>a)(?P=á)$", "aa", True),
        ("python-named-group-other-continue", r"^(?P<a·b>a)(?P=a·b)$", "aa", True),
        ("python-Z-final-newline", r"foo\Z", "foo\n", False),
        ("python-Z-exact", r"foo\Z", "foo", True),
        ("unicode-word-combining", r"^\w+$", "á", False),
        ("unicode-space-fs", r"^\s$", "\x1c", True),
        ("dollar-final-newline", r"foo$", "foo\n", True),
        # Mixed positive/negative shorthands in classes
        ("class-pos-word", r"[a\w]", "é", True),
        ("class-pos-word-ascii", r"(?a)[a\w]", "é", False),
        ("class-neg-word", r"[a\W]", "!", True),
        ("class-neg-word-letter", r"[a\W]", "b", False),
        ("class-negated-outer-neg-word", r"[^a\W]", "b", True),
        ("class-negated-outer-neg-word-a", r"[^a\W]", "a", False),
        ("class-negated-outer-neg-word-bang", r"[^a\W]", "!", False),
        ("class-S-W", r"[\S\W]", " ", True),
        ("class-S-W-letter", r"[\S\W]", "a", True),
        ("class-w-W-all", r"[\w\W]", "!", True),
        ("class-range-right-hex-escape", r"[a-\x7a]", "z", True),
        ("class-range-left-hex-escape", r"[\x61-z]", "m", True),
        ("class-range-left-hex-ignorecase", r"(?i)[\x61-z]", "_", False),
        ("class-range-right-hex-ignorecase", r"(?i)[a-\x7a]", "ı", True),
        # Global / scoped flags including disables
        ("flag-global-i", r"(?i)a", "A", True),
        ("flag-ai-ascii-word", r"(?ai)^\w+$", "é", False),
        ("flag-iu", r"(?iu)^\w+$", "é", True),
        ("flag-scoped-a", r"(?a:\w)", "é", False),
        ("flag-scoped-i-off", r"(?i:a)(?-i:a)", "Aa", True),
        ("flag-scoped-i-off-no-match", r"(?i:a)(?-i:a)", "AA", False),
        ("flag-verbose", "(?x) a # comment\n b", "ab", True),
        ("flag-global-not-start", r"a(?i)b", "aB", None),
        ("flag-au-incompatible", r"(?au)a", "a", None),
        ("flag-disable-a", r"(?-a:a)", "a", None),
        ("flag-global-disable", r"(?i)(?-i)a", "A", None),
        ("flag-enabled-disabled-same", r"(?i-i:a)", "a", None),
        ("flag-missing-after-dash", r"(?-:a)", "a", None),
        ("flag-scoped-u-overrides-a", r"(?a:(?u:\w))", "é", True),
        ("flag-global-a-scoped-u", r"(?a)(?u:\w)", "é", False),
        ("flag-global-a-then-u", r"(?a)(?u)\w", "é", None),
        # Multiline boundaries
        ("multiline-caret", r"(?m)^\w+", "x\ny", True),
        ("boundary-word", r"\ba\b", " a ", True),
        ("boundary-ascii", r"(?a)\b\w\b", "é", False),
        ("nonboundary", r"\Ba\B", "bac", True),
        ("nonboundary-empty", r"\B", "", False),
        ("nonboundary-nonword", r"\B", "!", True),
        # Equal / unequal lookbehind alternatives and nesting
        ("lookbehind-equal-alt", r"(?<=a|b)c", "ac", True),
        ("lookbehind-unequal-alt", r"(?<=a|bc)c", "bcc", None),
        ("lookbehind-nested-fixed", r"(?<=a(?:b|c))d", "abd", True),
        ("lookbehind-group-equal", r"(?<=(ab|cd))e", "abe", True),
        ("lookbehind-named-fixed", r"(?<=(?P<x>ab))c", "abc", True),
        ("lookbehind-variable", r"(?<=a+)b", "aaab", None),
        ("lookbehind-repeat-range", r"(?<=a{2,3})b", "aaab", None),
        ("lookbehind-exact-repeat", r"(?<=a{2})b", "aab", True),
        ("lookbehind-octal", r"(?<=\101)B", "AB", True),
        ("lookbehind-group-repeat-alt", r"(?<=(?:ab){2}|xxxx)c", "ababc", True),
        ("lookbehind-variable-assertion", r"(?<=(?=a+))a", "a", True),
        ("lookbehind-variable-repeat-zero-assertion", r"(?<=(?=a){1,2})a", "a", True),
        ("lookbehind-fixed-backref", r"(ab)(?<=\1)c", "abc", True),
        ("lookbehind-variable-backref", r"(a+)(?<=\1)b", "aab", None),
        ("lookbehind-fixed-conditional", r"(b)?(?<=(?(1)b|c))d", "bd", True),
        ("lookbehind-conditional-without-else-variable", r"(a)?(?<=(?(1)b))c", "abc", None),
        # Ignorecase edge glyphs
        ("ignorecase-i-dotless", r"(?i)i", "ı", True),
        ("ignorecase-i-dotted", r"(?i)i", "İ", True),
        ("ignorecase-long-s", r"(?i)s", "ſ", True),
        ("ignorecase-kelvin", r"(?i)k", "K", True),
        ("ignorecase-class-i", r"(?i)[i]", "ı", True),
        ("ignorecase-class-s", r"(?i)[s]", "ſ", True),
        ("ignorecase-ascii-i-no-dotless", r"(?ai)i", "ı", False),
        ("ignorecase-ascii-class-i", r"(?ai)[i]", "ı", False),
        ("ignorecase-neg-class-i", r"(?i)[^i]", "ı", False),
        ("ignorecase-ascii-letter-range-dotless", r"(?i)[a-z]", "ı", True),
        ("ignorecase-neg-ascii-letter-range-dotless", r"(?i)[^a-z]", "ı", False),
        ("ignorecase-range-micro", "(?i)[°-À]", "Μ", True),
        ("ignorecase-sharp-s", r"(?i)ß", "ẞ", True),
        ("ignorecase-micro-sign", r"(?i)µ", "Μ", True),
        ("ignorecase-micro-sign-u-escape", r"(?i)\u00b5", "Μ", True),
        ("ignorecase-micro-sign-name-escape", r"(?i)\N{MICRO SIGN}", "Μ", True),
        ("ignorecase-micro-sign-class-escape", r"(?i)[\u00b5]", "Μ", True),
        ("ignorecase-final-sigma", r"(?i)σ", "ς", True),
        # Unicode names and aliases
        ("unicode-name-alpha", r"\N{GREEK SMALL LETTER ALPHA}", "α", True),
        ("unicode-name-grinning", r"\N{GRINNING FACE}", "😀", True),
        ("unicode-name-cjk", r"\N{CJK UNIFIED IDEOGRAPH-4E2D}", "中", True),
        ("unicode-name-bom-alias", r"\N{BOM}", "\ufeff", True),
        ("unicode-name-bom-alt", r"\N{BYTE ORDER MARK}", "\ufeff", True),
        ("unicode-name-zwj-alias", r"\N{ZWJ}", "\u200d", True),
        ("unicode-name-zwj-canon", r"\N{ZERO WIDTH JOINER}", "\u200d", True),
        ("unicode-name-lower", r"\N{latin small letter a}", "a", True),
        ("unicode-name-extra-space", r"\N{LATIN  SMALL LETTER A}", "a", None),
        ("unicode-name-pad-space", r"\N{  LATIN SMALL LETTER A  }", "a", None),
        ("unicode-U-escape", r"\U00000061", "a", True),
        ("unicode-name-escape", r"\N{LATIN SMALL LETTER A}", "a", True),
        # Numeric / named backrefs and conditionals
        ("backref-forward-invalid", r"\1(a)", "aa", None),
        ("backref-missing-group", r"(a)\2", "aa", None),
        ("backref-open-group", r"(a\1)", "aa", None),
        ("named-backref-forward", r"(?P=a)(?P<a>.)", "aa", None),
        ("named-group-redef", r"(?P<a>.)(?P<a>.)", "ab", None),
        ("named-backref-open-group", r"(?P<a>a(?P=a))", "aa", None),
        ("conditional-group", r"(a)?(?(1)b|c)", "ab", True),
        ("conditional-group-else", r"(a)?(?(1)b|c)", "c", True),
        ("conditional-named", r"(?P<n>a)?(?(n)b|c)", "ab", True),
        ("conditional-unknown", r"(?(name)b|c)", "b", None),
        ("conditional-assertion", r"(?(?=a)b|c)", "b", None),
        # Octal / reference ambiguity
        ("octal-101", r"\101", "A", True),
        ("octal-1234", r"\1234", "S4", True),
        ("octal-08", r"\08", "\x008", True),
        ("group-then-octal", r"()\07", "\x07", True),
        ("group-invalid-11", r"()\11", "\t", None),
        # Python-invalid / regexp2-valid forms
        ("dotnet-named", r"(?<x>a)", "a", None),
        ("dotnet-quote-named", r"(?'x'a)", "a", None),
        ("dotnet-k-ref", r"(?<x>a)\k<x>", "aa", None),
        ("unicode-category", r"\p{L}", "a", None),
        ("assertion-conditional", r"(?(?=a)b|c)", "b", None),
        ("flag-n", r"(?n)a", "a", None),
        ("flag-after-lookahead", r"(?=a)(?i)b", "ab", None),
        ("flag-after-empty-group", r"(?:)(?i)a", "A", None),
        ("flag-after-comment", r"(?# comment)(?i)a", "A", True),
        ("possessive-quantifier", r"a++a", "aaa", False),
        ("missing-lower-repeat", r"a{,3}", "aa", True),
        ("missing-lower-unbounded-repeat", r"a{,}", "aa", True),
        ("missing-lower-possessive-repeat", r"a{,3}+a", "aaa", False),
        ("literal-braces-plus", r"a{foo}+", "a{foo}}", True),
        ("literal-invalid-repeat-plus", r"a{1x}+", "a{1x}}", True),
        ("literal-empty-braces-plus", r"a{}+", "a{}}", True),
        ("atomic-group", r"(?>a+)a", "aaa", False),
        ("unknown-letter-escape-q", r"\q", "q", None),
        ("unknown-letter-escape-c", r"\c", "c", None),
        ("bad-class-range-shorthand", r"[a-\w]", "a", None),
        ("bad-class-range-neg-shorthand-left", r"[a-\W]", "a", None),
        ("bad-class-range-neg-shorthand-right", r"[\W-a]", "a", None),
        ("bad-class-absolute-escape", r"[\A]", "A", None),
        ("ascii-word-flag", r"(?a)^\w+$", "é", False),
        ("variable-lookbehind", r"(?<=a+)b", "aaab", None),
    ]
    return [regex_case(*probe) for probe in probes]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reference", type=Path, required=True)
    parser.add_argument("--commit", default=PINNED_COMMIT)
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()

    import subprocess

    sha = subprocess.check_output(
        ["git", "-C", str(args.reference), "rev-parse", "HEAD"], text=True
    ).strip()
    if sha != args.commit:
        print(f"reference HEAD {sha} != expected {args.commit}", file=sys.stderr)
        return 2
    if platform.python_version() != PINNED_PYTHON_VERSION:
        print(
            f"CPython {PINNED_PYTHON_VERSION} required; got {platform.python_version()}",
            file=sys.stderr,
        )
        return 2

    sys.path.insert(0, str(args.reference))
    from yt_dlp.YoutubeDL import YoutubeDL  # noqa: WPS433

    ydl = YoutubeDL({"quiet": True})
    filter_cases, objects = build_filter_cases(ydl)
    regex_cases = build_regex_cases()
    derivation = (
        "CPython 3.12.13 conformance/compat/format_selector/capture_filter_oracle.py "
        f"--reference <pinned-yt-dlp-checkout> --commit {args.commit} --write"
    )
    filter_doc = {
        "schema_version": 1,
        "reference": {
            "repository": "https://github.com/yt-dlp/yt-dlp",
            "commit": args.commit,
            "python_version": f"CPython {platform.python_version()}",
            "source": "yt_dlp/YoutubeDL.py:2205-2270 _build_format_filter",
            "derivation": derivation,
        },
        "objects": objects,
        "cases": filter_cases,
    }
    regex_doc = {
        "schema_version": 1,
        "reference": {
            "repository": "https://github.com/yt-dlp/yt-dlp",
            "commit": args.commit,
            "python_version": f"CPython {platform.python_version()}",
            "source": "CPython re module search semantics",
            "derivation": derivation,
        },
        "cases": regex_cases,
    }
    filter_text = json.dumps(filter_doc, ensure_ascii=False, indent=2) + "\n"
    regex_text = json.dumps(regex_doc, ensure_ascii=False, indent=2) + "\n"
    filter_out = REPO_ROOT / "internal" / "format" / "testdata" / "filter_oracle.json"
    regex_out = REPO_ROOT / "internal" / "format" / "testdata" / "python_regex_oracle.json"
    if args.write:
        filter_out.write_text(filter_text, encoding="utf-8")
        regex_out.write_text(regex_text, encoding="utf-8")
    report = {
        "reference_sha": sha,
        "interpreter": f"CPython {platform.python_version()}",
        "derivation": derivation,
        "filter_oracle_sha256": sha256_text(filter_text),
        "python_regex_oracle_sha256": sha256_text(regex_text),
        "wrote": bool(args.write),
    }
    print(json.dumps(report, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
