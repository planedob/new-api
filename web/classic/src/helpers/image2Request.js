/**
 * Enforce the native Image2 wire contract at the request boundary. Only edits
 * may be multipart; generations are always JSON and discard stale image
 * fields that may survive a UI mode/model transition.
 */
export const normalizeImage2RequestPayload = (payload, operation) => {
  const isFormDataPayload =
    typeof FormData !== 'undefined' && payload instanceof FormData;
  if (!isFormDataPayload || operation === 'edits') return payload;

  const jsonPayload = {};
  payload.forEach((value, key) => {
    if (key === 'image' || typeof value !== 'string') return;
    jsonPayload[key] = value;
  });
  return jsonPayload;
};
