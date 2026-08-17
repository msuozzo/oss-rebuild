# Prominence: what it measures and why it is shaped this way

Prominence exists to cover a hole in criticality. Reverse-dependent counts rank
libraries, and nothing else. An application is a leaf: `ripgrep`, `hugo`, and
`black` have essentially no dependents, so the graph scores them near zero no
matter how many people install them. Any coverage story that leans only on the
graph will systematically skip the software users actually run.

The obvious fixes do not generalize. Download counts exist for PyPI and npm but
not uniformly elsewhere, are trivially inflated by CI, and mean different things
per registry. Repository stars require a resolved, reachable repo and measure
GitHub demographics as much as software importance. Neither is available across
every ecosystem we rebuild, so neither can be the signal. What is available
everywhere is that a language model has already read the internet, and can say
how well known a name is.

## The construct

`p` measures **presence of the package in the model's training corpus**, which
is a proxy for public awareness. It is explicitly not:

- a measure of quality, security, or trustworthiness,
- a measure of current popularity, which for recent packages it cannot know,
- a judgment that the package deserves anything.

Keeping the construct narrow is what makes the rest of the design fall out.

## Why a decade-anchored rubric

Recognition is log-scaled: each decade of popularity rank holds roughly ten
times more packages. Asking for a 0-100 score invites the model to invent
precision it does not have and to cluster everything in the middle. Asking
instead for a rank decade ("top 10, top 100, top 1,000...") matches the shape of
the underlying distribution and makes the model's uncertainty legible: a package
it cannot place lands at 5, and a name it does not believe exists lands at 0.

Ties within a decade do not matter, because the score is consumed as a
per-ecosystem quantile and averaged with criticality. That is why there is one
temperature-0 call per package and no ensemble, no second opinion, and no
log-probability de-tie: they would refine a distinction the consumer discards.

## The horizon rule

A package registered after the model's knowledge cutoff cannot be something the
model knows. Whatever score it produces is inherited from a similarly named
neighbor or confabulated outright. So `p` is floored to 0 past a horizon `H`,
and the package rides criticality alone.

This is one line, costs nothing, cannot be faked, and it closes register-now
name-squatting on its own: a newly registered package cannot buy prominence
because it is not eligible for any. `H` is a per-model-generation constant and
must be re-measured when the model changes. Floored packages skip the API
entirely, and the floor is recomputed every run rather than cached, so advancing
`H` never leaves a stale floor behind.

## Why there is no anti-gaming stack

An earlier version of this work carried a four-stage filter: an activation
probe, an expected-value-over-logprobs readout, an era filter, and an
alias/publisher filter. All of it was cut, because the threat model did not
survive contact with the consumer.

**The consumer is a rebuild queue, not a trust decision.** Inflating a package's
score moves it up the queue and buys the attacker some wasted rebuild compute.
It buys nothing else: a rebuild either reproduces or it does not, and a high
score grants no attestation, no badge, and no ranking users see. Defending that
with a multi-stage filter stack is defending a prize that does not exist.

The activation probe was independently refuted: it learned the signature of the
fake-name generator used to train it rather than the concept of a nonexistent
package, so it scored well in evaluation and would have failed in the field.

Reinstate the stack the day this score gates trust. Until then, the horizon rule
plus a discrete readout is the whole mechanism.

## Two residuals, documented rather than solved

- **Post-horizon packages are ranked by the graph alone.** A genuinely prominent
  brand-new package rides criticality until `H` advances. Acceptable for a
  rebuild queue, since it is still enqueued, just not boosted, and it
  self-corrects on the next scoring pass with a newer model. It would not be
  acceptable if the score gated trust.
- **Criticality inherits the graph's staleness across renames.** A relocated
  package whose reverse-dependents still point at the old name gets a
  criticality that lags reality. Rename-aware resolution belongs to deps.dev
  edge canonicalization, not here. Prominence cannot fix it and does not try.

## Validating a change

`ctl onboard priority eval` scores a held-out confounder set of famous packages,
long-tail real ones, names that collide with common English words, and names
that do not exist. Run it after any change to the rubric or the model.

The main AUC is the metric of record. The false-positive and disambiguation
gates are trust-oriented and matter less here, but a regression in either is
evidence the rubric has drifted, so they are enforced too. Use `--dump` to see
which rows are responsible for a failure: the historical failures have all been
either testset mislabels or hallucination bait like `pytorch`, whose real PyPI
name is `torch`.
