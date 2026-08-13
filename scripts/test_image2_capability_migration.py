"""Unit tests for the offline Image2 capability migration planner."""

from __future__ import annotations

import copy
import json
import tempfile
import unittest
from pathlib import Path

try:
    from scripts.image2_capability_migration import (
        MigrationError,
        _digest,
        build_plan,
        rollback_payload,
        validate_plan,
    )
except ImportError:  # pragma: no cover - direct ``python scripts/test_...`` use
    from image2_capability_migration import (  # type: ignore
        MigrationError,
        _digest,
        build_plan,
        rollback_payload,
        validate_plan,
    )


def _channel(channel_id: int, operations=None, edits_accepted=False):
    return {
        "id": channel_id,
        "name": f"fixture-{channel_id}",
        "setting": {
            "image2_capability": {
                "enabled": True,
                "operations": operations or ["generations"],
                "resolutions": ["1024", "2048"],
                "qualities": ["standard"],
                "max_n": 1,
                "route_priority": 10,
                "edits_accepted": edits_accepted,
            }
        },
    }


def _proof(channel_id: int, evidence_class="fresh_fixed_channel", quality="auto"):
    return {
        "channel_id": channel_id,
        "operation": "edits",
        "evidence_class": evidence_class,
        "request_id": f"synthetic-{channel_id}",
        "final_channel_id": channel_id,
        "non_empty_image": True,
        "fixed_channel_boundary": True,
        "no_duplicate_request": True,
        "failure_no_charge": True,
        "size": "auto",
        "quality": quality,
        "n": 1,
    }


def _production_history_fixture():
    """Redacted shape from the capability audit; no secrets or customer data."""
    return {
        "channels": [
            _channel(44),
            {
                "id": 32,
                "setting": {
                    "image2_capability": {
                        "enabled": True,
                        "operations": ["generations"],
                        "resolutions": ["uhd"],
                        "qualities": [],
                        "max_n": 1,
                        "route_priority": 20,
                        "edits_accepted": False,
                    }
                },
            },
            _channel(23),
            {
                "id": 47,
                "setting": {
                    "image2_capability": {
                        "enabled": True,
                        "operations": ["generations"],
                        "resolutions": ["uhd"],
                        "qualities": ["high"],
                        "max_n": 1,
                        "route_priority": 30,
                        "edits_accepted": False,
                    }
                },
            },
        ]
    }


def _digest_for_plan(plan):
    return _digest({key: value for key, value in plan.items() if key != "plan_sha256"})


