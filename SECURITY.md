# Security Policy

KERN executes untrusted code by design — take security reports seriously.

## Reporting

Do **not** open a public issue for vulnerabilities. Email **info@tdvorak.dev**
with a description, reproduction, and affected component. Expect an
acknowledgement within 72 hours.

## Scope

In scope: container escape, secret exposure (logs, API, claims), auth bypass,
webhook signature issues, privilege escalation between trust levels,
artifact/blob path traversal.

By design (not bugs): jobs run untrusted code in containers — that is the
product; a compromised job container is expected, a compromised host is not.
