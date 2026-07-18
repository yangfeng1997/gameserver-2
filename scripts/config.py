#!/usr/bin/env python3
"""Prepare runtime config directories from config/server/ templates.

Templates live under config/server/<service>/*.yaml.
Common config (config/server/common/common.yaml) is baked into each service's
conf/ directory so every service uses the same relative path ../conf/common.yaml.

Examples:
  python scripts/config.py --env dev --world-id 1
  python scripts/config.py --env dev --world-id 1 --dry-run
  python scripts/config.py --env dev --world-id 1 --svr gatesvr
"""

from __future__ import annotations

import argparse
import shutil
import sys
from pathlib import Path
from typing import Any

import yaml

from config_bake import bake_file, load_values

# Maps service name -> server type id for node_id generation.
SERVER_TYPES: dict[str, int] = {
    "routeragent": 6,
    "gatesvr": 1,
    "lobbysvr": 2,
    "roomsvr": 3,
    "matchsvr": 4,
    "onlinesvr": 5,
}


def main() -> None:
    parser = argparse.ArgumentParser(description="Bake runtime configs from config/server/")
    parser.add_argument("--env", default="dev")
    parser.add_argument("--out", default="run")
    parser.add_argument("--svr", default="", help="bake only one service")
    parser.add_argument("--world-id", required=True, type=_parse_world_id)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    root = _project_root()
    out_dir = root / args.out
    values_path = root / "config" / "values" / f"{args.env}.yaml"
    conf_dir = root / "config" / "server"
    common_template = conf_dir / "common" / "common.yaml"

    values_raw = _load_yaml(values_path)
    values = load_values(values_path)
    values["world_id"] = str(args.world_id)
    values.setdefault("cluster_name", "gameserver")
    values.setdefault("cluster_env", args.env)

    services = _services_from_values(values_raw)
    if args.svr:
        if args.svr not in services:
            sys.exit(f"ERROR: {args.svr} not in svr_list ({', '.join(services)})")
        services = [args.svr]

    _print_plan(args.env, out_dir, services, args.dry_run)

    if not args.dry_run:
        _reset_out_dir(out_dir)

    # 1. Bake common.yaml to shared run/common/conf/.
    bake_file(common_template, out_dir / "common" / "conf" / "common.yaml",
              values, args.dry_run, name="common")

    # 2. Bake each service's own config templates.
    for svc in services:
        svc_conf = conf_dir / svc
        if not svc_conf.is_dir():
            continue
        for template_path in sorted(svc_conf.glob("*.yaml")):
            dst = out_dir / svc / "conf" / template_path.name
            name = f"{svc}/{template_path.stem}"
            bake_file(template_path, dst, values, args.dry_run, name=name)

    if not args.dry_run:
        _prepare_dirs(out_dir, services)
        _write_scripts(out_dir, services, args.world_id)
        _write_env(out_dir / "ENV", args.env)

    print(f"config done env={args.env} services={','.join(services)}")


# ---- internal helpers ----


def _project_root() -> Path:
    return Path(__file__).resolve().parents[1]


def _parse_world_id(value: str) -> int:
    n = int(value, 10)
    if n < 1 or n > 0xFFFF:
        raise argparse.ArgumentTypeError("world_id must be 1..65535")
    return n


