# Historical desktop snapshot

The desktop application has moved to the independent
[VidStow](https://github.com/tejasa97/vidstow) repository.

This directory is the historical source snapshot used for the filtered-history
extraction. It is retained temporarily for migration traceability and must not
be treated as the current VidStow source, build guide, release configuration,
or supported product boundary.

Develop and package the desktop application from a VidStow checkout:

```sh
git clone https://github.com/tejasa97/vidstow.git
cd vidstow
```

VidStow uses tagged `github.com/tejasa97/youtube_dlp` releases and explicitly
composes root `engine` with `providers/youtube`.
