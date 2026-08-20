# Reporting a security problem

**Do not open an issue.** Use GitHub's private reporting —
[**Report a vulnerability**](https://github.com/orkcom-tech/cogitorium/security/advisories/new)
— which reaches the maintainers and nobody else. It is a form on this
repository; you need a GitHub account and nothing else, and there is no mailing
list in between.

Tell us what you did, what you got, and what you think it lets somebody do. A
proof of concept helps and is not required.

**What to expect:** an acknowledgement within three working days, and an
assessment within ten. If we agree it is a problem you will get a fix, a release
and credit in the advisory unless you would rather not be named. If we disagree
you will get the reasoning rather than silence.

## What is in scope

Anything that crosses one of the boundaries this product is built on:

- **Agent-authored code escaping the sandbox**, or reaching the network without
  a grant.
- **The approval gate being bypassed** — a gear running at a version nobody
  approved, or a plugin taking effect before it was approved.
- **A credential leaving where it belongs**: a provider key or a named secret
  reaching a model's context, a run's output, a log, or an unprivileged screen.
- **A member of a workspace reaching something the workspace does not own**, or
  a non-administrator reaching an administrator's screen or route.
- Anything in the interface that lets one person act as another.

## What is not

- **The terminal being a shell on the machine.** That is what it is, on
  purpose, and `terminal: false` refuses it. See the
  [guide](https://orkcom-tech.github.io/cogitorium/guide/#terminal).
- **`sandbox: subprocess` running gears with the server's file access.** The
  server says so at startup and the interface says so on the gear screen; it is
  the documented cost of having no container runtime.
- **An administrator being able to do administrator things**, including
  approving code and reading named values.
- **A model saying something wrong.** That is not a vulnerability.
- Findings from a scanner with no path to impact described.

## Supported

The latest release. There is no long-term branch: fixes go out as a new version.
