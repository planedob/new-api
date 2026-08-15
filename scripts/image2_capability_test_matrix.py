"""Build an Image2 routing capability exclusively from controlled tests.

Supplier declarations are intentionally not accepted by this module.  Every
routable profile is one exact operation/size/quality/n combination that passed
a fixed-channel test.  Failed profiles stay in the audit report and are never
promoted into routing metadata.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, Iterable, List, Tuple

try:
    from scripts.image2_capability_migration import MigrationError, _canonical, _scan_secrets
except ImportError:  # pragma: no cover
    from image2_capability_migration import MigrationError, _canonical, _scan_secrets  # type: ignore


TEST_KEYS = {
    "channel_id",
    "operation",
    "size",
    "quality",
    "n",
    "status",
    "request_id",
    "final_channel_id",
    "non_empty_image",
    "fixed_channel_boundary",
    "no_duplicate_request",
    "failure_no_charge",
    "failure_reason",
}


def _rfc3339(value: str, field: str) -> datetime:
    if not isinstance(value, str):
        raise MigrationError(f"{field} must be RFC3339")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise MigrationError(f"{field} must be RFC3339") from exc
    if parsed.tzinfo is None:
        raise MigrationError(f"{field} must include timezone")
    return parsed.astimezone(timezone.utc)


def _resolution(size: Any) -> str:
    value = str(size or "").strip().lower()
    mapping = {
        "auto": "1024",
        "1024": "1024",
        "1024x1024": "1024",
        "2048": "2048",
        "2048x2048": "2048",
        "uhd": "uhd",
        "4096": "uhd",
        "4096x4096": "uhd",
    }
    if value not in mapping:
        raise MigrationError(f"unsupported Image2 test size {size!r}")
    return mapping[value]


def _canonical_size(size: Any) -> str:
    value = str(size or "").strip().lower()
    mapping = {"": "auto", "auto": "auto", "1024": "1024x1024", "2048": "2048x2048", "uhd": "4096x4096", "4096": "4096x4096"}
    return mapping.get(value, value)


def _quality(value: Any) -> str:
    quality = str(value or "").strip().lower()
    if quality in {"", "auto"}:
        return "default"
    if quality not in {"standard", "high"}:
        raise MigrationError(f"unsupported Image2 test quality {value!r}")
    return quality


def _profile(entry: Dict[str, Any]) -> Dict[str, Any]:
    operation = str(entry["operation"]).strip().lower()
    if operation not in {"generations", "edits"}:
        raise MigrationError(f"unsupported Image2 operation {operation!r}")
    n = entry["n"]
    if isinstance(n, bool) or not isinstance(n, int) or n < 1:
        raise MigrationError("Image2 test n must be a positive integer")
    return {
        "operation": operation,
        "resolution": _resolution(entry["size"]),
        "size": _canonical_size(entry["size"]),
        "quality": _quality(entry["quality"]),
        "max_n": n,
    }


def _profile_key(profile: Dict[str, Any]) -> Tuple[str, str, str, str, int]:
    return (
        profile["operation"],
        profile["resolution"],
        profile["size"],
        profile["quality"],
        profile["max_n"],
    )


def _go_omitempty_capability(capability: Dict[str, Any]) -> Dict[str, Any]:
    """Mirror the Go DTO's JSON `omitempty` shape before hashing."""
    return {
        key: value
        for key, value in capability.items()
        if value not in (False, 0, "", None, [], {})
    }


