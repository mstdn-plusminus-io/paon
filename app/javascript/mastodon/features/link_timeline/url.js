export const decodeLinkTimelineURL = value => {
  if (!value) {
    return null;
  }

  try {
    const decoded = decodeURIComponent(value);
    const parsed = new URL(decoded);

    if (decoded !== decoded.trim() || !['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) {
      return null;
    }

    return decoded;
  } catch {
    return null;
  }
};

