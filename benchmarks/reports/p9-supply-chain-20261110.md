# P9 supply-chain checks — 2026-11-10

Evidence level: implementation-verified (commands run on the production host; output below).

## 1. Reproducible build (`sudo deploy/checks/reproducible-build.sh`)

| File | Build 1 sha256 | Build 2 sha256 | Same |
|---|---|---|---|
| aura | `e2d8f0d2e366425fb1143f5ee628fbb6cb868ad0f6d82e8201f80153e62a0087` | `e2d8f0d2e366425fb1143f5ee628fbb6cb868ad0f6d82e8201f80153e62a0087` | yes |
| aulvm.wasm | `e3fb8a800bbe3b293d1dd8a8e05f5c599b67e80ce588c4767c117263fdb6de76` | `e3fb8a800bbe3b293d1dd8a8e05f5c599b67e80ce588c4767c117263fdb6de76` | yes |

Two builds without cache from commit 4c5fb2b (working tree had only these check scripts uncommitted). Cross-check: the production AUL VM (release 0.10.0-p8) recorded vm_sha256 `e3fb8a80…` in its AUL_STARTED event, which matches the rebuilt module.

Not covered: the runtime image as a whole (the Python base image and apt layers change upstream); the image's files are pinned by the signed release manifest instead (ADR-0037).

## 2. Dependency and license audit (`sudo ./dev audit`)

Counts: {'OK': 34, 'NEEDS_APPROVAL': 0, 'UNKNOWN': 0, 'FORBIDDEN': 0}

```
OK              BSD-3-Clause           go     github.com/atotto/clipboard v0.1.4
OK              MIT                    go     github.com/aymanbagabas/go-osc52/v2 v2.0.1
OK              MIT                    go     github.com/charmbracelet/bubbles v1.0.0
OK              MIT                    go     github.com/charmbracelet/bubbletea v1.3.10
OK              MIT                    go     github.com/charmbracelet/colorprofile v0.4.1
OK              MIT                    go     github.com/charmbracelet/lipgloss v1.1.0
OK              MIT                    go     github.com/charmbracelet/x/ansi v0.11.6
OK              MIT                    go     github.com/charmbracelet/x/cellbuf v0.0.15
OK              MIT                    go     github.com/charmbracelet/x/term v0.2.2
OK              MIT                    go     github.com/clipperhouse/displaywidth v0.9.0
OK              MIT                    go     github.com/clipperhouse/stringish v0.1.1
OK              MIT                    go     github.com/clipperhouse/uax29/v2 v2.5.0
OK              MIT                    go     github.com/dustin/go-humanize v1.1.0
OK              BSD-3-Clause           go     github.com/google/uuid v1.6.0
OK              MIT                    go     github.com/lucasb-eyer/go-colorful v1.3.0
OK              MIT                    go     github.com/mattn/go-isatty v0.0.24
OK              MIT                    go     github.com/mattn/go-runewidth v0.0.19
OK              MIT                    go     github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6
OK              MIT                    go     github.com/muesli/cancelreader v0.2.2
OK              MIT                    go     github.com/muesli/termenv v0.16.0
OK              Apache-2.0             go     github.com/oklog/ulid/v2 v2.1.1
OK              BSD-3-Clause           go     github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec
OK              MIT                    go     github.com/rivo/uniseg v0.4.7
OK              Apache-2.0             go     github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
OK              Apache-2.0             go     github.com/tetratelabs/wazero v1.9.0
OK              MIT                    go     github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e
OK              BSD-3-Clause           go     golang.org/x/crypto v0.31.0
OK              BSD-3-Clause           go     golang.org/x/sys v0.49.0
OK              BSD-3-Clause           go     golang.org/x/text v0.21.0
OK              Apache-2.0             go     gopkg.in/yaml.v3 v3.0.1
OK              BSD-3-Clause           go     modernc.org/libc v1.80.1
OK              BSD-3-Clause           go     modernc.org/mathutil v1.7.1
OK              BSD-3-Clause           go     modernc.org/memory v1.12.1
OK              BSD-3-Clause+MIT+PublicDomain go     modernc.org/sqlite v1.62.0
```

- The worker has no runtime Python dependencies (pyproject `dependencies = []`).
- modernc.org/sqlite ships LICENSE-3RD-PARTY.md, whose only copyleft-adjacent component (hashicorp/golang-lru, MPL-2.0) is in its module graph but not compiled in; `go list -deps` confirms it is not in the AURA binaries. An earlier run flagged AGPL from that file: a false positive (the MPL-2.0 text names the AGPL as a "Secondary License"); the classifier now checks MPL first and has a self-test.
- Not covered by the script: the browser image (Playwright 1.49.1 Apache-2.0, Chromium 131 BSD-3-Clause with bundled notices), python.wasm (CPython 3.12, PSF-2.0), base images (Debian).
