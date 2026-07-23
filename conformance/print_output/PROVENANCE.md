# Staged print-output provenance

Behavioral expectations were derived from the read-only yt-dlp checkout at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Relevant pinned sources:

- `yt_dlp/options.py`: `-O`/`--print`, `--print-to-file`, legacy `--get-*`
  options, and lifecycle stage names;
- `yt_dlp/__init__.py`: implicit quiet and simulation rules and legacy output
  mapping;
- `yt_dlp/YoutubeDL.py`: `_forceprint`, append-to-file ordering, field and
  filename templates, dictionary and trailing-`=` shorthand expansion,
  compact/indented JSON conversions, derived filename, selected URL/format
  fields, optional legacy fields, and lifecycle dispatch;
- `yt_dlp/utils/_utils.py`: legacy duration formatting.

Pinned differential expectations include `title,id`, `{id,title}`, `title=`,
and `{id,title}=` normalization, missing projected-field omission, and the
four-space indentation used by `#j`.

All automated fixtures are repository-authored and deterministic. Production,
test, and build execution does not invoke Python or depend on the reference
checkout.
