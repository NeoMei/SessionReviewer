// Native provider Session IDs are case-sensitive ASCII path components.
export const SESSION_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export function sessionIdentity(value: unknown, path: string): string {
  if (typeof value !== "string" || !SESSION_ID.test(value)) throw new Error(`${path} must be a safe Session ID`);
  return value;
}
