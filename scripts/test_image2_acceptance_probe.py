#!/usr/bin/env python3
"""Offline tests for the strict Image2 acceptance parser."""

from __future__ import annotations

import json
import unittest

try:
    from scripts.image2_acceptance_probe import endpoint_for, parse_image2_response
except ModuleNotFoundError:  # Direct ``python scripts/test_....py`` invocation.
    from image2_acceptance_probe import endpoint_for, parse_image2_response


def legacy_probe_data_count(body: str) -> int:
    """The r2 field-only probe behavior, retained as a regression witness."""

    payload = json.loads(body)
    data = payload.get("data")
    return len(data) if isinstance(data, list) else 0


def legacy_probe_accepts(status: int, body: str) -> bool:
    """Model the old status/error-only success decision for this regression."""

    payload = json.loads(body)
    return 200 <= status < 300 and "error" not in payload


class Image2AcceptanceParserTests(unittest.TestCase):
    def test_old_probe_misclassifies_http_200_auth_envelope(self) -> None:
        body = json.dumps({"success": False, "message": "invalid access token"})
        self.assertEqual(legacy_probe_data_count(body), 0)
        self.assertTrue(legacy_probe_accepts(200, body))

        result = parse_image2_response(200, body, request_id="secret-request-id-1234", elapsed_ms=45)
        self.assertFalse(result.passed)
        self.assertEqual(result.reason_code, "success_false")
        self.assertEqual(result.data_count, 0)

    def test_error_and_message_envelopes_block_even_on_200(self) -> None:
        cases = [
            {"message": "no compatible Image2 channel remains"},
            {"error": {"code": "invalid_request"}},
            {"success": True, "message": "unexpected diagnostic"},
        ]
        for payload in cases:
            with self.subTest(payload=payload):
                result = parse_image2_response(200, json.dumps(payload))
                self.assertFalse(result.passed)

    def test_http_and_data_shape_fail_closed(self) -> None:
        cases = [
            (500, {"data": [{"url": "https://img.invalid/a.png"}]}),
            (200, {}),
            (200, {"data": []}),
            (200, {"data": [{"url": "", "b64_json": ""}]}),
            (200, {"data": [{"url": "https://img.invalid/a.png"}, {}]}),
        ]
        for status, payload in cases:
            with self.subTest(status=status, payload=payload):
                result = parse_image2_response(status, json.dumps(payload))
                self.assertFalse(result.passed)

    def test_only_nonempty_image_references_pass(self) -> None:
        url_result = parse_image2_response(
            200,
            json.dumps({"data": [{"url": "https://img.invalid/a.png"}]}),
            request_id="req-abcdef012345",
            elapsed_ms=123,
        )
        self.assertTrue(url_result.passed)
        self.assertEqual(url_result.data_count, 1)
        self.assertEqual(len(url_result.hashes), 1)

        b64_result = parse_image2_response(200, json.dumps({"data": [{"b64_json": "aGVsbG8="}]}))
        self.assertTrue(b64_result.passed)

    def test_route_contract_is_fixed_to_v1_api_token_endpoints(self) -> None:
        self.assertEqual(endpoint_for("generations"), "/v1/images/generations")
        self.assertEqual(endpoint_for("edits"), "/v1/images/edits")
        with self.assertRaises(ValueError):
            endpoint_for("pg-generations")

    def test_summary_contains_only_redacted_evidence(self) -> None:
        result = parse_image2_response(
            200,
            json.dumps({"data": [{"b64_json": "super-secret-image-bytes"}]}),
            request_id="secret-request-id-1234",
            elapsed_ms=45,
        )
        encoded = json.dumps(result.summary(), ensure_ascii=False)
        self.assertNotIn("secret-request-id-1234", encoded)
        self.assertNotIn("super-secret-image-bytes", encoded)
        self.assertNotIn("b64_json", encoded)
        self.assertNotIn("message", encoded)
        self.assertEqual(set(result.summary()), {"verdict", "request_id", "status", "elapsed_ms", "data_count", "hash"})


if __name__ == "__main__":
    unittest.main()
