"""Contract test: validate semglot's okf bundle with the OKF reference implementation.

`test/okf_conformance_test.go` re-implements the reference implementation's rules
in Go so CI always has a check. This runs the real thing, so it catches the case
where our reading of those rules drifts from upstream.

It is not wired into CI (that would put a Python toolchain and a network fetch on
every build). Run it by hand when touching the okf emitter, and when upstream
changes:

    git clone --depth 1 https://github.com/GoogleCloudPlatform/knowledge-catalog /tmp/kc
    python3 -m venv /tmp/okf-venv
    /tmp/okf-venv/bin/pip install -e '/tmp/kc/okf[dev]'
    UPDATE_GOLDEN=1 go test ./test/ -run TestOKFGolden   # refresh the bundle
    /tmp/okf-venv/bin/python test/okf_contract_test.py test/models/ecommerce/dbt/okf

Verified against knowledge-catalog @ main, 2026-07-20.

Two checks:

1. Every concept passes OKFDocument.validate(). Note that the reference
   implementation is STRICTER than SPEC.md, which requires only `type`: it also
   requires title, description and timestamp to be non-empty.
2. regenerate_indexes() is a no-op on our bundle. index.md is reserved and
   machine-generated upstream, so if ours differs, their tooling silently
   rewrites it and every subsequent diff is noise.
"""

import shutil
import sys
import tempfile
from pathlib import Path

try:
    from reference_agent.bundle.document import OKFDocument
    from reference_agent.bundle.index import regenerate_indexes
except ImportError:
    sys.exit(
        "reference_agent not importable. Install it first:\n"
        "  git clone --depth 1 https://github.com/GoogleCloudPlatform/knowledge-catalog /tmp/kc\n"
        "  python3 -m venv /tmp/okf-venv && /tmp/okf-venv/bin/pip install -e '/tmp/kc/okf[dev]'"
    )

RESERVED = {"index.md", "log.md"}


def check_documents(bundle: Path) -> list[str]:
    failures = []
    for md in sorted(bundle.rglob("*.md")):
        if md.name in RESERVED:
            continue
        try:
            OKFDocument.parse(md.read_text(encoding="utf-8")).validate()
        except Exception as e:
            failures.append(f"{md.relative_to(bundle)}: {e}")
    return failures


def check_indexes_are_stable(bundle: Path) -> list[str]:
    """Regenerate indexes in a copy and diff them against ours."""
    with tempfile.TemporaryDirectory() as tmp:
        copy = Path(tmp) / "bundle"
        shutil.copytree(bundle, copy)
        # synthesize is the upstream LLM call for directory descriptions; stub it
        # so the check stays offline and deterministic. Our directory
        # descriptions are static, so we assert ours are preserved by passing
        # them straight back.
        regenerate_indexes(copy, synthesize=lambda rel, pairs, **kw: _our_dir_desc(bundle, rel))

        failures = []
        for theirs in sorted(copy.rglob("index.md")):
            rel = theirs.relative_to(copy)
            ours = bundle / rel
            if not ours.exists():
                failures.append(f"{rel}: reference tooling writes this index, we do not")
            elif ours.read_text() != theirs.read_text():
                failures.append(
                    f"{rel}: differs from regenerate_indexes output\n"
                    f"--- ours ---\n{ours.read_text()}\n--- theirs ---\n{theirs.read_text()}"
                )
        return failures


def _our_dir_desc(bundle: Path, rel: str) -> str:
    """Our own description for a subdirectory, read back out of the root index."""
    root = (bundle / "index.md").read_text().splitlines()
    for line in root:
        if line.startswith(f"* [{rel}]("):
            _, _, desc = line.partition(") - ")
            return desc
    return ""


def main() -> int:
    if len(sys.argv) != 2:
        sys.exit(f"usage: {sys.argv[0]} <bundle-dir>")
    bundle = Path(sys.argv[1])
    if not bundle.is_dir():
        sys.exit(f"not a directory: {bundle}")

    failures = check_documents(bundle) + check_indexes_are_stable(bundle)
    for f in failures:
        print(f"FAIL {f}")
    if failures:
        print(f"\n{len(failures)} failure(s)")
        return 1
    print(f"OK: {bundle} validates against the OKF reference implementation")
    return 0


if __name__ == "__main__":
    sys.exit(main())
