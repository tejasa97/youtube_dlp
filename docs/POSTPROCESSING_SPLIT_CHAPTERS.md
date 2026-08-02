# Split-chapters post-processing boundary

`--split-chapters` is a bounded typed post-processing operation. It reads the
final chapter timeline after chapter removal/SponsorBlock processing and writes
one stream-copy media artifact per chapter. The primary media remains the
archive-bearing `Result.Filename`; chapter files are additional `chapter`
artifacts and do not create additional archive records.

Chapter names are rendered beneath `OutputPathChapter` (the CLI `chapter:`
path class) with section fields `section_number`, `section_title`,
`section_start`, and `section_end`. The default is
`%(title)s-%(section_number)03d-%(section_title)s.%(ext)s`; an explicit
`OutputTemplateChapter` overrides it. Rendering uses the existing bounded
filename sanitizer and root confinement. Duplicate rendered names, symlinks,
non-regular existing destinations, unsafe paths, and post-overwrite conflicts
fail closed.

The ffmpeg boundary stages every chapter in private same-directory temporary
directories and publishes the complete set only after every extraction
succeeds. Cancellation, ffmpeg failure, or a later lifecycle failure removes
all staged/partial chapters and the surrounding output transaction restores
the original destination and archive state. Chapter count is limited to the
existing 1,000-entry bound and each chapter is limited to 24 hours.