def _load_yaml(path: Path) -> dict[str, Any]:
    if not path.exists():
        sys.exit(f"ERROR: values file not found: {path}")
    with path.open("r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    if not isinstance(data, dict):
        sys.exit(f"ERROR: values file must be a yaml map: {path}")
    return data


def _services_from_values(data: dict[str, Any]) -> list[str]:
    svc = data.get("svr_list") or []
    if not isinstance(svc, list):
        sys.exit("ERROR: svr_list must be a yaml list")
    return [str(s) for s in svc]


def _print_plan(env: str, out_dir: Path, services: list[str], dry_run: bool) -> None:
    mode = "dry-run" if dry_run else "write"
    print(f"config mode={mode} env={env} out={out_dir} services={len(services)}")


def _reset_out_dir(out_dir: Path) -> None:
    if out_dir.exists():
        if not out_dir.is_dir():
            sys.exit(f"ERROR: {out_dir} is not a directory")
        shutil.rmtree(out_dir)


def _prepare_dirs(out_dir: Path, services: list[str]) -> None:
    (out_dir / "common" / "conf").mkdir(parents=True, exist_ok=True)
    for svc in services:
        (out_dir / svc / "bin").mkdir(parents=True, exist_ok=True)
        (out_dir / svc / "log").mkdir(parents=True, exist_ok=True)


def _node_id(world_id: int, service: str) -> str:
    st = SERVER_TYPES.get(service)
    if st is None:
        sys.exit(f"ERROR: unknown server type for {service}")
    return f"{world_id}.{st}.0"


def _write_scripts(out_dir: Path, services: list[str], world_id: int) -> None:
    for svc in services:
        _write_svc_script(out_dir, svc, world_id)
    _write_batch_script(out_dir / "startall.sh", services, "start", clean_logs=True)
    _write_batch_script(out_dir / "stopall.sh", reversed(services), "stop", clean_logs=False)


def _write_svc_script(out_dir: Path, svc: str, world_id: int) -> None:
    nid = _node_id(world_id, svc)
    bin_dir = out_dir / svc / "bin"

    # Config paths are baked into generated Go constants.
    # Only --nodeid and --daemon needed at runtime.
    start = (
        "#!/bin/sh\n"
        f'DIR="$(cd "$(dirname "$0")" && pwd)"\n'
        'cd "$DIR" || exit 1\n'
        f'LOG_DIR="../log"\n'
        f'STDOUT_LOG="$LOG_DIR/{svc}.stdout.log"\n'
        f'STDERR_LOG="$LOG_DIR/{svc}.stderr.log"\n'
        'mkdir -p "$LOG_DIR"\n'
        ': > "$STDOUT_LOG"\n'
        ': > "$STDERR_LOG"\n'
        f'exec ./{svc} --nodeid {nid} --daemon'
        f' 1>>"$STDOUT_LOG" 2>>"$STDERR_LOG"\n'
    )
    stop = (
        "#!/bin/sh\n"
        f'DIR="$(cd "$(dirname "$0")" && pwd)"\n'
        'cd "$DIR" || exit 1\n'
        f'if [ ! -f {svc}.pid ]; then\n'
        f'    echo "{svc}.pid not found, skip stop."\n'
        "    exit 0\n"
        "fi\n"
        f'pid="$(cat {svc}.pid)"\n'
        'if [ -z "$pid" ]; then\n'
        f'    echo "{svc}.pid is empty, skip stop."\n'
        "    exit 0\n"
        "fi\n"
        'kill -TERM "$pid"\n'
    )
    _write_exec(bin_dir / "start.sh", start)
    _write_exec(bin_dir / "stop.sh", stop)


def _write_batch_script(
    path: Path, services: Any, action: str, clean_logs: bool
) -> None:
    lines = [
        "#!/bin/sh",
        'DIR="$(cd "$(dirname "$0")" && pwd)"',
        'cd "$DIR" || exit 1',
    ]
    if clean_logs:
        for svc in services:
            lines.extend(
                [
                    f'echo "=== clean log {svc} ==="',
                    f"rm -rf ./{svc}/log",
                    f"mkdir -p ./{svc}/log",
                ]
            )
    lines.append("ret=0")
    for svc in services:
        lines.extend(
            [
                f'echo "=== {action} {svc} ==="',
                f"./{svc}/bin/{action}.sh",
                "code=$?",
                'if [ "$code" -ne 0 ]; then',
                "    ret=$code",
                "fi",
            ]
        )
    lines.append('exit "$ret"')
    _write_exec(path, "\n".join(lines) + "\n")


def _write_exec(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    try:
        path.chmod(0o755)
    except OSError:
        pass


def _write_env(path: Path, env: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"{env}\n", encoding="utf-8")


if __name__ == "__main__":
    main()
