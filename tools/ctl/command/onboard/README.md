# Onboarding

Bringing a package into oss-rebuild coverage means two things: deciding it is
worth the capacity, and then spending capacity until it reproduces.
`ctl onboard priority` answers the first.

## Priority

There are far more packages than rebuild capacity, so something has to say
which ones matter. Priority is a per-package score fusing independent signals
of importance, each computed by its own offline job and each stored as a
per-ecosystem quantile so heavy tails and incomparable registry scales stay out
of the arithmetic.

| Signal | What it measures | Source |
|---------|------------------|--------|
| criticality | distinct packages that depend on this one | deps.dev dependency graph |

Criticality is computed at two granularities. The package number answers "is
this package worth onboarding at all". The per-version number answers "which of
its versions matters", which is a different question: the most-depended-upon
version is rarely the newest, since lockfiles pin, semver ranges settle, and old
majors keep their dependents for years.

Each job materializes the score onto the package's priority document after
updating its own signal, so readers get a number rather than recomputing a
formula, and jobs can land in any order without erasing each other.

```sh
# Read-only against public BigQuery data. Dump to inspect, load to apply.
ctl onboard priority criticality --project ssci-demos --out crit.json
ctl onboard priority criticality --project ssci-demos --load
```

Criticality is deliberately not the OSSF Criticality Score, which scores
repository activity. It measures blast radius only, because reverse-dependent
counts are the one importance signal published uniformly across every ecosystem
deps.dev covers. Download counts and repository statistics would serve a similar
role but are not available through all registries.
