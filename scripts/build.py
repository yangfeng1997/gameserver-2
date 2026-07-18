#!/usr/bin/env python3
"""Build service binaries and copy into run/<svc>/bin."""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

import yaml

DEFAULT_SERVICES = ["gatesvr", "lobbysvr"]


def main() -> None:
    parser = argparse.ArgumentParser(description="Build runtime service binaries")
    parser.add_argument("--out", default="run", help="runtime output directory")
    parser.add_argument("--build", default="build", help="build output directory")
    parser.add_argument("--svr", default="", help="build only one service")
    args = parser.parse_args()

    root = _project_root()
    out_dir = root / args.out
    build_dir = root / args.build
    env = _read_env(out_dir / "ENV")
    services = _read_services(root / "config" / "values" / f"{env}.yaml")
    if args.svr:
        if args.svr not in services:
            sys.exit(f"ERROR: {args.svr} not in svr_list ({','.join(services)})")
        services = [args.svr]
    if not services:
        services = DEFAULT_SERVICES[:]

    print(
        f"build env={env} services={','.join(services)} "
        f"build={build_dir} out={out_dir}"
    )
    _validate_dirs(root, out_dir, services)
    build_dir.mkdir(parents=True, exist_ok=True)

    failed: list[str] = []
    for svc in services:
        if not _build_svc(root, build_dir, out_dir, svc):
            failed.append(svc)

    if failed:
        sys.exit(f"ERROR: build failed: {', '.join(failed)}")
    print("build done")


# ---- internal helpers ----


def _project_root() -> Path:
    return Path(__file__).resolve().parents[1]


def _read_env(path: Path) -> str:
    if not path.exists():
        sys.exit(
            f"ERROR: {path} not found, run 'make config ENV=dev WORLDID=1' first"
        )
    env = path.read_text(encoding="utf-8").strip()
    if not env:
        sys.exit(f"ERROR: {path} is empty")
    return env


def _read_services(path: Path) -> list[str]:
    if not path.exists():
        sys.exit(f"ERROR: values file not found: {path}")
    with path.open("r", encoding="utf-8") as f:
        data: dict[str, Any] = yaml.safe_load(f) or {}
    services = data.get("svr_list") or []
    if not isinstance(services, list):
        sys.exit("ERROR: svr_list must be a yaml list")
    return [str(s) for s in services]


def _validate_dirs(root: Path, out_dir: Path, services: list[str]) -> None:
    for svc in services:
        cmd_dir = root / "cmd" / svc
        if not cmd_dir.is_dir():
            sys.exit(
                f"ERROR: cmd directory missing for {svc}: {cmd_dir}"
            )
        conf_dir = out_dir / svc / "conf"
        if not conf_dir.is_dir():
            sys.exit(
                f"ERROR: runtime config missing for {svc}: {conf_dir}; "
                "run 'make config ENV=... WORLDID=...' first"
            )
        (out_dir / svc / "bin").mkdir(parents=True, exist_ok=True)
        (out_dir / svc / "log").mkdir(parents=True, exist_ok=True)


def _build_svc(
    root: Path, build_dir: Path, out_dir: Path, svc: str
) -> bool:
    exe_suffix = ".exe" if os.name == "nt" else ""
    binary = build_dir / f"{svc}{exe_suffix}"
    dst = out_dir / svc / "bin" / f"{svc}{exe_suffix}"
    print(f"=== building {svc} ===")
    result = subprocess.run(
        ["go", "build", "-o", str(binary), f"./cmd/{svc}"],
        cwd=root,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"ERROR: build {svc} failed", file=sys.stderr)
        print(result.stderr, file=sys.stderr)
        return False
    if dst.exists():
        dst.unlink()
    shutil.copy2(binary, dst)
    try:
        dst.chmod(0o755)
    except OSError:
        pass
    print(f"  {binary} -> {dst}")
    return True


if __name__ == "__main__":
    main()
