/** Explicit extension descriptors never inherit upstream administrator bypasses. */
export function parseExtensionPermission(
  descriptor: string,
): { resource: string; action: string } | null {
  const match =
    /^([a-z][a-z0-9-]{0,63}):([a-z][a-z0-9-]{0,63}(?:\.[a-z][a-z0-9-]{0,63})*)$/.exec(
      descriptor,
    );
  return match ? { resource: match[1], action: match[2] } : null;
}
