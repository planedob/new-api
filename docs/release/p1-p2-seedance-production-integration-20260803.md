# P1/P2 production-baseline integration

Date: 2026-08-03 CST

This integration is based on the production source commit `d207ac4e` and
re-applies the reviewed P1 entitlement changes and P2 token-group visibility
changes. It must not replace the production Seedance baseline with a candidate
that was based only on `19be7b44`.

## Source inputs

- Production baseline: `d207ac4e`
- P1 reviewed source: `5bc52715235c5c5da1aa91d65b3c19d7cf4181a`
- P2 reviewed source: `69ce58f6982ed53816fc10633f0f3f393f092fae`

## Verification

- Targeted Go race tests for controller/model/service/middleware/router: PASS
- Go vet for controller/model/service/middleware/router: PASS
- Go build after frontend assets: PASS
- Default frontend typecheck and build: PASS
- Classic frontend build: PASS
- Full Go test: baseline failures remain in Claude file-content conversion and
  stream-scanner pre-initialized status; the same failures reproduce on
  `d207ac4e` and are unrelated to P1/P2.

No production deployment is implied by this document. An image tag and digest,
database backup/restore evidence, and an exact rollback command are still
required before changing production containers.
