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
import re
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
    "credential",
    "credentials",
    "auth",
    "headers",
    "http_headers",
    "connection_string",
    "dsn",
}
ALLOWED_OPERATIONS = {"generations", "edits"}
ALLOWED_RESOLUTIONS = {"1024", "2048", "uhd"}
ALLOWED_QUALITIES = {"standard", "high"}
DEFAULT_QUALITY_VALUES = {"", "auto"}

CAPABILITY_KEYS = {
    "enabled",
    "operations",
    "resolutions",
    "qualities",
    "max_n",
    "route_priority",
    "edits_accepted",
}
EVIDENCE_KEYS = {
    "channel_id",
    "operation",
    "evidence_class",
    "request_id",
    "final_channel_id",
    "non_empty_image",
    "fixed_channel_boundary",
    "no_duplicate_request",
    "failure_no_charge",
    "size",
    "quality",
    "n",
}
PLAN_KEYS = {
    "version",
    "mode",
    "target_channel_ids",
    "snapshot_channel_ids",
    "snapshot_sha256",
    "changes",
    "needs_info",
    "rollback",
    "plan_sha256",
}
CHANGE_KEYS = {
    "channel_id",
    "before",
    "before_sha256",
    "after",
    "after_sha256",
}
NEEDS_INFO_KEYS = {"channel_id", "reasons"}
ROLLBACK_KEYS = {"channel_id", "restore", "restore_sha256", "expected_after_sha256"}
HEX_DIGEST = re.compile(r"^[0-9a-f]{64}$")


def _canonical(value: Any) -> str:
    try:
        return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    except (TypeError, ValueError) as exc:
        raise MigrationError(f"value is not canonical JSON: {exc}") from exc


def _digest(value: Any) -> str:
    return hashlib.sha256(_canonical(value).encode("utf-8")).hexdigest()


def _strict_int(value: Any, field: str) -> int:
    if isinstance(value, bool):
        raise MigrationError(f"{field} must be an integer")
    if isinstance(value, int):
        return value
    if isinstance(value, str) and value.strip():
        try:
            return int(value.strip(), 10)
        except ValueError:
            pass
    raise MigrationError(f"{field} must be an integer")


def _strict_channel_id(value: Any, field: str) -> int:
    channel_id = _strict_int(value, field)
    if channel_id <= 0:
        raise MigrationError(f"{field} must be a positive channel id")
    return channel_id


def _strict_digest(value: Any, field: str) -> str:
    if not isinstance(value, str) or not HEX_DIGEST.fullmatch(value):
        raise MigrationError(f"{field} must be a lowercase SHA-256 digest")
    return value


def _exact_keys(value: Any, expected: set, field: str) -> None:
    if not isinstance(value, dict):
        raise MigrationError(f"{field} must be an object")
    actual = set(value)
    missing = expected - actual
    unknown = actual - expected
    if missing or unknown:
        details = []
        if missing:
            details.append(f"missing={sorted(missing, key=str)}")
        if unknown:
            details.append(f"unknown={sorted(unknown, key=str)}")
        raise MigrationError(f"{field} schema mismatch ({', '.join(details)})")


def _strict_id_list(value: Any, field: str, *, nonempty: bool = True) -> List[int]:
    if not isinstance(value, list):
        raise MigrationError(f"{field} must be a list")
    if nonempty and not value:
        raise MigrationError(f"{field} must not be empty")
    result: List[int] = []
    seen = set()
    for index, raw in enumerate(value):
        channel_id = _strict_channel_id(raw, f"{field}[{index}]")
        if channel_id in seen:
            raise MigrationError(f"{field} contains duplicate channel {channel_id}")
        seen.add(channel_id)
        result.append(channel_id)
    if result != sorted(result):
        raise MigrationError(f"{field} must be sorted")
    return result


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
            if looks_sensitive:
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
        channel_id = _strict_channel_id(channel["id"], "channel.id")
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
    _scan_secrets(setting, f"channel[{channel.get('id')}].setting")
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
        if not isinstance(raw, str):
            raise MigrationError(f"image2_capability.{field} values must be strings")
        value = raw.strip().lower()
        if not value or (allowed is not None and value not in allowed):
            raise MigrationError(f"image2_capability.{field} contains unsupported value {raw!r}")
        if value in seen:
            raise MigrationError(f"image2_capability.{field} contains duplicate value {raw!r}")
        seen.add(value)
        result.append(value)
    return result


