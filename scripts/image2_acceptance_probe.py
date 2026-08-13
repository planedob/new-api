#!/usr/bin/env python3
"""Strict, credential-safe Image2 acceptance probe.

The probe deliberately uses the public API-token routes, never the dashboard
``/pg`` routes.  It reads a token from ``--token`` or ``AIBUFF_API_TOKEN`` but
never prints it.  The response body is parsed in memory and is never emitted;
the only CLI output is a JSON summary containing a redacted request ID, HTTP
status, elapsed time, data count, image hashes, and PASS/BLOCK verdict.

Use ``parse_image2_response`` directly for offline tests and release gates.
The parser is fail-closed: an HTTP 200 envelope with ``success: false`` or a
top-level auth/error message is a BLOCK, not an empty successful image list.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import mimetypes
import os
import sys
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Mapping, Sequence


IMAGE_ENDPOINTS: Mapping[str, str] = {
    "generations": "/v1/images/generations",
    "edits": "/v1/images/edits",
}
DEFAULT_TIMEOUT_SECONDS = 60.0
MAX_RESPONSE_BYTES = 16 * 1024 * 1024


def endpoint_for(operation: str) -> str:
    """Return the only API-token endpoint allowed for an Image2 operation."""

    try:
        return IMAGE_ENDPOINTS[operation]
    except KeyError as exc:
        raise ValueError(f"unsupported Image2 operation: {operation!r}") from exc


def redact_request_id(request_id: str | None) -> str | None:
    """Keep only a short prefix/suffix of a request ID for correlation."""

    if not request_id:
        return None
    value = str(request_id).strip()
    if not value:
        return None
    if len(value) <= 8:
        return "***"
    return f"{value[:4]}…{value[-4:]}"


def _nonempty_text(value: Any) -> str:
    return value.strip() if isinstance(value, str) else ""


def _image_ref(item: Any) -> str:
    """Return the opaque value to hash for one image item, or an empty string."""

    if not isinstance(item, Mapping):
        return ""
    # Prefer URL when both forms are supplied.  We hash only the value and do
    # not include it in the result, so query strings cannot leak into output.
    url = _nonempty_text(item.get("url"))
    if url:
        return url
    return _nonempty_text(item.get("b64_json"))


@dataclass(frozen=True)
class Image2ParseResult:
    verdict: str
    reason_code: str
    status: int | None
    elapsed_ms: int | None
    data_count: int
    hashes: tuple[str, ...] = field(default_factory=tuple)
    request_id: str | None = None

    @property
    def passed(self) -> bool:
        return self.verdict == "PASS"

    def summary(self) -> dict[str, Any]:
        """Return the only data shape permitted for CLI/log output."""

        return {
            "verdict": self.verdict,
            "request_id": redact_request_id(self.request_id),
            "status": self.status,
            "elapsed_ms": self.elapsed_ms,
            "data_count": self.data_count,
            "hash": list(self.hashes),
        }


def _blocked(
    reason_code: str,
    status: int | None,
    elapsed_ms: int | None,
    request_id: str | None,
    data_count: int = 0,
) -> Image2ParseResult:
    return Image2ParseResult(
        verdict="BLOCK",
        reason_code=reason_code,
        status=status,
        elapsed_ms=elapsed_ms,
        data_count=data_count,
        request_id=request_id,
    )


def parse_image2_response(
    status: int | None,
    body: bytes | str,
    *,
    request_id: str | None = None,
    elapsed_ms: int | None = None,
) -> Image2ParseResult:
    """Strictly classify one Image2 API response without exposing its body.

    PASS requires a 2xx response and a non-empty ``data`` list where every
    item carries a non-empty ``url`` or ``b64_json``.  A top-level ``message``
    or ``error`` envelope is treated as a failure, including the legacy
    HTTP-200 ``{"success": false, "message": ...}`` auth response.
    """

    if status is None or not 200 <= status < 300:
        return _blocked("http_non_2xx", status, elapsed_ms, request_id)

    try:
        payload = json.loads(body)
    except (TypeError, ValueError, json.JSONDecodeError):
        return _blocked("invalid_json", status, elapsed_ms, request_id)

    if not isinstance(payload, Mapping):
        return _blocked("response_not_object", status, elapsed_ms, request_id)

    if payload.get("success") is False:
        return _blocked("success_false", status, elapsed_ms, request_id)

    if "message" in payload:
        return _blocked("top_level_message", status, elapsed_ms, request_id)

    if "error" in payload:
        return _blocked("top_level_error", status, elapsed_ms, request_id)

    data = payload.get("data")
    if not isinstance(data, list) or not data:
        return _blocked("data_missing_or_empty", status, elapsed_ms, request_id)

    hashes: list[str] = []
    for item in data:
        image_ref = _image_ref(item)
        if not image_ref:
            return _blocked("image_item_empty", status, elapsed_ms, request_id, len(data))
        hashes.append(hashlib.sha256(image_ref.encode("utf-8")).hexdigest())

    return Image2ParseResult(
        verdict="PASS",
        reason_code="ok",
        status=status,
        elapsed_ms=elapsed_ms,
        data_count=len(data),
        hashes=tuple(hashes),
        request_id=request_id,
    )


def _request_id_from_headers(headers: Mapping[str, str]) -> str | None:
    for name in ("X-Request-Id", "X-Request-ID", "X-Oneapi-Request-Id"):
        value = headers.get(name)
        if value:
            return value
    return None


def _read_response_body(response: Any, limit: int = MAX_RESPONSE_BYTES) -> bytes:
    body = response.read(limit + 1)
    if len(body) > limit:
        return body[:limit] + b"\n"
    return body


def _multipart_body(
    fields: Mapping[str, str], file_field: str, filename: str, content_type: str, content: bytes
) -> tuple[bytes, str]:
    boundary = f"----aibuff-image2-{uuid.uuid4().hex}"
    chunks: list[bytes] = []
    for name, value in fields.items():
        chunks.extend(
            [
                f"--{boundary}\r\n".encode(),
                f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode(),
                value.encode("utf-8"),
                b"\r\n",
            ]
        )
    chunks.extend(
        [
            f"--{boundary}\r\n".encode(),
            (
                f'Content-Disposition: form-data; name="{file_field}"; '
                f'filename="{filename}"\r\n'
            ).encode(),
            f"Content-Type: {content_type}\r\n\r\n".encode(),
            content,
            b"\r\n",
            f"--{boundary}--\r\n".encode(),
        ]
    )
    return b"".join(chunks), f"multipart/form-data; boundary={boundary}"


def _build_request_body(args: argparse.Namespace) -> tuple[bytes, str]:
    if args.body_file:
        return Path(args.body_file).read_bytes(), args.content_type

    fields: dict[str, str] = {"model": args.model, "prompt": args.prompt}
    if args.n is not None:
        fields["n"] = str(args.n)
    if args.size:
        fields["size"] = args.size
    if args.quality:
        fields["quality"] = args.quality

    if args.operation == "generations":
        return json.dumps(fields, separators=(",", ":")).encode("utf-8"), "application/json"

    if not args.image_file:
        raise ValueError("edits requires --image-file or --body-file")
    image_path = Path(args.image_file)
    image_content = image_path.read_bytes()
    image_type = mimetypes.guess_type(image_path.name)[0] or "application/octet-stream"
    return _multipart_body(fields, "image", image_path.name, image_type, image_content)


def run_probe(args: argparse.Namespace) -> Image2ParseResult:
    token = args.token or os.environ.get("AIBUFF_API_TOKEN", "")
    if not token:
        raise ValueError("API token is required via --token or AIBUFF_API_TOKEN")

    endpoint = endpoint_for(args.operation)
    url = args.base_url.rstrip("/") + endpoint
    request_body, content_type = _build_request_body(args)
    request = urllib.request.Request(
        url,
        data=request_body,
        method="POST",
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": content_type,
            "Accept": "application/json",
        },
    )

    started = time.monotonic()
    response_body = b""
    response_status: int | None = None
    request_id: str | None = None
    try:
        with urllib.request.urlopen(request, timeout=args.timeout) as response:
            response_status = response.status
            request_id = _request_id_from_headers(response.headers)
            response_body = _read_response_body(response)
    except urllib.error.HTTPError as exc:
        response_status = exc.code
        request_id = _request_id_from_headers(exc.headers)
        response_body = _read_response_body(exc)
    except (urllib.error.URLError, TimeoutError, OSError):
        elapsed_ms = int(round((time.monotonic() - started) * 1000))
        return _blocked("transport_error", None, elapsed_ms, request_id)

    elapsed_ms = int(round((time.monotonic() - started) * 1000))
    return parse_image2_response(
        response_status,
        response_body,
        request_id=request_id,
        elapsed_ms=elapsed_ms,
    )


def _build_arg_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, help="Aibuff base URL; route suffix is fixed by --operation")
    parser.add_argument("--operation", choices=sorted(IMAGE_ENDPOINTS), required=True)
    parser.add_argument("--model", default="gpt-image-2")
    parser.add_argument("--prompt", default="image2 acceptance probe")
    parser.add_argument("--n", type=int, default=1)
    parser.add_argument("--size", default="")
    parser.add_argument("--quality", default="")
    parser.add_argument("--image-file", help="Reference image for edits; never printed")
    parser.add_argument("--body-file", help="Optional raw request body; never printed")
    parser.add_argument("--content-type", default="application/json")
    parser.add_argument("--token", help="API token; prefer AIBUFF_API_TOKEN to avoid shell history")
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT_SECONDS)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_arg_parser()
    args = parser.parse_args(argv)
    try:
        result = run_probe(args)
    except (OSError, ValueError) as exc:
        # Do not print exception text: it can contain paths, URLs, or caller
        # supplied material. Emit only the same redacted summary shape.
        result = _blocked("probe_configuration_error", None, None, None)
    print(json.dumps(result.summary(), ensure_ascii=False, separators=(",", ":")))
    return 0 if result.passed else 2


if __name__ == "__main__":
    sys.exit(main())
