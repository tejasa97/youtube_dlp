#!/bin/sh
set -eu

app_path=${1:-}
if [ -z "$app_path" ]; then
  echo "verify-js-helper: missing app or executable path" >&2
  exit 2
fi

case "$app_path" in
  */Contents/MacOS/*)
    helper_path=$(CDPATH= cd -- "$(dirname -- "$app_path")" && pwd)/ytdlp-js-helper
    ;;
  *)
    helper_path=$(CDPATH= cd -- "$(dirname -- "$app_path")" && pwd)/ytdlp-js-helper
    ;;
esac

if [ ! -f "$helper_path" ] || [ -L "$helper_path" ] || [ ! -x "$helper_path" ]; then
  echo "verify-js-helper: missing regular executable sibling: $helper_path" >&2
  exit 1
fi

case "$helper_path" in
  */ytdlp-js-helper) ;;
  *) echo "verify-js-helper: helper is not the required sibling name" >&2; exit 1 ;;
esac

if ! "$helper_path" --version >/dev/null 2>&1; then
  echo "verify-js-helper: helper --version failed: $helper_path" >&2
  exit 1
fi
