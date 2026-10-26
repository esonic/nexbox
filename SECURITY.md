# Security Policy

## Reporting a vulnerability

Please do not open a public issue for a suspected vulnerability or exposed
credential. Use GitHub's private vulnerability reporting for this repository:

<https://github.com/esonic/nexbox/security/advisories/new>

Include the affected version, reproduction steps, impact, and any suggested
mitigation. Do not include live credentials or personal data. Maintainers will
acknowledge the report as soon as practical and coordinate disclosure after a
fix is available.

## Deployment boundary

NexBox runs agent CLIs and terminals with the permissions of its operating
system account. Keep the default loopback listen address unless access is
protected by authentication and a trusted HTTPS reverse proxy. Treat the data
directory, CLI configuration, browser profile, and logs as sensitive.
