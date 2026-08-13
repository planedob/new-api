"""Fail-closed Image2 capability migration planner.

This module is deliberately offline.  It consumes a non-secret channel
snapshot and separately recorded acceptance evidence, then emits a dry-run
plan.  It never calls an Aibuff API and never writes a production object.
The caller must explicitly provide evidence for every newly declared
operation; model names and historical traffic are not capability proof.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import sys
from pathlib import Path
from typing import Any, Dict, Iterable, List, Optional, Sequence, Tuple


class MigrationError(ValueError):
    """Input or plan drift that must stop a migration."""


SENSITIVE_KEYS = {
    "key",
    "api_key",
    "apikey",
    "token",
    "access_token",
    "authorization",
    "password",
    "passwd",
    "secret",
    "private_key",
    "cookie",
    "session",
    "connection_string",
    "dsn",
}
ALLOWED_OPERATIONS = {"generations", "edits"}
ALLOWED_RESOLUTIONS = {"1024", "2048", "uhd"}
ALLOWED_QUALITIES = {"standard", "high"}
DEFAULT_QUALITY_VALUES = {"", "auto"}


def _canonical(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def _digest(value: Any) -> str:
    return hashlib.sha256(_canonical(value).encode("utf-8")).hexdigest()


def _scan_secrets(value: Any, path: str = "$") -> None:
    if isinstance(value, dict):
        for key, child in value.items():
            normalized = str(key).strip().lower().replace("-", "_")
            looks_sensitive = (
                normalized in SENSITIVE_KEYS
                or normalized.endswith("_key")
                or normalized.endswith("_token")
                or normalized in {"authorization_header", "cookie_header"}
            )
            if looks_sensitive and child not in (None, "", [], {}):
                raise MigrationError(f"credential-bearing field at {path}.{key}; remove it")
            _scan_secrets(child, f"{path}.{key}")
    elif isinstance(value, list):
        for index, child in enumerate(value):
            _scan_secrets(child, f"{path}[{index}]")


def _load_json(path: Path) -> Any:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise MigrationError(f"cannot read JSON {path}: {exc}") from exc
    _scan_secrets(value)
    return value


def _channels(snapshot: Any) -> List[Dict[str, Any]]:
    if isinstance(snapshot, list):
        channels = snapshot
    elif isinstance(snapshot, dict) and isinstance(snapshot.get("channels"), list):
        channels = snapshot["channels"]
    else:
        raise MigrationError("snapshot must be a JSON list or an object with channels[]")
    result: List[Dict[str, Any]] = []
    seen = set()
    for channel in channels:
        if not isinstance(channel, dict):
            raise MigrationError("every channel must be an object")
        if "id" not in channel:
            raise MigrationError("every channel must include id")
        try:
            channel_id = int(channel["id"])
        except (TypeError, ValueError) as exc:
            raise MigrationError(f"invalid channel id {channel.get('id')!r}") from exc
        if channel_id in seen:
            raise MigrationError(f"duplicate channel id {channel_id}")
        seen.add(channel_id)
        result.append(channel)
    return result


def _setting(channel: Dict[str, Any]) -> Dict[str, Any]:
    setting = channel.get("setting", {})
    if isinstance(setting, str):
        try:
            setting = json.loads(setting) if setting else {}
        except json.JSONDecodeError as exc:
            raise MigrationError(f"channel {channel.get('id')} setting is not valid JSON") from exc
    if setting is None:
        setting = {}
    if not isinstance(setting, dict):
        raise MigrationError(f"channel {channel.get('id')} setting must be an object")
    return setting


def _capability(channel: Dict[str, Any]) -> Dict[str, Any]:
    capability = _setting(channel).get("image2_capability")
    if capability is None:
        return {}
    if not isinstance(capability, dict):
        raise MigrationError(f"channel {channel.get('id')} image2_capability must be an object")
    return capability


def _normalize_values(field: str, values: Any, allowed: Optional[set] = None) -> List[str]:
    if values is None:
        return []
    if not isinstance(values, list):
        raise MigrationError(f"image2_capability.{field} must be a list")
    result: List[str] = []
    seen = set()
    for raw in values:
        value = str(raw).strip().lower()
        if not value or (allowed is not None and value not in allowed):
            raise MigrationError(f"image2_capability.{field} contains unsupported value {raw!r}")
        if value in seen:
            raise MigrationError(f"image2_capability.{field} contains duplicate value {raw!r}")
        seen.add(value)
        result.append(value)
    return result


def validate_capability(capability: Dict[str, Any]) -> None:
    if not capability or not capability.get("enabled", False):
        return
    operations = _normalize_values("operations", capability.get("operations"), ALLOWED_OPERATIONS)
    resolutions = _normalize_values("resolutions", capability.get("resolutions"), ALLOWED_RESOLUTIONS)
    raw_qualities = capability.get("qualities", [])
    if raw_qualities is None:
        raw_qualities = []
    qualities = _normalize_values("qualities", raw_qualities, ALLOWED_QUALITIES)
    if not operations:
        raise MigrationError("enabled image2_capability requires operations")
    if not resolutions:
        raise MigrationError("enabled image2_capability requires resolutions")
    if "edits" in operations and not bool(capability.get("edits_accepted", False)):
        raise MigrationError("edits operation requires edits_accepted=true")
    # auto is a request value, never an advertised capability.
    if any(str(value).strip().lower() == "auto" for value in raw_qualities):
        raise MigrationError("auto must not be declared in image2_capability.qualities")
    # Force callers to exercise the parser above, keeping this validator
    # strict even if the input happened to use a tuple-like JSON surrogate.
    if len(qualities) != len(raw_qualities):
        raise MigrationError("qualities contains an invalid value")


def _resolution_for_size(size: str) -> Optional[str]:
    normalized = str(size or "").strip().lower()
    if normalized in {"", "auto", "1024", "1024x1024"}:
        return "1024"
    if normalized in {"2048", "2048x2048"}:
        return "2048"
    if normalized in {"uhd", "4096", "4096x4096"}:
        return "uhd"
    return None


def _evidence_entries(evidence: Any) -> List[Dict[str, Any]]:
    if evidence is None:
        return []
    if isinstance(evidence, dict) and isinstance(evidence.get("evidence"), list):
        evidence = evidence["evidence"]
    if not isinstance(evidence, list):
        raise MigrationError("evidence must be a JSON list or an object with evidence[]")
    for entry in evidence:
        if not isinstance(entry, dict):
            raise MigrationError("every evidence entry must be an object")
    return evidence


def _entry_channel_id(entry: Dict[str, Any]) -> Optional[int]:
    try:
        return int(entry.get("channel_id", -1))
    except (TypeError, ValueError):
        return None


def _proof_for(entry: Dict[str, Any], channel_id: int, operation: str) -> Tuple[bool, str]:
    entry_channel_id = _entry_channel_id(entry)
    if entry_channel_id is None:
        return False, "channel_id_invalid"
    if entry_channel_id != channel_id:
        return False, "channel_id_mismatch"
    if str(entry.get("operation", "")).strip().lower() != operation:
        return False, "operation_missing"
    # Historical A-layer success (for example an old legacy fallback) is not
    # proof for the B-layer capability router.  Only a fresh, fixed-channel
    # evidence record may authorize a capability migration.
    if str(entry.get("evidence_class", "")).strip().lower() != "fresh_fixed_channel":
        return False, "fresh_fixed_channel_evidence_missing"
    required = {
        "request_id": "request_id_missing",
        "final_channel_id": "final_channel_mismatch",
    }
    for key, reason in required.items():
        if not str(entry.get(key, "")).strip():
            return False, reason
    try:
        final_channel_id = int(entry.get("final_channel_id", -1))
    except (TypeError, ValueError):
        return False, "final_channel_mismatch"
    if final_channel_id != channel_id:
        return False, "final_channel_mismatch"
    if entry.get("non_empty_image") is not True:
        return False, "non_empty_image_missing"
    if entry.get("fixed_channel_boundary") is not True:
        return False, "fixed_channel_boundary_missing"
    if entry.get("no_duplicate_request") is not True:
        return False, "duplicate_request_evidence_missing"
    if entry.get("failure_no_charge") is not True:
        return False, "failure_no_charge_evidence_missing"
    try:
        n = int(entry.get("n", 0))
    except (TypeError, ValueError):
        return False, "n_invalid"
    if n != 1:
        return False, "n_must_equal_1"
    resolution = _resolution_for_size(str(entry.get("size", "")))
    if resolution is None:
        return False, "size_unsupported"
    return True, resolution


def _quality_from_evidence(entry: Dict[str, Any]) -> Optional[str]:
    quality = str(entry.get("quality", "")).strip().lower()
    if quality in DEFAULT_QUALITY_VALUES:
        return None
    if quality not in ALLOWED_QUALITIES:
        raise MigrationError(f"evidence quality {quality!r} is not an allowed explicit value")
    return quality


def _capability_with_proof(
    channel: Dict[str, Any], proofs: Sequence[Dict[str, Any]]
) -> Tuple[Optional[Dict[str, Any]], List[str]]:
    channel_id = int(channel["id"])
    before = copy.deepcopy(_capability(channel))
    validate_capability(before)
    relevant = [entry for entry in proofs if _entry_channel_id(entry) == channel_id]
    if not relevant:
        return None, ["edits_evidence_missing"]
    good: List[Tuple[Dict[str, Any], str]] = []
    reasons: List[str] = []
    for entry in relevant:
        ok, detail = _proof_for(entry, channel_id, "edits")
        if not ok:
            reasons.append(detail)
            continue
        good.append((entry, detail))
    if not good:
        return None, sorted(set(reasons))
    target = copy.deepcopy(before)
    target["enabled"] = True
    operations = {str(value).strip().lower() for value in target.get("operations", [])}
    operations.add("edits")
    target["operations"] = sorted(operations)
    resolutions = {str(value).strip().lower() for value in target.get("resolutions", [])}
    for _, resolution in good:
        resolutions.add(resolution)
    target["resolutions"] = sorted(resolutions, key=("1024", "2048", "uhd").index)
    qualities = {str(value).strip().lower() for value in target.get("qualities", [])}
    for entry, _ in good:
        explicit_quality = _quality_from_evidence(entry)
        if explicit_quality:
            qualities.add(explicit_quality)
    target["qualities"] = sorted(qualities)
    target["edits_accepted"] = True
    validate_capability(target)
    return target, []


def build_plan(snapshot: Any, evidence: Any, channel_ids: Optional[Iterable[int]] = None) -> Dict[str, Any]:
    _scan_secrets(snapshot)
    _scan_secrets(evidence)
    channels = _channels(snapshot)
    selected = None if channel_ids is None else {int(value) for value in channel_ids}
    evidence_entries = _evidence_entries(evidence)
    plan: Dict[str, Any] = {
        "version": 1,
        "mode": "dry-run",
        "snapshot_sha256": _digest(snapshot),
        "changes": [],
        "needs_info": [],
        "rollback": [],
    }
    for channel in sorted(channels, key=lambda item: int(item["id"])):
        channel_id = int(channel["id"])
        if selected is not None and channel_id not in selected:
            continue
        target, reasons = _capability_with_proof(channel, evidence_entries)
        if target is None:
            plan["needs_info"].append({"channel_id": channel_id, "reasons": reasons})
            continue
        before = _capability(channel)
        if _canonical(before) == _canonical(target):
            continue
        plan["changes"].append({"channel_id": channel_id, "before": before, "after": target})
        plan["rollback"].append({"channel_id": channel_id, "restore": before, "expected_after_sha256": _digest(target)})
    plan["plan_sha256"] = _digest({key: value for key, value in plan.items() if key != "plan_sha256"})
    return plan


def validate_plan(snapshot: Any, plan: Dict[str, Any]) -> None:
    _scan_secrets(snapshot)
    _scan_secrets(plan)
    if not isinstance(plan, dict) or plan.get("version") != 1:
        raise MigrationError("unsupported migration plan")
    if plan.get("snapshot_sha256") != _digest(snapshot):
        raise MigrationError("snapshot digest drifted since plan generation")
    channel_map = {int(channel["id"]): channel for channel in _channels(snapshot)}
    for change in plan.get("changes", []):
        channel_id = int(change.get("channel_id", -1))
        if channel_id not in channel_map:
            raise MigrationError(f"planned channel {channel_id} is missing from snapshot")
        current = _capability(channel_map[channel_id])
        if _canonical(current) != _canonical(change.get("before", {})):
            raise MigrationError(f"channel {channel_id} changed after plan generation")
        after = change.get("after")
        if not isinstance(after, dict):
            raise MigrationError(f"channel {channel_id} has invalid target")
        validate_capability(after)
        if not after.get("edits_accepted") or "edits" not in after.get("operations", []):
            raise MigrationError(f"channel {channel_id} target does not explicitly accept edits")
    expected = plan.get("plan_sha256")
    actual = _digest({key: value for key, value in plan.items() if key != "plan_sha256"})
    if expected and expected != actual:
        raise MigrationError("plan digest mismatch")


def rollback_payload(plan: Dict[str, Any]) -> Dict[str, Any]:
    if not isinstance(plan, dict) or plan.get("version") != 1:
        raise MigrationError("unsupported migration plan")
    rollback = {
        "version": 1,
        "mode": "rollback-dry-run",
        "source_plan_sha256": plan.get("plan_sha256"),
        "restore": sorted(plan.get("rollback", []), key=lambda item: int(item["channel_id"])),
    }
    rollback["rollback_sha256"] = _digest({key: value for key, value in rollback.items() if key != "rollback_sha256"})
    return rollback


def _write_json(value: Any, output: Optional[Path]) -> None:
    rendered = json.dumps(value, sort_keys=True, indent=2, ensure_ascii=False) + "\n"
    if output is None:
        sys.stdout.write(rendered)
    else:
        output.write_text(rendered, encoding="utf-8")


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    plan_parser = subparsers.add_parser("plan", help="build a fail-closed dry-run plan")
    plan_parser.add_argument("--snapshot", type=Path, required=True)
    plan_parser.add_argument("--evidence", type=Path, required=True)
    plan_parser.add_argument("--channel", type=int, action="append", dest="channels")
    plan_parser.add_argument("--output", type=Path)

    validate_parser = subparsers.add_parser("validate", help="validate a plan against the same snapshot")
    validate_parser.add_argument("--snapshot", type=Path, required=True)
    validate_parser.add_argument("--plan", type=Path, required=True)

    rollback_parser = subparsers.add_parser("rollback", help="emit a rollback dry-run payload")
    rollback_parser.add_argument("--plan", type=Path, required=True)
    rollback_parser.add_argument("--output", type=Path)

    args = parser.parse_args(argv)
    try:
        if args.command == "plan":
            plan = build_plan(_load_json(args.snapshot), _load_json(args.evidence), args.channels)
            _write_json(plan, args.output)
            return 0
        if args.command == "validate":
            snapshot = _load_json(args.snapshot)
            plan = _load_json(args.plan)
            validate_plan(snapshot, plan)
            return 0
        if args.command == "rollback":
            _write_json(rollback_payload(_load_json(args.plan)), args.output)
            return 0
    except MigrationError as exc:
        print(f"BLOCK: {exc}", file=sys.stderr)
        return 2
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
