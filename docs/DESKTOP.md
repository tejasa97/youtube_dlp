# Desktop application

The focused desktop application is now maintained as
[VidStow](https://github.com/tejasa97/vidstow), an independent open-source
project with its own versioning, documentation, release packaging, and product
scope.

VidStow currently provides a native Wails/Svelte workflow for public,
single-video YouTube URLs. Its repository is the source of truth for supported
UI workflows, quality presets, queue behavior, settings, local data, build
requirements, screenshots, and releases.

## Engine relationship

VidStow depends on tagged releases of this module and constructs the focused
runtime explicitly:

```text
VidStow UI
    │
    ├── engine
    └── providers/youtube
```

Both metadata analysis and downloads use the same focused YouTube composition.
VidStow does not import the broad `pkg/ytdlp` facade or the mixed extractor
catalog.

The UI remains the product boundary: an engine capability is not automatically
a VidStow feature. New desktop workflows belong in the VidStow repository and
should be exposed only when the corresponding user experience is designed,
tested, and documented.

## Historical snapshot

`apps/desktop` remains temporarily in this repository as the source snapshot
from which VidStow was extracted. Do not develop or package the desktop product
from that directory. Current work belongs in
[`tejasa97/vidstow`](https://github.com/tejasa97/vidstow).
