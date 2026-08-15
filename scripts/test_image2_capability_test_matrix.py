from __future__ import annotations

import copy
import unittest

from scripts.image2_capability_migration import MigrationError
from scripts.image2_capability_test_matrix import build_verified_capability


def proof(operation="generations", size="1024", quality="auto", status="passed"):
    return {
        "channel_id": 44,
        "operation": operation,
        "size": size,
        "quality": quality,
        "n": 1,
        "status": status,
        "request_id": f"synthetic-{operation}-{size}-{quality}-{status}",
        "final_channel_id": 44,
        "non_empty_image": status == "passed",
        "fixed_channel_boundary": True,
        "no_duplicate_request": True,
        "failure_no_charge": True,
        "failure_reason": "upstream rejected this exact profile" if status == "failed" else "",
    }


class Image2CapabilityTestMatrixTests(unittest.TestCase):
    def build(self, tests):
        return build_verified_capability(
            44, tests, route_priority=10,
            verified_at="2026-08-15T00:00:00+08:00",
            valid_until="2026-09-14T00:00:00+08:00",
        )

    def test_capability_contains_only_exact_passed_profiles(self):
        result = self.build([
            proof("generations", "4096", "high"),
            proof("edits", "1024", "standard"),
        ])
        profiles = result["image2_capability"]["profiles"]
        self.assertEqual(profiles, [
            {"operation": "edits", "resolution": "1024", "size": "1024x1024", "quality": "standard", "max_n": 1},
            {"operation": "generations", "resolution": "uhd", "size": "4096x4096", "quality": "high", "max_n": 1},
        ])
        self.assertNotIn(
            {"operation": "edits", "resolution": "uhd", "size": "4096x4096", "quality": "high", "max_n": 1},
            profiles,
        )

    def test_failed_profile_is_reported_but_not_routable(self):
        result = self.build([proof(), proof("edits", "2048", "high", "failed")])
        self.assertEqual(len(result["image2_capability"]["profiles"]), 1)
        self.assertEqual(result["rejected_profiles"][0]["operation"], "edits")

    def test_conflicting_same_profile_requires_investigation(self):
        failed = proof(status="failed")
        passed = proof(status="passed")
        passed["request_id"] = "another-request"
        with self.assertRaises(MigrationError):
            self.build([failed, passed])

    def test_supplier_declaration_or_credentials_are_not_inputs(self):
        entry = proof()
        entry["api_key"] = "must-not-be-read"
        with self.assertRaises(MigrationError):
            self.build([entry])

    def test_pass_without_image_is_rejected(self):
        entry = copy.deepcopy(proof())
        entry["non_empty_image"] = False
        with self.assertRaises(MigrationError):
            self.build([entry])


if __name__ == "__main__":
    unittest.main()
