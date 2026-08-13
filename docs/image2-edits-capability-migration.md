# Image2 edits capability migration (offline candidate)

This document describes the fail-closed migration contract implemented by
`scripts/image2_capability_migration.py`. It is an offline plan/validate/
rollback tool; it does not call an Aibuff API, read credentials, or write a
channel.

## Capability audit boundary

The following is a redacted audit summary. A historical A-layer success is not
treated as B-layer smart-router capability proof:

| channel | observed evidence | current capability shape | candidate status |
|---:|---|---|---|
| `#44` | historical direct edits successes exist | `generations`, `edits_accepted=false` | `CONFLICT / NEEDS_INFO`: require a fresh fixed-channel edits run |
| `#32` | historical legacy/fallback edits successes exist; UHD generation evidence exists | `generations`, `resolutions=[uhd]`, `qualities=[]`, `edits_accepted=false` | `CONFLICT / NEEDS_INFO`: require a fresh direct fixed-channel run |
| `#23` | no current direct fixed-channel edits proof | generation-only history | `NEEDS_INFO` |
| `#47` | generation explicit `high` + UHD evidence | generation-only, explicit `high` | edits `NEEDS_INFO`; generation high does not imply edits |
| `#43` | no direct fixed-channel edits proof in the inspected evidence | capability shape not verified here | `NEEDS_INFO` |
| `#71` | no direct fixed-channel edits proof in the inspected evidence | capability shape not verified here | `NEEDS_INFO` |
| `#63` | no direct fixed-channel edits proof in the inspected evidence | capability shape not verified here | `NEEDS_INFO` |

No row is automatically promoted to `operations=["edits"]`. Historical
traffic, model names, and a fallback chain are deliberately insufficient.

## Failure-first evidence

On the r3 parent (`555a46d...`), enabling the edits gate with the production
history shape (`#44/#23` generation-only, `edits_accepted=false`) and sending a
multipart `/v1/images/edits` request (`quality=auto`, `size=auto`, `n=1`) built a
zero-candidate chain (`operation_unsupported` for both channels). The same
request shape on `/pg/images/edits` follows the same relay mode. This is the
intended safety RED: the router must not guess edits support from model names
or old fallback history. The GREEN path is a fresh fixed-channel proof followed
by an explicit capability plan and then the edits gate.

The r3 parent also treated `qualities=[]` as a wildcard for explicit quality.
The candidate changes this to fail-closed: omitted/`auto` use provider default;
explicit `standard`/`high` require a declared quality, otherwise the reason is
`quality_unverified`. Capability declarations themselves accept only the
verified explicit values `standard` and `high`; `auto`, blank, duplicate, or
unknown values block the candidate.

## Required proof for an edits promotion

An evidence entry must identify the fixed channel and operation, and must
include:

- a fresh `evidence_class` of `fresh_fixed_channel`;
- a non-empty image and non-empty request ID;
- `final_channel_id` equal to the intended channel;
- `n=1`, a supported size (`auto`/1024/2048/UHD), and any explicit quality;
- `fixed_channel_boundary=true`;
- `no_duplicate_request=true` and `failure_no_charge=true`.

Only then does the planner generate a dry-run target adding `edits` and
`edits_accepted=true`. Explicit `standard`/`high` is added only when that
quality is present in the fresh evidence. Omitted/`auto` quality never becomes
a capability declaration.

## Commands

```text
python3 scripts/image2_capability_migration.py plan \
  --snapshot nonsecret-channel-snapshot.json \
  --evidence fixed-channel-edits-evidence.json \
  --output image2-edits-plan.json

python3 scripts/image2_capability_migration.py validate \
  --snapshot nonsecret-channel-snapshot.json \
  --plan image2-edits-plan.json

python3 scripts/image2_capability_migration.py rollback \
  --plan image2-edits-plan.json \
  --output image2-edits-rollback.json
```

`plan` emits `NEEDS_INFO` for missing or historical-only evidence. `validate`
fails closed on snapshot drift, target capability errors, credential-bearing
input, or a plan digest mismatch. `rollback` emits exact pre-change settings
and expected post-change digests; it performs no write.
