"""Dependency and license audit (Blueprint D.1 P9, §16.11 license policy, ADR-0051).

Lists every Go module compiled into the shipped binaries (cmd/aura, cmd/aulvm) and the worker's Python runtime
dependencies, classifies each license from its license file, and fails on strong or network copyleft (GPL, AGPL,
SSPL) or on a license that cannot be identified. Weak copyleft (LGPL, MPL, EPL) is reported for owner/legal approval.
Run inside the dev container: ./dev audit
"""
import json
import pathlib
import subprocess
import sys
import tomllib

ALLOWED = {"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", "Unlicense", "PublicDomain"}
PRIMARY = {"LICENSE", "LICENSE.MD", "LICENSE.TXT", "LICENCE", "LICENCE.MD", "LICENCE.TXT", "COPYING", "COPYING.MD", "COPYING.TXT"}
WEAK = {"MPL-2.0", "LGPL", "EPL"}
FORBIDDEN = {"GPL", "AGPL", "SSPL"}


def classify(text: str) -> str:
    t = " ".join(text.split()).lower()
    if "mozilla public license" in t and "2.0" in t:  # first: the MPL text itself names the GPL family
        return "MPL-2.0"
    if "gnu affero general public license" in t:
        return "AGPL"
    if "server side public license" in t:
        return "SSPL"
    if "gnu lesser general public license" in t or "gnu library general public license" in t:
        return "LGPL"
    if "gnu general public license" in t:
        return "GPL"
    if "eclipse public license" in t:
        return "EPL"
    if "apache license" in t and "version 2.0" in t:
        return "Apache-2.0"
    if "permission is hereby granted, free of charge" in t:
        return "MIT"
    if "permission to use, copy, modify, and/or distribute this software for any purpose" in t or (
            "permission to use, copy, modify, and distribute this software for any purpose" in t and "isc" in t):
        return "ISC"
    if "redistribution and use in source and binary forms" in t:
        if "neither the name" in t or "may be used to endorse or promote" in t:
            return "BSD-3-Clause"
        return "BSD-2-Clause"
    if "this is free and unencumbered software released into the public domain" in t:
        return "Unlicense"
    if "dedicated to the public domain" in t:
        return "PublicDomain"
    return "UNKNOWN"


def license_of(d: pathlib.Path) -> tuple[str, str, list[str]]:
    """The module's own license (its primary license file) and the notice files it ships.

    Files such as LICENSE-3RD-PARTY.md aggregate other projects' licenses (for example the MPL-2.0 text, which
    names the GPL family as "Secondary Licenses"); they are notices to carry, not the module's license. The
    components they cover are audited on their own when they are compiled in (go list -deps).
    """
    files = sorted(p for p in d.iterdir() if p.is_file() and p.name.upper().startswith(("LICENSE", "LICENCE", "COPYING")))
    primary = [p for p in files if p.name.upper() in PRIMARY] or files[:1]
    notices = [p.name for p in files if p not in primary]
    if not primary:
        return "UNKNOWN", "", notices
    kinds = {classify(p.read_text(errors="replace")) for p in primary}
    kinds.discard("UNKNOWN")
    for p in files:  # bundled components with their own license file (e.g. LICENSE-SQLITE)
        if p not in primary and "3RD" not in p.name.upper() and "THIRD" not in p.name.upper():
            k = classify(p.read_text(errors="replace"))
            if k != "UNKNOWN":
                kinds.add(k)
    if not kinds:
        return "UNKNOWN", primary[0].name, notices
    return "+".join(sorted(kinds)), ",".join(p.name for p in primary), notices


def go_modules(root: pathlib.Path) -> list[dict]:
    fmt = "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}"
    out = {}
    for target, env in (("./cmd/aura", {}), ("./cmd/aulvm", {"GOOS": "wasip1", "GOARCH": "wasm"})):
        res = subprocess.run(["go", "list", "-deps", "-f", fmt, target], cwd=root, capture_output=True, text=True, check=True,
                             env={**__import__("os").environ, **env})
        for line in res.stdout.splitlines():
            parts = line.split("\t")
            if len(parts) == 3 and parts[1]:  # the main module has no version
                out[parts[0]] = {"module": parts[0], "version": parts[1], "dir": parts[2]}
    rows = []
    for m in sorted(out.values(), key=lambda x: x["module"]):
        lic, f, notices = license_of(pathlib.Path(m["dir"]))
        rows.append({"ecosystem": "go", "name": m["module"], "version": m["version"], "license": lic, "file": f, "notices": notices})
    return rows


def python_runtime(root: pathlib.Path) -> list[dict]:
    proj = tomllib.loads((root / "intelligence/python/pyproject.toml").read_text())
    return [{"ecosystem": "python", "name": d, "version": "", "license": "SEE-PACKAGE", "file": ""}
            for d in proj["project"].get("dependencies", [])]


def verdict(lic: str) -> str:
    parts = set(lic.split("+"))
    if parts & FORBIDDEN:
        return "FORBIDDEN"
    if parts & WEAK:
        return "NEEDS_APPROVAL"
    if parts <= ALLOWED:
        return "OK"
    return "UNKNOWN"


def self_test() -> None:
    """The classifier must not miss copyleft, and must not mistake the MPL text for the AGPL."""
    cases = {
        "GNU AFFERO GENERAL PUBLIC LICENSE Version 3": "AGPL",
        "GNU GENERAL PUBLIC LICENSE Version 2, June 1991": "GPL",
        "GNU LESSER GENERAL PUBLIC LICENSE Version 2.1": "LGPL",
        "Mozilla Public License Version 2.0 ... Secondary License means either the GNU General Public License, Version 2.0, "
        "the GNU Lesser General Public License, Version 2.1, the GNU Affero General Public License, Version 3.0": "MPL-2.0",
        "Server Side Public License VERSION 1": "SSPL",
        "Permission is hereby granted, free of charge, to any person": "MIT",
        "Apache License Version 2.0, January 2004": "Apache-2.0",
        "Redistribution and use in source and binary forms ... Neither the name of": "BSD-3-Clause",
    }
    for text, want in cases.items():
        got = classify(text)
        assert got == want, f"classify({text[:40]!r}) = {got}, want {want}"
    assert verdict("AGPL+MIT") == "FORBIDDEN" and verdict("MPL-2.0") == "NEEDS_APPROVAL" and verdict("UNKNOWN") == "UNKNOWN"


def main() -> int:
    self_test()
    repo = pathlib.Path(__file__).resolve().parents[2]
    rows = go_modules(repo / "core/go") + python_runtime(repo)
    for r in rows:
        r["verdict"] = verdict(r["license"])
    bad = [r for r in rows if r["verdict"] in ("FORBIDDEN", "UNKNOWN")]
    report = {"components": rows, "counts": {v: sum(1 for r in rows if r["verdict"] == v)
                                             for v in ("OK", "NEEDS_APPROVAL", "UNKNOWN", "FORBIDDEN")},
              "not_covered": ["browser image: Playwright 1.49.1 (Apache-2.0) and Chromium 131 (BSD-3-Clause and bundled third-party "
                              "licenses), pinned upstream images, not redistributed as AURA source",
                              "python.wasm: CPython 3.12 (PSF-2.0, permissive) from vmware-labs webassembly-language-runtimes",
                              "base images: python:3.12-slim (Debian), golang:1.26-bookworm (build only)"]}
    json.dump(report, sys.stdout, indent=1)
    print()
    for r in rows:
        print(f"{r['verdict']:15} {r['license']:22} {r['ecosystem']:6} {r['name']} {r['version']}", file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
