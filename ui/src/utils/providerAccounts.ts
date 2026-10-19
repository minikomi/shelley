// Saved provider accounts may legitimately share an explicit label (for
// example two keys both called "Work"). The label is never rewritten; where
// two accounts would look identical we append a short slice of the stable
// provider id so the picker and Manage Models can tell them apart.

export interface ProviderAccountRef {
  id: string;
  label: string;
}

function labelKey(label: string): string {
  return label.trim().toLowerCase();
}

/** Returns "#abcd" style suffixes keyed by account id, only for accounts whose label collides. */
export function accountSuffixes(accounts: ProviderAccountRef[]): Map<string, string> {
  const idsByLabel = new Map<string, Set<string>>();
  for (const account of accounts) {
    const key = labelKey(account.label);
    if (!idsByLabel.has(key)) idsByLabel.set(key, new Set());
    idsByLabel.get(key)!.add(account.id);
  }
  const suffixes = new Map<string, string>();
  for (const ids of idsByLabel.values()) {
    if (ids.size < 2) continue;
    for (const id of ids) suffixes.set(id, `#${id.slice(0, 4)}`);
  }
  return suffixes;
}

/** Host part of an endpoint URL, or "" when it cannot be parsed. */
export function endpointHost(endpoint: string | undefined): string {
  if (!endpoint) return "";
  try {
    return new URL(endpoint).host;
  } catch {
    return "";
  }
}
