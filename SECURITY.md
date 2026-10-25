# Security policy

`kube-outlier` is built around two design rules: it never writes to a cluster, and it
never prints or stores a Secret value. A way to break either is a security issue.

What it does, for anyone deciding whether to allow it on production clusters:

- **Reads** API discovery and metadata-only lists (names, labels, annotations,
  `managedFields`) of the types in scope; with `--compare-content`, also full objects of
  the `content.kinds` types. It drops the value of the
  `kubectl.kubernetes.io/last-applied-configuration` annotation as soon as it reads it.
  The access it needs, and a role that can't read Secrets, are in
  [docs/permissions.md](docs/permissions.md).
- **Never writes:** its HTTP client refuses any request that is not a GET or HEAD.
- **Sends nothing anywhere** except requests to the API servers in your kubeconfig, and
  whatever your kubeconfig's login method contacts (an exec credential plugin or an OIDC
  provider), exactly as with `kubectl`. There is no telemetry and no update check.
- **Writes one file,** the baseline you name with `baseline generate --out`. A baseline
  holds no values, but it does list cluster names and the kind, namespace and name of
  every resource in scope.

## Reporting a vulnerability

Please report privately through GitHub:
**[Report a vulnerability](https://github.com/vibe-shift-coding/kube-outlier/security/advisories/new)**
(the repository's **Security** tab → **Report a vulnerability**). Do not open a public
issue.

Include the version (`kube-outlier version`), the command you ran, and what you
observed. Remove real Secret values and cluster names from anything you attach.

Reports are handled on a best-effort basis by the maintainers. You'll get a reply once
the report has been looked at, and we'll work with you on a fix and on when to disclose
it publicly.

## In scope

- Any request that could change cluster state (anything but GET or HEAD), or a way to
  bypass `scripts/check-readonly.sh` or the read-only HTTP transport.
- A Secret value (or a value of a `sensitive.kinds` kind), or anything that can be
  checked against a guessed value, reaching a report, `--why` output, a baseline, or
  a log.
- Any network request to a destination other than the API servers and login methods in
  the kubeconfig.
- Vulnerabilities in dependencies that the tool's code actually reaches
  (`govulncheck ./...` runs in CI).

## Supported versions

Security fixes are made in the latest release; earlier releases are not patched.
