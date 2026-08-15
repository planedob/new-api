# Image2 capability verification

Image2 smart routing distinguishes three different facts:

1. `image2_declared_capability`: what the supplier documents; this is the primary routing contract.
2. `image2_capability`: controlled-test or legacy observed capability, used when the supplier declaration is missing.
3. `image2_capability_verification`: evidence integrity, test time, expiry and status.

The declaration avoids requiring a paid Cartesian-product test matrix for every
channel. Tests supplement missing documentation, investigate declaration/runtime
conflicts, and periodically sample stability. A malformed or explicitly disabled
declaration fails closed and is never silently replaced by guessed capability.

## Test matrix

When testing is needed, each proof is a complete profile, not an independent list item:

- operation: `generations` or multipart `edits`;
- exact request size and normalized tier: auto, 1024, 2048, UHD/4096, including each advertised aspect ratio;
- quality: provider default (`auto`/omitted), `standard`, or `high`;
- `n`: test `1` first; larger values are routable only after their own proof;
- fixed final channel, non-empty image, one request/one consumption, and failure-no-charge evidence.

The offline builder `scripts/image2_capability_test_matrix.py` accepts no credential field. A passing test creates one exact fallback `profiles[]` row. A failed test is retained in `rejected_profiles` for investigation. Supplier-declared capability remains a separate routing source; runtime health controls may temporarily remove an unhealthy channel without rewriting the supplier statement.

## Runtime policy

- `IMAGE2_VERIFIED_CAPABILITY_REQUIRED=false` is the migration-safe default for test-derived fallback capability.
- If enabled, strict verification applies only when a supplier declaration is missing.
- A valid enabled supplier declaration routes without exhaustive combination proofs.
- A missing declaration may use current passed test evidence; failed, conflicting, stale, expired or malformed fallback evidence is excluded.
- The verification record binds the capability JSON by SHA-256; editing claimed capability without regenerating test evidence invalidates routing.
- Changing a channel Key, base URL, model list, group or model mapping marks its verification `stale` unless the same request carries the current managed-settings CAS digest and a new verified record.

## Admin behavior

Default and Classic channel forms show whether routing comes from the supplier declaration or test fallback. A missing declaration is shown as an action item. Ordinary channel edits cannot overwrite the managed declaration/capability/verification fields. A controlled migration must first read `image2_managed_sha256` and submit it as `image2_managed_expected_sha256`; a stale digest receives HTTP 409.

This candidate does not run paid tests automatically and does not change production. Test execution, budgets, provider billing reconciliation and activation of strict mode remain separately authorized operations.
