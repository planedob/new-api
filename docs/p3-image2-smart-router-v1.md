# P3 Image2 Smart Router v1 — development handoff

Status: development complete, not released. This document describes local code
only; it does not authorize channel, model-group, pricing, or production
configuration changes.

## Existing capability check

- One public model can already bind to many enabled channels: the channel cache
  indexes `group -> model -> []channel IDs` in `model/channel_cache.go`, and
  `model.GetRandomSatisfiedChannel*` selects an eligible one.
- There was no resolution-aware selector. The new `model.GetSatisfiedChannels`
  deliberately exposes the full eligible set to the capability router.
- This belongs in the built-in relay, not a plugin or a separate service: the
  relay owns the replayable request body, billing session, response-written
  evidence, and `RelayInfo` needed for safe failover.
- Existing reusable safety controls are `service.EvaluateSafeFailover`, the
  exhaustive exclusion set in `service.RetryParam`, and the relay's request ID
  / billing lifecycle. It already stops on response started, customer cancel,
  content safety, acceptance evidence, and skip-retry errors; it continues for
  transport failures, 429, and explicit 5xx.

## Architecture

Each channel may opt in through its existing channel `setting` JSON:

```json
{
  "image2_capability": {
    "enabled": true,
    "operations": ["generations", "edits"],
    "resolutions": ["1024", "2048", "uhd"],
    "qualities": ["standard", "high"],
    "max_n": 4,
    "route_priority": 20,
    "edits_accepted": true
  }
}
```

`route_priority` provides the desired ordering without source code channel IDs
or cost ordering. Operators can declare Web/Codex/Adobe priorities as 10/20/30
for 1024, 20/30 for 2048, and 30 for UHD using their capabilities. A candidate
is excluded before an upstream attempt when operation, edit acceptance,
resolution, quality, or quantity is incompatible. Missing capability metadata
is safely excluded while the feature is enabled.

The `IMAGE2_SMART_ROUTING_ENABLED` environment switch defaults to `false`. In
that state the original selector is untouched. When enabled for `gpt-image-2`
image generation/edit requests, capability ordering becomes the candidate
chain and the existing safe-failover logic makes each compatible channel
eligible once at most.

## Logging and billing relationship

The relay logs the normalized request shape, selected chain, and every
exclusion reason as `image2 smart routing`. The normal `use_channel` chain and
existing error log `admin_info.use_channel` continue to provide per-attempt
channel history. Billing remains attached to the public model and existing
`RelayInfo` billing session; this change does not select by cost or alter any
price data.

## Known rollout requirements

No existing channel has been configured by this change. Before any isolated
acceptance deployment, each intended test channel needs explicit capability
metadata, and Adobe edits must remain `edits_accepted: false` until accepted.
