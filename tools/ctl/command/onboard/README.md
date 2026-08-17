# Onboarding

Bringing a package into oss-rebuild coverage means two things: deciding it is
worth the capacity, and then spending capacity until it reproduces.
`ctl onboard priority` answers the first, `ctl onboard enqueue|round|status`
answers the second.

## Priority

There are far more packages than rebuild capacity, so something has to say
which ones matter. Priority is a per-package score fusing independent signals
of importance, each computed by its own offline job and each stored as a
per-ecosystem quantile so heavy tails and incomparable registry scales stay out
of the arithmetic.

| Signal | What it measures | Source |
|---------|------------------|--------|
| criticality | distinct packages that depend on this one | deps.dev dependency graph |
| prominence | how well known the package is by name | one Gemini call per package |

Criticality is computed at two granularities. The package number answers "is
this package worth onboarding at all". The per-version number answers "which of
its versions matters", which is a different question: the most-depended-upon
version is rarely the newest, since lockfiles pin, semver ranges settle, and old
majors keep their dependents for years.

Each job materializes the score onto the package's priority document after
updating its own signal, so readers get a number rather than recomputing a
formula, and jobs can land in any order without erasing each other.

```sh
# Criticality: read-only against public BigQuery data.
ctl onboard priority criticality --project ssci-demos --out crit.json
ctl onboard priority criticality --project ssci-demos --load

# Prominence: one cached model call per package. The corpus can be the export above.
ctl onboard priority prominence --project ssci-demos --corpus crit.json \
    --cache prom-cache.json --horizon 2025-06-01 --load

# Validate the scorer after changing the rubric or the model.
ctl onboard priority eval --project ssci-demos
```

The two signals cover each other rather than corroborating each other.
Criticality sees blast radius, including plumbing nobody has heard of, and it
correctly sinks deprecated names that are still famous. Prominence sees public
awareness, including the leaf applications no dependency graph can rank: an
application like ripgrep has no dependents no matter how many people install it.
Equal weight is the neutral prior, and a package carrying only one signal
scores at most half.

Criticality is deliberately not the OSSF Criticality Score, which scores
repository activity. It measures blast radius only, because reverse-dependent
counts are the one importance signal published uniformly across every ecosystem
deps.dev covers. Download counts and repository statistics would serve a similar
role but are not available through all registries, which is also why prominence
is elicited from a model rather than read off a registry. See
[PROMINENCE.md](PROMINENCE.md) for that argument in full, including the horizon
rule and why there is no anti-gaming filter stack.

## The queue

`enqueue` expands a package into one queue document per version, each recording
where that version sits on the escalation ladder. Every version starts at T1
(heuristic inference plus a build).

```sh
ctl onboard enqueue --project ssci-demos --ecosystem npm --package lodash
ctl onboard status --project ssci-demos
```

Admission is deliberate. A package can have hundreds of versions and the queue
is meant to be a working set, not a backlog of everything conceivable, so
`--max-versions` keeps only the top few. It admits by the same ordering the
queue is drained by, so a version that would never reach the front never enters
in the first place.

Each document carries two ordering terms rather than one score. `Score` is the
package's priority specialized to that version, preferring the version's own
criticality where deps.dev knows it and falling back to the package's.
`Freshness` is a recency boost, so a new release spikes and then decays into the
backlog. `DispatchOrder` multiplies them, which lets a fresh release of a
mid-tier package outrank a stale version of a critical one while keeping a fresh
version of a critical package above both.

Where no version criticality exists, every version of a package inherits the
same score and recency orders the back catalogue. That is the right default:
absent evidence about individual versions, prefer rebuilding more of a prominent
package.

State lives in Firestore so a run is resumable, and the collection needs no
composite indexes: it is small enough to scan whole and sort in memory. That
holds only because admission keeps it small.