def build_verified_capability(
    channel_id: int,
    tests: Iterable[Dict[str, Any]],
    *,
    route_priority: int,
    verified_at: str,
    valid_until: str,
) -> Dict[str, Any]:
    _scan_secrets(tests)
    if isinstance(channel_id, bool) or not isinstance(channel_id, int) or channel_id < 1:
        raise MigrationError("channel_id must be positive")
    if isinstance(route_priority, bool) or not isinstance(route_priority, int):
        raise MigrationError("route_priority must be an integer")
    verified_time = _rfc3339(verified_at, "verified_at")
    valid_time = _rfc3339(valid_until, "valid_until")
    if valid_time <= verified_time:
        raise MigrationError("valid_until must be after verified_at")

    entries = list(tests)
    if not entries:
        raise MigrationError("at least one capability test is required")
    profiles: List[Dict[str, Any]] = []
    rejected: List[Dict[str, Any]] = []
    evidence_hashes: List[str] = []
    seen: Dict[Tuple[str, str, str, int], str] = {}
    for index, entry in enumerate(entries):
        if not isinstance(entry, dict) or set(entry) != TEST_KEYS:
            raise MigrationError(f"tests[{index}] schema mismatch")
        if entry["channel_id"] != channel_id or entry["final_channel_id"] != channel_id:
            raise MigrationError(f"tests[{index}] is not fixed to channel {channel_id}")
        if not isinstance(entry["request_id"], str) or not entry["request_id"].strip():
            raise MigrationError(f"tests[{index}].request_id is required")
        if entry["fixed_channel_boundary"] is not True or entry["no_duplicate_request"] is not True:
            raise MigrationError(f"tests[{index}] lacks fixed/single-attempt evidence")
        if entry["failure_no_charge"] is not True:
            raise MigrationError(f"tests[{index}] lacks failure-no-charge evidence")
        profile = _profile(entry)
        status = str(entry["status"]).strip().lower()
        if status not in {"passed", "failed"}:
            raise MigrationError(f"tests[{index}].status must be passed or failed")
        key = _profile_key(profile)
        previous = seen.get(key)
        if previous is not None:
            if previous != status:
                raise MigrationError(f"conflicting test results for profile {key}; investigate before routing")
            raise MigrationError(f"duplicate test result for profile {key}")
        seen[key] = status
        evidence_hashes.append(hashlib.sha256(_canonical(entry).encode("utf-8")).hexdigest())
        if status == "passed":
            if entry["non_empty_image"] is not True:
                raise MigrationError(f"tests[{index}] passed without a non-empty image")
            profiles.append(profile)
        else:
            reason = str(entry["failure_reason"]).strip()
            if not reason:
                raise MigrationError(f"tests[{index}].failure_reason is required for failed tests")
            rejected.append({**profile, "reason": reason})

    if not profiles:
        raise MigrationError("no Image2 capability profile passed")
    profiles.sort(key=_profile_key)
    operations = sorted({item["operation"] for item in profiles})
    order = {"1024": 0, "2048": 1, "uhd": 2}
    resolutions = sorted({item["resolution"] for item in profiles}, key=order.get)
    qualities = sorted({item["quality"] for item in profiles if item["quality"] != "default"})
    capability = {
        "enabled": True,
        "operations": operations,
        "resolutions": resolutions,
        "qualities": qualities,
        "max_n": max(item["max_n"] for item in profiles),
        "route_priority": route_priority,
        "edits_accepted": "edits" in operations,
        "profiles": profiles,
    }
    return {
        "image2_capability": capability,
        "image2_capability_verification": {
            "status": "passed",
            "source": "fixed_channel_test",
            "verified_at": verified_time.isoformat().replace("+00:00", "Z"),
            "valid_until": valid_time.isoformat().replace("+00:00", "Z"),
            "capability_sha256": hashlib.sha256(
                _canonical(_go_omitempty_capability(capability)).encode("utf-8")
            ).hexdigest(),
            "evidence_sha256": sorted(evidence_hashes),
        },
        "rejected_profiles": rejected,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    raw = json.loads(args.input.read_text(encoding="utf-8"))
    _scan_secrets(raw)
    expected = {"channel_id", "route_priority", "verified_at", "valid_until", "tests"}
    if not isinstance(raw, dict) or set(raw) != expected:
        raise MigrationError("input schema mismatch")
    result = build_verified_capability(
        raw["channel_id"], raw["tests"], route_priority=raw["route_priority"],
        verified_at=raw["verified_at"], valid_until=raw["valid_until"],
    )
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
