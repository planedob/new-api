# Image2 capability verification

Image2 smart routing distinguishes three different facts:

1. `image2_declared_capability`: what the supplier or operator claims. It is visible for comparison but never routes traffic.
2. `image2_capability`: the exact profiles derived from controlled tests.
3. `image2_capability_verification`: evidence integrity, test time, expiry and status.

## Test matrix

Each proof is a complete profile, not an independent list item:

- operation: `generations` or multipart `edits`;
- exact request size and normalized tier: auto, 1024, 2048, UHD/4096, including each advertised aspect ratio;
- quality: provider default (`auto`/omitted), `standard`, or `high`;
- `n`: test `1` first; larger values are routable only after their own proof;
- fixed final channel, non-empty image, one request/one consumption, and failure-no-charge evidence.

The offline builder `scripts/image2_capability_test_matrix.py` accepts no supplier declaration or credential field. A passing test creates one exact `profiles[]` row. A failed test is retained in `rejected_profiles` and never becomes routable. Conflicting pass/fail evidence for the same profile blocks the plan and requires investigation.

## Runtime policy

- `IMAGE2_VERIFIED_CAPABILITY_REQUIRED=false` is the migration-safe default.
- After all participating production channels have evidence records, set it to `true` in a separately authorized rollout.
- Strict mode excludes missing, failed, conflicting, stale, expired or malformed verification.
- The verification record binds the capability JSON by SHA-256; editing claimed capability without regenerating test evidence invalidates routing.
- Changing a channel Key, base URL, model list, group or model mapping marks its verification `stale` unless the same request carries the current managed-settings CAS digest and a new verified record.

## Admin behavior

Default and Classic channel forms show Image2 verification status and exact proven profiles as read-only data. Ordinary channel edits cannot overwrite the managed declaration/capability/verification fields. A controlled migration must first read `image2_managed_sha256` and submit it as `image2_managed_expected_sha256`; a stale digest receives HTTP 409.

This candidate does not run paid tests automatically and does not change production. Test execution, budgets, provider billing reconciliation and activation of strict mode remain separately authorized operations.
