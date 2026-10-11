#!/usr/bin/env python3
"""Load a built plugin library and exercise the native CPA ABI."""

from __future__ import annotations

import base64
import ctypes
import json
import pathlib
import sys


ABI_VERSION = 1
SCHEMA_VERSION = 6


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


HostCall = ctypes.CFUNCTYPE(
    ctypes.c_int,
    ctypes.c_void_p,
    ctypes.c_char_p,
    ctypes.POINTER(ctypes.c_uint8),
    ctypes.c_size_t,
    ctypes.POINTER(Buffer),
)
HostFree = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
PluginCall = ctypes.CFUNCTYPE(
    ctypes.c_int,
    ctypes.c_char_p,
    ctypes.POINTER(ctypes.c_uint8),
    ctypes.c_size_t,
    ctypes.POINTER(Buffer),
)
PluginFree = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
PluginShutdown = ctypes.CFUNCTYPE(None)


class HostAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("host_ctx", ctypes.c_void_p),
        ("call", HostCall),
        ("free_buffer", HostFree),
    ]


class PluginAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("call", PluginCall),
        ("free_buffer", PluginFree),
        ("shutdown", PluginShutdown),
    ]


@HostCall
def host_call(_ctx, _method, _request, _request_len, response):
    response.contents.ptr = None
    response.contents.len = 0
    return 0


@HostFree
def host_free(_ptr, _length):
    return None


def invoke(api: PluginAPI, method: str, payload: dict) -> dict:
    encoded = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    request = (ctypes.c_uint8 * len(encoded)).from_buffer_copy(encoded)
    response = Buffer()
    status = api.call(
        method.encode("utf-8"),
        request,
        len(encoded),
        ctypes.byref(response),
    )
    if status != 0:
        raise RuntimeError(f"{method} returned ABI status {status}")
    if not response.ptr or response.len == 0:
        raise RuntimeError(f"{method} returned an empty response")
    try:
        raw = ctypes.string_at(response.ptr, response.len)
    finally:
        api.free_buffer(response.ptr, response.len)
    envelope = json.loads(raw)
    if not envelope.get("ok"):
        raise RuntimeError(f"{method} failed: {envelope.get('error')}")
    return envelope


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: abi-smoke.py <plugin-library>")
    library_path = pathlib.Path(sys.argv[1]).resolve(strict=True)
    library = ctypes.CDLL(str(library_path))
    initialize = library.cliproxy_plugin_init
    initialize.argtypes = [ctypes.POINTER(HostAPI), ctypes.POINTER(PluginAPI)]
    initialize.restype = ctypes.c_int

    host = HostAPI(ABI_VERSION, None, host_call, host_free)
    plugin = PluginAPI()
    if initialize(ctypes.byref(host), ctypes.byref(plugin)) != 0:
        raise RuntimeError("cliproxy_plugin_init failed")
    if plugin.abi_version != ABI_VERSION:
        raise RuntimeError(
            f"plugin ABI version {plugin.abi_version}, want {ABI_VERSION}"
        )

    try:
        registration = invoke(
            plugin,
            "plugin.register",
            {"config_yaml": "", "schema_version": SCHEMA_VERSION},
        )["result"]
        if registration.get("schema_version") != SCHEMA_VERSION:
            raise RuntimeError(f"unexpected registration: {registration}")
        capabilities = registration.get("capabilities", {})
        if not capabilities.get("request_interceptor") or not capabilities.get(
            "request_lifecycle_plugin"
        ) or not capabilities.get("management_api"):
            raise RuntimeError(f"missing capabilities: {capabilities}")

        management = invoke(plugin, "management.register", {})["result"]
        routes = management.get("Routes", management.get("routes", []))
        if len(routes) != 1:
            raise RuntimeError(f"unexpected management routes: {management}")
        route = routes[0]
        if route.get("Method", route.get("method")) != "GET" or route.get(
            "Path", route.get("path")
        ) != "/plugins/codex-switch-safe/status":
            raise RuntimeError(f"unexpected management route: {route}")

        request_body = b'{"input":[{"type":"message","role":"user","content":"smoke"}]}'
        invoke(
            plugin,
            "request.intercept_after",
            {
                "RequestID": "abi-smoke-request",
                "ToFormat": "codex",
                "Model": "gpt-smoke",
                "Headers": {"Session-Id": ["abi-smoke-session"]},
                "Body": base64.b64encode(request_body).decode("ascii"),
                "Metadata": {
                    "caller_scope": "abi-smoke",
                    "selected_auth_id": "abi-smoke-auth",
                    "canonical_session_id": "abi-smoke-session",
                },
            },
        )
        invoke(
            plugin,
            "request.complete",
            {"RequestID": "abi-smoke-request", "Outcome": "succeeded"},
        )
        status_response = invoke(
            plugin,
            "management.handle",
            {
                "Method": "GET",
                "Path": "/v0/management/plugins/codex-switch-safe/status",
            },
        )["result"]
        if status_response.get("StatusCode") != 200:
            raise RuntimeError(f"unexpected management response: {status_response}")
        status = json.loads(base64.b64decode(status_response["Body"]))
        if status.get("plugin", {}).get("id") != "codex-switch-safe":
            raise RuntimeError(f"unexpected status identity: {status}")
        if status.get("counters", {}).get("requests_observed") != 1:
            raise RuntimeError(f"unexpected status counters: {status}")
    finally:
        plugin.shutdown()

    print(f"native ABI smoke test passed: {library_path.name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
