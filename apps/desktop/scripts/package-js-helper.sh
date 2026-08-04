#!/bin/sh
set -eu

app_path=${1:-}
if [ -z "$app_path" ]; then
  echo "package-js-helper: missing Wails output path" >&2
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
desktop_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
repo_dir=$(CDPATH= cd -- "$desktop_dir/../.." && pwd)

case "$app_path" in
  */Contents/MacOS/*)
    helper_dir=$(CDPATH= cd -- "$(dirname -- "$app_path")" && pwd)
    app_bundle=${app_path%/Contents/MacOS/*}
    ;;
  *)
    helper_dir=$(CDPATH= cd -- "$(dirname -- "$app_path")" && pwd)
    app_bundle=
    ;;
esac

mkdir -p "$helper_dir"
helper_path="$helper_dir/ytdlp-js-helper"
temporary_path="$helper_path.tmp.$$"
trap 'rm -f "$temporary_path"' EXIT HUP INT TERM

printf '%s\n' "package-js-helper: building sibling helper"
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -o "$temporary_path" ./cmd/ytdlp-js-helper)
chmod 755 "$temporary_path"
mv -f "$temporary_path" "$helper_path"

"$script_dir/verify-js-helper.sh" "$app_path"

if [ "$(uname -s)" = "Darwin" ] && [ -n "$app_bundle" ] && [ -d "$app_bundle" ]; then
  printf '%s\n' "package-js-helper: re-signing bundle after helper placement"
  /usr/bin/codesign --force --deep --sign - "$app_bundle"
  /usr/bin/codesign --verify --deep --strict "$app_bundle"
fi

printf '%s\n' "package-js-helper: verified $helper_path"