def _validate_capability_shape(capability: Any, field: str = "image2_capability") -> Dict[str, Any]:
    if not isinstance(capability, dict):
        raise MigrationError(f"{field} must be an object")
    unknown = set(capability) - CAPABILITY_KEYS
    if unknown:
        raise MigrationError(f"{field} has unknown fields {sorted(unknown, key=str)}")
    if "enabled" in capability and not isinstance(capability["enabled"], bool):
        raise MigrationError(f"{field}.enabled must be a boolean")
    for key in ("operations", "resolutions", "qualities"):
        if key in capability and not isinstance(capability[key], list):
            raise MigrationError(f"{field}.{key} must be a list")
    for key in ("max_n", "route_priority"):
        if key in capability:
            number = _strict_int(capability[key], f"{field}.{key}")
            if key == "max_n" and number < 0:
                raise MigrationError(f"{field}.max_n must not be negative")
    if "edits_accepted" in capability and not isinstance(capability["edits_accepted"], bool):
        raise MigrationError(f"{field}.edits_accepted must be a boolean")
    return capability


def validate_capability(capability: Dict[str, Any]) -> None:
    _validate_capability_shape(capability)
    if not capability or not capability.get("enabled", False):
        return
    operations = _normalize_values("operations", capability.get("operations"), ALLOWED_OPERATIONS)
    resolutions = _normalize_values("resolutions", capability.get("resolutions"), ALLOWED_RESOLUTIONS)
    raw_qualities = capability.get("qualities", [])
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
    if isinstance(evidence, dict):
        _exact_keys(evidence, {"evidence"}, "evidence document")
        if not isinstance(evidence.get("evidence"), list):
            raise MigrationError("evidence document.evidence must be a list")
        evidence = evidence["evidence"]
    if not isinstance(evidence, list):
        raise MigrationError("evidence must be a JSON list or an object with evidence[]")
    for entry in evidence:
        if not isinstance(entry, dict):
            raise MigrationError("every evidence entry must be an object")
    return evidence


def _validate_evidence_entries(evidence: Sequence[Dict[str, Any]], target_ids: Sequence[int]) -> None:
    target_set = set(target_ids)
    seen = set()
    for index, entry in enumerate(evidence):
        _exact_keys(entry, EVIDENCE_KEYS, f"evidence[{index}]")
        channel_id = _strict_channel_id(entry["channel_id"], f"evidence[{index}].channel_id")
        if channel_id not in target_set:
            raise MigrationError(f"evidence[{index}] channel {channel_id} is not a target channel")
        if channel_id in seen:
            raise MigrationError(f"evidence contains duplicate channel {channel_id}")
        seen.add(channel_id)
        if not isinstance(entry["operation"], str) or not entry["operation"].strip():
            raise MigrationError(f"evidence[{index}].operation must be a non-empty string")
        if entry["operation"].strip().lower() not in ALLOWED_OPERATIONS:
            raise MigrationError(f"evidence[{index}].operation is unsupported")
        for field in ("evidence_class", "request_id", "size", "quality"):
            if not isinstance(entry[field], str):
                raise MigrationError(f"evidence[{index}].{field} must be a string")
        if not entry["request_id"].strip():
            raise MigrationError(f"evidence[{index}].request_id must not be empty")
        _strict_channel_id(entry["final_channel_id"], f"evidence[{index}].final_channel_id")
        for field in ("non_empty_image", "fixed_channel_boundary", "no_duplicate_request", "failure_no_charge"):
            if not isinstance(entry[field], bool):
                raise MigrationError(f"evidence[{index}].{field} must be a boolean")
        n = _strict_int(entry["n"], f"evidence[{index}].n")
        if n < 1:
            raise MigrationError(f"evidence[{index}].n must be positive")