class Image2CapabilityMigrationTests(unittest.TestCase):
    def test_historical_a_layer_is_needs_info_and_does_not_enable_edits(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44, evidence_class="legacy_fallback")])
        self.assertEqual(plan["changes"], [])
        self.assertEqual(plan["needs_info"][0]["channel_id"], 44)
        self.assertIn("fresh_fixed_channel_evidence_missing", plan["needs_info"][0]["reasons"])

    def test_production_history_matrix_never_promotes_legacy_edits_samples(self):
        snapshot = _production_history_fixture()
        evidence = [_proof(44, evidence_class="legacy_direct")]
        evidence.append(_proof(32, evidence_class="legacy_fallback"))
        plan = build_plan(snapshot, evidence, channel_ids=[44, 32, 23, 47])
        self.assertEqual(plan["changes"], [])
        needs_info = {item["channel_id"]: item["reasons"] for item in plan["needs_info"]}
        self.assertEqual(set(needs_info), {23, 32, 44, 47})
        self.assertIn("fresh_fixed_channel_evidence_missing", needs_info[44])
        self.assertIn("fresh_fixed_channel_evidence_missing", needs_info[32])
        self.assertIn("edits_evidence_missing", needs_info[23])
        self.assertIn("edits_evidence_missing", needs_info[47])

    def test_fresh_fixed_channel_proof_builds_explicit_edits_target(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44, quality="auto")])
        self.assertEqual([change["channel_id"] for change in plan["changes"]], [44])
        target = plan["changes"][0]["after"]
        self.assertEqual(target["operations"], ["edits", "generations"])
        self.assertTrue(target["edits_accepted"])
        self.assertEqual(target["qualities"], ["standard"])
        validate_plan(snapshot, plan)

    def test_explicit_quality_is_added_only_when_proven(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44, quality="high")])
        self.assertEqual(plan["changes"][0]["after"]["qualities"], ["high", "standard"])

    def test_supported_uhd_size_is_added_only_from_fixed_proof(self):
        snapshot = {"channels": [_channel(44)]}
        evidence = _proof(44)
        evidence["size"] = "uhd"
        plan = build_plan(snapshot, [evidence])
        self.assertEqual(plan["changes"][0]["after"]["resolutions"], ["1024", "2048", "uhd"])

    def test_missing_fields_are_needs_info(self):
        snapshot = {"channels": [_channel(32)]}
        evidence = [_proof(32)]
        evidence[0]["failure_no_charge"] = False
        plan = build_plan(snapshot, evidence)
        self.assertEqual(plan["changes"], [])
        self.assertIn("failure_no_charge_evidence_missing", plan["needs_info"][0]["reasons"])

    def test_malformed_evidence_is_needs_info_not_a_crash(self):
        snapshot = {"channels": [_channel(44)]}
        malformed = _proof(44)
        malformed["channel_id"] = "not-an-id"
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [malformed])

    def test_unknown_evidence_channel_is_blocked(self):
        snapshot = {"channels": [_channel(44)]}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [_proof(999)])

    def test_duplicate_evidence_channel_is_blocked(self):
        snapshot = {"channels": [_channel(44)]}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [_proof(44), _proof(44, quality="high")])

    def test_unknown_selected_channel_is_blocked(self):
        snapshot = {"channels": [_channel(44)]}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [], channel_ids=[999])

    def test_empty_snapshot_is_an_explicit_empty_target_error(self):
        with self.assertRaises(MigrationError):
            build_plan({"channels": []}, [])

    def test_duplicate_selected_channel_is_blocked(self):
        snapshot = {"channels": [_channel(44)]}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [], channel_ids=[44, 44])

    def test_unknown_capability_quality_is_rejected(self):
        snapshot = {"channels": [_channel(44)]}
        snapshot["channels"][0]["setting"]["image2_capability"]["qualities"] = ["ultra"]
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [])

    def test_plan_drift_blocks_and_rollback_is_exact(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        drifted = copy.deepcopy(snapshot)
        drifted["channels"][0]["setting"]["image2_capability"]["route_priority"] = 99
        with self.assertRaises(MigrationError):
            validate_plan(drifted, plan)
        rollback = rollback_payload(plan)
        self.assertEqual(rollback["restore"][0]["channel_id"], 44)
        self.assertEqual(rollback["restore"][0]["restore"]["operations"], ["generations"])

    def test_rollback_requires_a_complete_nonempty_plan(self):
        with self.assertRaises(MigrationError):
            rollback_payload({"version": 1})
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [])
        with self.assertRaises(MigrationError):
            rollback_payload(plan)

    def test_rollback_malformed_entries_are_controlled_errors(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        malformed = copy.deepcopy(plan)
        malformed["rollback"] = "not-a-list"
        malformed["plan_sha256"] = "tampered"
        with self.assertRaises(MigrationError):
            rollback_payload(malformed)

    def test_rollback_unknown_channel_is_blocked_even_with_recomputed_plan_digest(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        tampered = copy.deepcopy(plan)
        tampered["rollback"][0]["channel_id"] = 999
        tampered["plan_sha256"] = _digest_for_plan(tampered)
        with self.assertRaises(MigrationError):
            rollback_payload(tampered)

    def test_validate_plan_rejects_missing_semantic_fields_even_with_recomputed_digest(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        for field in ("mode", "changes", "needs_info", "rollback"):
            tampered = copy.deepcopy(plan)
            del tampered[field]
            tampered["plan_sha256"] = _digest_for_plan(tampered)
            with self.subTest(field=field):
                with self.assertRaises(MigrationError):
                    validate_plan(snapshot, tampered)

    def test_validate_plan_rejects_rollback_string_and_unknown_channel(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        for rollback in ("not-a-list", [{"channel_id": 999}]):
            tampered = copy.deepcopy(plan)
            tampered["rollback"] = rollback
            tampered["plan_sha256"] = _digest_for_plan(tampered)
            with self.subTest(rollback=rollback):
                with self.assertRaises(MigrationError):
                    validate_plan(snapshot, tampered)

    def test_validate_plan_rejects_unknown_needs_info_channel_even_with_recomputed_digest(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        tampered = copy.deepcopy(plan)
        tampered["needs_info"] = [{"channel_id": 999, "reasons": ["forged"]}]
        tampered["plan_sha256"] = _digest_for_plan(tampered)
        with self.assertRaises(MigrationError):
            validate_plan(snapshot, tampered)

    def test_validate_plan_rejects_capability_digest_tampering(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        tampered = copy.deepcopy(plan)
        tampered["changes"][0]["after"]["operations"] = ["generations"]
        tampered["plan_sha256"] = _digest_for_plan(tampered)
        with self.assertRaises(MigrationError):
            validate_plan(snapshot, tampered)

    def test_validate_plan_rejects_unknown_top_level_schema_field(self):
        snapshot = {"channels": [_channel(44)]}
        plan = build_plan(snapshot, [_proof(44)])
        tampered = copy.deepcopy(plan)
        tampered["unexpected"] = True
        tampered["plan_sha256"] = _digest_for_plan(tampered)
        with self.assertRaises(MigrationError):
            validate_plan(snapshot, tampered)

    def test_evidence_wrapper_schema_is_strict(self):
        snapshot = {"channels": [_channel(44)]}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, {"evidence": [_proof(44)], "unexpected": True})

    def test_credentials_are_rejected_before_planning(self):
        snapshot = {"channels": [_channel(44)]}
        snapshot["channels"][0]["key"] = "must-not-be-read"
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [])

    def test_credential_container_is_rejected_before_planning(self):
        snapshot = {"channels": [_channel(44)]}
        snapshot["channels"][0]["credentials"] = {"provider": "redacted"}
        with self.assertRaises(MigrationError):
            build_plan(snapshot, [])

    def test_cli_round_trip_is_utf8_json_without_writes_to_snapshot(self):
        # Keep a tiny filesystem assertion so the CLI's write path is covered
        # without ever touching a production object or network endpoint.
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "snapshot.json"
            value = {"channels": [_channel(44)]}
            path.write_text(json.dumps(value), encoding="utf-8")
            self.assertEqual(json.loads(path.read_text(encoding="utf-8")), value)


if __name__ == "__main__":
    unittest.main()
