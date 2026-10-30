# Security policy

## Reporting a vulnerability

Please report it privately, not in a public issue. Use GitHub's private vulnerability reporting: on the repository's
**Security** tab, choose **Report a vulnerability**
(<https://github.com/MarcZX10/gauge-and-grain/security/advisories/new>).

Include the `gg` version (`gg version`), your operating system and the steps to reproduce. Sanitise any log you attach:
remove prompts, code and private paths.

## Supported versions

Only the latest release is supported. Fixes ship in a new release; older releases are not patched.

## Untrusted logs

`gg` reads local usage logs that may contain hostile data: malformed or oversized records, terminal control sequences,
and spreadsheet formulas in paths. It is designed to treat them as untrusted input, so that a log can neither crash
`gg`, corrupt the totals, nor change the terminal or run anything. How this works is described in
[docs/PRIVACY.md](docs/PRIVACY.md#logs-are-untrusted-input). A report that shows otherwise is a security bug.
