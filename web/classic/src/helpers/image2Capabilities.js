export const image2Profiles = (catalog, operation) =>
  (catalog?.profiles || []).filter(
    (profile) => profile.operation === operation,
  );

export const image2Sizes = (catalog, operation) => [
  ...new Set(image2Profiles(catalog, operation).map((profile) => profile.size)),
];

export const image2Qualities = (catalog, operation, size) => [
  ...new Set(
    image2Profiles(catalog, operation)
      .filter((profile) => profile.size === size)
      .map((profile) => profile.quality),
  ),
];

export const image2MaxN = (catalog, operation, size, quality) =>
  image2Profiles(catalog, operation)
    .filter((profile) => profile.size === size && profile.quality === quality)
    .reduce(
      (max, profile) =>
        Math.max(max, Number(profile.max_n) > 0 ? Number(profile.max_n) : 4),
      0,
    );

export const image2DefaultSelection = (catalog, operation) => {
  const profile = image2Profiles(catalog, operation)[0];
  if (!profile) return null;
  return {
    size: profile.size,
    quality: profile.quality,
    n: 1,
  };
};

export const isImage2SelectionSupported = (
  catalog,
  operation,
  size,
  quality,
  n,
) =>
  image2Profiles(catalog, operation).some(
    (profile) =>
      profile.size === size &&
      profile.quality === quality &&
      Number(n) >= 1 &&
      Number(n) <= (Number(profile.max_n) > 0 ? Number(profile.max_n) : 4),
  );
