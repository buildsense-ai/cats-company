// Client-side email sanity check. The server performs the authoritative
// validation (publicsuffix); the client only checks the basic structure so
// obvious mistakes (missing @, no dot in the domain, empty labels, spaces)
// are caught immediately. TLD/suffix judgment is intentionally left to the
// server: a client-side allowlist would wrongly block valid addresses such
// as user@example.museum or user@example.international.
export function isValidEmailFormat(value) {
  const email = String(value || '').trim();
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return false;
  const domain = email.split('@')[1].toLowerCase();
  const labels = domain.split('.');
  if (labels.length < 2) return false;
  if (!labels.every((label) => label.length > 0)) return false;
  return true;
}