def _entry_channel_id(entry: Dict[str, Any]) -> Optional[int]:
    try:
        return _strict_channel_id(entry.get("channel_id", -1), "evidence.channel_id")
    except MigrationError:
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
        final_channel_id = _strict_channel_id(entry.get("final_channel_id", -1), "evidence.final_channel_id")
    except MigrationError:
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
        n = _strict_int(entry.get("n", 0), "evidence.n")
    except MigrationError:
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
    snapshot_ids = sorted(int(channel["id"]) for channel in channels)
    available_ids = set(snapshot_ids)
    if channel_ids is None:
        target_ids = snapshot_ids
    else:
        try:
            requested_ids = list(channel_ids)
        except TypeError as exc:
            raise MigrationError("channel_ids must be an iterable of channel ids") from exc
        if not requested_ids:
            raise MigrationError("channel_ids must not be empty")
        target_ids = []
        seen = set()
        for index, raw in enumerate(requested_ids):
            channel_id = _strict_channel_id(raw, f"channel_ids[{index}]")
            if channel_id in seen:
                raise MigrationError(f"channel_ids contains duplicate channel {channel_id}")
            if channel_id not in available_ids:
                raise MigrationError(f"channel_ids contains unknown channel {channel_id}")
            seen.add(channel_id)
            target_ids.append(channel_id)
        target_ids.sort()
    evidence_entries = _evidence_entries(evidence)
    _validate_evidence_entries(evidence_entries, target_ids)
    plan: Dict[str, Any] = {
        "version": 1,
        "mode": "dry-run",
        "target_channel_ids": target_ids,
        "snapshot_channel_ids": snapshot_ids,
        "snapshot_sha256": _digest(snapshot),
        "changes": [],
        "needs_info": [],
        "rollback": [],
    }
    for channel in sorted(channels, key=lambda item: int(item["id"])):
        channel_id = int(channel["id"])
        if channel_id not in set(target_ids):
            continue
        target, reasons = _capability_with_proof(channel, evidence_entries)
        if target is None:
            plan["needs_info"].append({"channel_id": channel_id, "reasons": reasons})
            continue
        before = copy.deepcopy(_capability(channel))
        plan["changes"].append(
            {
                "channel_id": channel_id,
                "before": before,
                "before_sha256": _digest(before),
                "after": target,
                "after_sha256": _digest(target),
            }
        )
        plan["rollback"].append(
            {
                "channel_id": channel_id,
                "restore": before,
                "restore_sha256": _digest(before),
                "expected_after_sha256": _digest(target),
            }
        )
    if not plan["changes"] and not plan["needs_info"]:
        raise MigrationError("target plan is empty")
    plan["plan_sha256"] = _digest({key: value for key, value in plan.items() if key != "plan_sha256"})
    return plan


def _validate_reason_list(value: Any, field: str) -> None:
    if not isinstance(value, list) or not value:
        raise MigrationError(f"{field} must be a non-empty list")
    seen = set()
    for index, reason in enumerate(value):
        if not isinstance(reason, str) or not reason.strip():
            raise MigrationError(f"{field}[{index}] must be a non-empty string")
        if reason in seen:
            raise MigrationError(f"{field} contains duplicate reason")
        seen.add(reason)


def _validate_change(change: Any, field: str) -> Dict[str, Any]:
    _exact_keys(change, CHANGE_KEYS, field)
    channel_id = _strict_channel_id(change["channel_id"], f"{field}.channel_id")
    before = change["before"]
    after = change["after"]
    _scan_secrets(before, f"{field}.before")
    _scan_secrets(after, f"{field}.after")
    _validate_capability_shape(before, f"{field}.before")
    _validate_capability_shape(after, f"{field}.after")
    validate_capability(before)
    validate_capability(after)
    if not after.get("edits_accepted") or "edits" not in after.get("operations", []):
        raise MigrationError(f"{field}.after must explicitly accept edits")
    if change["before_sha256"] != _digest(before):
        raise MigrationError(f"{field}.before_sha256 does not match before")
    if change["after_sha256"] != _digest(after):
        raise MigrationError(f"{field}.after_sha256 does not match after")
    _strict_digest(change["before_sha256"], f"{field}.before_sha256")
    _strict_digest(change["after_sha256"], f"{field}.after_sha256")
    return {
        "channel_id": channel_id,
        "before": before,
        "before_sha256": change["before_sha256"],
        "after": after,
        "after_sha256": change["after_sha256"],
    }


def _validate_needs_info(item: Any, field: str) -> int:
    _exact_keys(item, NEEDS_INFO_KEYS, field)
    channel_id = _strict_channel_id(item["channel_id"], f"{field}.channel_id")
    _validate_reason_list(item["reasons"], f"{field}.reasons")
    return channel_id


def _validate_rollback_item(item: Any, field: str) -> Dict[str, Any]:
    _exact_keys(item, ROLLBACK_KEYS, field)
    channel_id = _strict_channel_id(item["channel_id"], f"{field}.channel_id")
    restore = item["restore"]
    _scan_secrets(restore, f"{field}.restore")
    _validate_capability_shape(restore, f"{field}.restore")
    validate_capability(restore)
    _strict_digest(item["restore_sha256"], f"{field}.restore_sha256")
    _strict_digest(item["expected_after_sha256"], f"{field}.expected_after_sha256")
    if item["restore_sha256"] != _digest(restore):
        raise MigrationError(f"{field}.restore_sha256 does not match restore")
    return {
        "channel_id": channel_id,
        "restore": restore,
        "restore_sha256": item["restore_sha256"],
        "expected_after_sha256": item["expected_after_sha256"],
    }


