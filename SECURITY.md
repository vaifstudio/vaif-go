# Security policy

## Reporting a vulnerability

Report security vulnerabilities in this SDK privately, through the "Report a
vulnerability" form on this repository's Security tab (GitHub private
vulnerability reporting) or by email to security@vaif.studio. Do not open a
public GitHub issue or pull request for a vulnerability.

The same address is published in the VAIF Studio
[security.txt](https://vaif.studio/.well-known/security.txt) and on the
[security page](https://vaif.studio/security/#disclosure), and it is the
contact for vulnerabilities in the VAIF Studio API and console as well.

## What to include

- The SDK version (the `Version` constant, or the version in your `go.mod`)
  and your Go version and operating system.
- A description of the vulnerability and its impact.
- Steps to reproduce it, preferably a minimal program or test.
- Any proof-of-concept output, logs or request IDs (`vaif.Error.RequestID`).
  Remove access tokens, refresh tokens, API keys and other credentials first.

Test only against projects and accounts you own, and do not access, change or
keep other people's data.

## What happens next

1. We acknowledge your report within 2 business days.
2. We send an initial assessment within 5 business days.
3. We investigate, confirm whether the SDK is affected, and keep you informed
   of our progress.
4. When a fix is ready, we publish a release and record it in
   [CHANGELOG.md](CHANGELOG.md). With your permission, we credit you in the
   release notes.

Please keep the details private until a fixed release is available. We do not
take legal action against good-faith research that follows this policy.

## Supported versions

| Version | Supported |
| --- | --- |
| 1.x | Yes |
| 0.1.0, 0.1.1 | No |

Security fixes are released for the latest 1.x minor version. Upgrade to the
latest 1.x release to receive them. Versions 0.1.0 and 0.1.1 were published
under the module path `github.com/vaifllc/vaif-go` and are not supported.
