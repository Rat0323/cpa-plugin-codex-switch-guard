#!/usr/bin/env python3
"""Start a CPA release with the plugin and verify its management route."""

from __future__ import annotations

import argparse
import json
import pathlib
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


MANAGEMENT_SECRET = "codex-switch-safe-host-smoke"


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def yaml_string(value: pathlib.Path | str) -> str:
    return json.dumps(str(value).replace("\\", "/"))


def write_config(path: pathlib.Path, auth_dir: pathlib.Path, plugin_dir: pathlib.Path, port: int) -> None:
    path.write_text(
        "\n".join(
            [
                'host: "127.0.0.1"',
                f"port: {port}",
                "remote-management:",
                "  allow-remote: false",
                f"  secret-key: {json.dumps(MANAGEMENT_SECRET)}",
                "  disable-control-panel: true",
                f"auth-dir: {yaml_string(auth_dir)}",
                "api-keys:",
                '  - "host-smoke-api-key"',
                "debug: true",
                "logging-to-file: false",
                "usage-statistics-enabled: false",
                "plugins:",
                "  enabled: true",
                f"  dir: {yaml_string(plugin_dir)}",
                "  configs:",
                "    codex-switch-safe:",
                "      enabled: true",
                "      diagnostics: actions",
                "",
            ]
        ),
        encoding="utf-8",
    )


def read_output(stream, lines: list[str]) -> None:
    for line in iter(stream.readline, ""):
        lines.append(line.rstrip())


def fetch_status(port: int) -> dict:
    request = urllib.request.Request(
        f"http://127.0.0.1:{port}/v0/management/plugins/codex-switch-safe/status",
        headers={"Authorization": f"Bearer {MANAGEMENT_SECRET}"},
    )
    with urllib.request.urlopen(request, timeout=1) as response:
        if response.status != 200:
            raise RuntimeError(f"management status returned HTTP {response.status}")
        return json.load(response)


def verify_private_status_route(port: int) -> None:
    for path, headers, expected in [
        ("/v0/management/plugins/codex-switch-safe/status", {}, {401, 403}),
        (
            "/v0/management/plugins/codex-switch-safe/status",
            {"Authorization": "Bearer invalid-host-smoke-secret"},
            {401, 403},
        ),
        ("/v0/resource/plugins/codex-switch-safe/status", {}, {404}),
    ]:
        request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", headers=headers)
        try:
            with urllib.request.urlopen(request, timeout=1) as response:
                code = response.status
        except urllib.error.HTTPError as error:
            code = error.code
            error.close()
        if code not in expected:
            raise RuntimeError(f"private status route check failed: {path} returned HTTP {code}")


def smoke(host: pathlib.Path, plugin: pathlib.Path, timeout: float) -> dict:
    with tempfile.TemporaryDirectory(prefix="codex-switch-safe-host-smoke-") as raw_dir:
        work_dir = pathlib.Path(raw_dir)
        auth_dir = work_dir / "auth"
        plugin_dir = work_dir / "plugins"
        auth_dir.mkdir()
        plugin_dir.mkdir()
        installed_plugin = plugin_dir / f"codex-switch-safe{plugin.suffix}"
        shutil.copy2(plugin, installed_plugin)
        config = work_dir / "config.yaml"
        port = free_port()
        write_config(config, auth_dir, plugin_dir, port)

        process = subprocess.Popen(
            [str(host), "-config", str(config)],
            cwd=work_dir,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        output: list[str] = []
        assert process.stdout is not None
        reader = threading.Thread(target=read_output, args=(process.stdout, output), daemon=True)
        reader.start()
        deadline = time.monotonic() + timeout
        last_error: Exception | None = None
        try:
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise RuntimeError(
                        f"CPA exited with status {process.returncode}\n" + "\n".join(output[-80:])
                    )
                try:
                    status = fetch_status(port)
                except (OSError, RuntimeError, urllib.error.URLError, json.JSONDecodeError) as error:
                    last_error = error
                    time.sleep(0.2)
                    continue
                if status.get("plugin", {}).get("id") != "codex-switch-safe":
                    raise RuntimeError(f"unexpected plugin status: {status}")
                verify_private_status_route(port)
                return status
            raise RuntimeError(
                f"CPA management route was not ready: {last_error}\n" + "\n".join(output[-80:])
            )
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
            reader.join(timeout=1)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", required=True, type=pathlib.Path)
    parser.add_argument("--plugin", required=True, type=pathlib.Path)
    parser.add_argument("--timeout", type=float, default=20)
    args = parser.parse_args()
    host = args.host.resolve(strict=True)
    plugin = args.plugin.resolve(strict=True)
    status = smoke(host, plugin, args.timeout)
    print(
        "CPA host smoke passed: "
        f"plugin={status['plugin']['version']} schema={status['schema_version']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