def _validate_plan_structure(plan: Any) -> Dict[str, Any]:
    _exact_keys(plan, PLAN_KEYS, "plan")
    if _strict_int(plan["version"], "plan.version") != 1:
        raise MigrationError("unsupported migration plan version")
    if plan["mode"] != "dry-run":
        raise MigrationError("plan.mode must be dry-run")
    target_ids = _strict_id_list(plan["target_channel_ids"], "plan.target_channel_ids")
    snapshot_ids = _strict_id_list(plan["snapshot_channel_ids"], "plan.snapshot_channel_ids")
    if not set(target_ids).issubset(snapshot_ids):
        raise MigrationError("plan target channels must exist in snapshot_channel_ids")
    _strict_digest(plan["snapshot_sha256"], "plan.snapshot_sha256")
    if not isinstance(plan["changes"], list):
        raise MigrationError("plan.changes must be a list")
    if not isinstance(plan["needs_info"], list):
        raise MigrationError("plan.needs_info must be a list")
    if not isinstance(plan["rollback"], list):
        raise MigrationError("plan.rollback must be a list")

    changes: Dict[int, Dict[str, Any]] = {}
    for index, change in enumerate(plan["changes"]):
        validated = _validate_change(change, f"plan.changes[{index}]")
        channel_id = validated["channel_id"]
        if channel_id in changes:
            raise MigrationError(f"plan.changes contains duplicate channel {channel_id}")
        changes[channel_id] = validated

    needs_info: Dict[int, Any] = {}
    for index, item in enumerate(plan["needs_info"]):
        channel_id = _validate_needs_info(item, f"plan.needs_info[{index}]")
        if channel_id in needs_info:
            raise MigrationError(f"plan.needs_info contains duplicate channel {channel_id}")
        needs_info[channel_id] = item

    rollback: Dict[int, Dict[str, Any]] = {}
    for index, item in enumerate(plan["rollback"]):
        validated = _validate_rollback_item(item, f"plan.rollback[{index}]")
        channel_id = validated["channel_id"]
        if channel_id in rollback:
            raise MigrationError(f"plan.rollback contains duplicate channel {channel_id}")
        rollback[channel_id] = validated

    change_ids = set(changes)
    needs_ids = set(needs_info)
    if change_ids & needs_ids:
        raise MigrationError("plan changes and needs_info overlap")
    if change_ids | needs_ids != set(target_ids):
        raise MigrationError("plan target channels do not match changes and needs_info")
    if set(rollback) != change_ids:
        raise MigrationError("plan rollback channels must exactly match changes")
    for channel_id, change in changes.items():
        restore = rollback[channel_id]["restore"]
        if _canonical(restore) != _canonical(change["before"]):
            raise MigrationError(f"plan rollback channel {channel_id} restore does not match before")
        if rollback[channel_id]["restore_sha256"] != change["before_sha256"]:
            raise MigrationError(f"plan rollback channel {channel_id} restore digest does not match before")
        if rollback[channel_id]["expected_after_sha256"] != change["after_sha256"]:
            raise MigrationError(f"plan rollback channel {channel_id} after digest does not match change")

    _strict_digest(plan["plan_sha256"], "plan.plan_sha256")
    actual = _digest({key: value for key, value in plan.items() if key != "plan_sha256"})
    if plan["plan_sha256"] != actual:
        raise MigrationError("plan digest mismatch")
    if not target_ids:
        raise MigrationError("plan target channels must not be empty")
    return {
        "target_ids": target_ids,
        "snapshot_ids": snapshot_ids,
        "changes": changes,
        "needs_info": needs_info,
        "rollback": rollback,
    }


def validate_plan(snapshot: Any, plan: Dict[str, Any]) -> None:
    _scan_secrets(snapshot)
    _scan_secrets(plan)
    structure = _validate_plan_structure(plan)
    channels = _channels(snapshot)
    snapshot_ids = sorted(int(channel["id"]) for channel in channels)
    if structure["snapshot_ids"] != snapshot_ids:
        raise MigrationError("plan snapshot_channel_ids do not match snapshot")
    if plan["snapshot_sha256"] != _digest(snapshot):
        raise MigrationError("snapshot digest drifted since plan generation")
    channel_map = {int(channel["id"]): channel for channel in channels}
    for channel_id, change in structure["changes"].items():
        current = _capability(channel_map[channel_id])
        if _canonical(current) != _canonical(change["before"]):
            raise MigrationError(f"channel {channel_id} changed after plan generation")


def rollback_payload(plan: Dict[str, Any]) -> Dict[str, Any]:
    _scan_secrets(plan)
    structure = _validate_plan_structure(plan)
    if not structure["changes"]:
        raise MigrationError("cannot create rollback for an empty change set")
    rollback = {
        "version": 1,
        "mode": "rollback-dry-run",
        "source_plan_sha256": plan["plan_sha256"],
        "restore": sorted(
            plan["rollback"],
            key=lambda item: _strict_channel_id(item["channel_id"], "rollback.channel_id"),
        ),
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
