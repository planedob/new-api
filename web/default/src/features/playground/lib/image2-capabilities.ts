import type {
  Image2CapabilityCatalog,
  Image2CapabilityOption,
  Image2Mode,
} from '../types'

export interface Image2Selection {
  size: string
  quality: Image2CapabilityOption['quality']
  n: number
}

const effectiveMaxN = (maxN: number) => (maxN > 0 ? maxN : 4)

export function image2Profiles(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode
): Image2CapabilityOption[] {
  return (catalog?.profiles ?? []).filter(
    (profile) => profile.operation === mode
  )
}

export function image2Sizes(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode
): string[] {
  return [
    ...new Set(image2Profiles(catalog, mode).map((profile) => profile.size)),
  ]
}

export function image2Qualities(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode,
  size: string
): Image2CapabilityOption['quality'][] {
  return [
    ...new Set(
      image2Profiles(catalog, mode)
        .filter((profile) => profile.size === size)
        .map((profile) => profile.quality)
    ),
  ]
}

export function image2MaxN(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode,
  size: string,
  quality: Image2CapabilityOption['quality']
): number {
  return image2Profiles(catalog, mode)
    .filter((profile) => profile.size === size && profile.quality === quality)
    .reduce((max, profile) => Math.max(max, effectiveMaxN(profile.max_n)), 0)
}

export function image2DefaultSelection(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode
): Image2Selection | null {
  const profile = image2Profiles(catalog, mode)[0]
  if (!profile) return null
  return {
    size: profile.size,
    quality: profile.quality,
    n: 1,
  }
}

export function isImage2SelectionSupported(
  catalog: Image2CapabilityCatalog | undefined,
  mode: Image2Mode,
  selection: Image2Selection
): boolean {
  return image2Profiles(catalog, mode).some(
    (profile) =>
      profile.size === selection.size &&
      profile.quality === selection.quality &&
      selection.n >= 1 &&
      selection.n <= effectiveMaxN(profile.max_n)
  )
}
