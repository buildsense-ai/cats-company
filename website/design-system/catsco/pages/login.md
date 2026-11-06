# CatsCo Login Page Override

- Provide a quiet, centered account-entry card with the CatsCo mark, return-home navigation and a skip link.
- Authentication happens in the product workspace through `appLoginUrl` and `appRegisterUrl`. The public website does not collect passwords or implement a login form.
- Keep the existing plan, billing and source query parameters on workspace links for compatibility. Do not interpret legacy plan names as new packages or display retired package prices on this landing page; the product catalogue owns commercial details.
- Show the compact invitation summary only for `access=invite` without plan or billing parameters. Distinguish entries from pricing with `source=pricing` and provide a link back to pricing.
- Keep the login action primary and registration visibly available. Controls are at least 48px tall and preserve visible keyboard focus.
- Mobile layout uses 20px gutters with no horizontal page scrolling.
