// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/flow"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	"github.com/pkg/errors"
)

// UploadRef is a repository as one upload published it.
type UploadRef struct {
	Repo string    `json:"repo" yaml:"repo"`
	Time time.Time `json:"time" yaml:"time"` // the upload's Last-Modified
}

// Override is a package the builder had installed that its base
// repositories did not list: one its own earlier builds had just produced,
// or one a later upload listed after a failed upload. It is installed from
// the upload that first listed it.
type Override struct {
	Name    string    `json:"name" yaml:"name"`
	Version string    `json:"version" yaml:"version"`
	Repo    string    `json:"repo" yaml:"repo"`
	Upload  time.Time `json:"upload" yaml:"upload"`
}

// Tag is the apk repository tag of the override's upload.
func (o Override) Tag() string { return "u" + strconv.FormatInt(o.Upload.Unix(), 10) }

// Abuild rebuilds an Alpine package the way Alpine's official builder does:
// abuild run by the buildozer user in the builder's environment as of the
// build's start, reconstructed from a snapshot archive.
type Abuild struct {
	// Archive is the GCS bucket holding the snapshot archive that timewarp
	// serves repositories from.
	Archive string `json:"archive" yaml:"archive"`
	Branch  string `json:"branch" yaml:"branch"`
	Repo    string `json:"repo" yaml:"repo"`
	Arch    string `json:"arch" yaml:"arch"`
	Origin  string `json:"origin" yaml:"origin"` // the APKBUILD's directory name
	// Builder is the official builder's hostname, which builds see.
	Builder string `json:"builder" yaml:"builder"`
	// Repositories are the builder's repositories as of the build's start.
	Repositories []UploadRef `json:"repositories" yaml:"repositories"`
	Overrides    []Override  `json:"overrides,omitempty" yaml:"overrides,omitempty"`
	// Baseline is what the builder had installed apart from the build's
	// dependencies.
	Baseline []string `json:"baseline,omitempty" yaml:"baseline,omitempty"`
	// Requested are the dependencies abuild installed, as the official log
	// lists them.
	Requested []string `json:"requested,omitempty" yaml:"requested,omitempty"`
	// AportsCommit is the aports commit the builder had checked out.
	AportsCommit string `json:"aports_commit" yaml:"aports_commit"`
	// Commit is the last commit to the APKBUILD's directory, which abuild
	// records in the package.
	Commit          string `json:"commit" yaml:"commit"`
	SourceDateEpoch int64  `json:"source_date_epoch" yaml:"source_date_epoch"`
	Packager        string `json:"packager,omitempty" yaml:"packager,omitempty"`
}

var _ rebuild.Strategy = &Abuild{}

// timewarpRepo is the apk repository URL of repo as the given upload
// published it. The URL is a template that the build environment resolves.
func (b *Abuild) timewarpRepo(t time.Time, repo string) string {
	return fmt.Sprintf(`{{.BuildEnv.TimewarpURLFromString "alpine" %q}}/%s/%s/%s`, t.UTC().Format(time.RFC3339), b.Archive, b.Branch, repo)
}

// repositories are the lines of /etc/apk/repositories: the base
// repositories and a tagged repository per upload that overrides come from.
func (b *Abuild) repositories() string {
	var lines []string
	for _, r := range b.Repositories {
		lines = append(lines, b.timewarpRepo(r.Time, r.Repo))
	}
	for _, o := range b.Overrides {
		if l := "@" + o.Tag() + " " + b.timewarpRepo(o.Upload, o.Repo); !slices.Contains(lines, l) {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// shellWords quotes each word for sh.
func shellWords(words []string) string {
	var q []string
	for _, w := range words {
		q = append(q, "'"+strings.ReplaceAll(w, "'", `'\''`)+"'")
	}
	return strings.Join(q, " ")
}

func (b *Abuild) validate() error {
	switch {
	case b.Archive == "" || b.Branch == "" || b.Repo == "" || b.Arch == "" || b.Origin == "" || b.Builder == "":
		return errors.New("archive, branch, repo, arch, origin and builder are required")
	case len(b.Repositories) == 0:
		return errors.New("no repositories")
	case b.AportsCommit == "" || b.Commit == "":
		return errors.New("aports_commit and commit are required")
	}
	return nil
}

// ToWorkflow expresses the build as workflow steps: the aports checkout, then
// the builder's environment installed through timewarp, then abuild.
func (b *Abuild) ToWorkflow() *rebuild.WorkflowStrategy {
	var overrides []string
	for _, o := range b.Overrides {
		overrides = append(overrides, o.Name+"@"+o.Tag()+"="+o.Version)
	}
	packager := "unset PACKAGER"
	if b.Packager != "" {
		packager = "export PACKAGER=" + shellWords([]string{b.Packager})
	}
	return &rebuild.WorkflowStrategy{
		Source: []flow.Step{{
			Uses: "alpine/fetch/aports",
			With: map[string]string{"commit": b.AportsCommit},
		}},
		Deps: []flow.Step{
			{
				Uses: "alpine/deps/install",
				With: map[string]string{
					"repositories": b.repositories(),
					"always":       shellWords(alwaysInstall),
					"baseline":     shellWords(b.Baseline),
					"overrides":    shellWords(overrides),
					"origin":       b.Origin,
					"requested":    shellWords(b.Requested),
				},
			},
			{
				Uses: "alpine/deps/builder",
				With: map[string]string{
					"packager":        packager,
					"commit":          b.Commit,
					"sourceDateEpoch": strconv.FormatInt(b.SourceDateEpoch, 10),
					"branch":          b.Branch,
					"repo":            b.Repo,
					"origin":          b.Origin,
				},
			},
		},
		Build: []flow.Step{{
			Uses: "alpine/build/abuild",
			With: map[string]string{
				"builder":  b.Builder,
				"repo":     b.Repo,
				"arch":     b.Arch,
				"origin":   b.Origin,
				"artifact": "{{.Target.Artifact}}",
			},
		}},
		// NOTE: The build sets the builder's hostname and enables IPv6 on the
		// loopback interface, as official builders run, which needs a
		// privileged container.
		Requires: rebuild.RequiredEnv{Privileged: true},
	}
}

// GenerateFor generates the instructions for an Abuild.
func (b *Abuild) GenerateFor(t rebuild.Target, be rebuild.BuildEnv) (rebuild.Instructions, error) {
	if err := b.validate(); err != nil {
		return rebuild.Instructions{}, errors.Wrap(err, "validating alpine strategy")
	}
	return b.ToWorkflow().GenerateFor(t, be)
}

func init() {
	for _, t := range toolkit {
		flow.Tools.MustRegister(t)
	}
}

// home is the build user's home. Builds record paths below it, so it must
// match the official builders'.
const home = "/home/buildozer"

var toolkit = []*flow.Tool{
	{
		// Only the builder's checkout is fetched: aports is too large to clone.
		// abuild takes the APKBUILD's last commit from the environment, but
		// some builds record the checkout's HEAD.
		Name: "alpine/fetch/aports",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				mkdir -p ` + home + `/aports
				cd ` + home + `/aports
				git init -q
				git remote add origin ` + AportsRepo + `.git
				git fetch -q --depth 1 origin {{.With.commit}}
				git checkout -q FETCH_HEAD`)[1:],
			Needs: []string{"git"},
		}},
	},
	{
		// The builder's packages as of the build's start: the base
		// repositories' packages upgraded or downgraded to their versions,
		// what every builder has, the builder's baseline, the overrides, and
		// the build's dependencies in abuild's virtual package. Baseline
		// packages missing from the repositories are skipped.
		Name: "alpine/deps/install",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				cat > /etc/apk/repositories <<'EOF'
				{{.With.repositories}}
				EOF
				apk update
				apk upgrade -a
				apk add {{.With.always}}
				{{- if .With.baseline}}
				apk add {{.With.baseline}} || for p in {{.With.baseline}}; do apk add "$p" || echo "baseline package $p skipped"; done
				{{- end}}
				{{- if .With.overrides}}
				apk add {{.With.overrides}}
				{{- end}}
				{{- if .With.requested}}
				apk add --virtual .makedepends-{{.With.origin}} {{.With.requested}}
				{{- end}}`)[1:],
		}},
	},
	{
		// The build user as on the builder, with its signing key and the
		// sources fetched while the network is certain to be available.
		// NOTE: busybox adduser gives the home directory mode 2755, which
		// builders do not have and which propagates setgid below it.
		Name: "alpine/deps/builder",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				mkdir -p ` + home + `/packages ` + home + `/.abuild /var/cache/distfiles
				adduser -D buildozer
				addgroup buildozer abuild
				chown -R buildozer:buildozer ` + home + `
				chmod 0755 ` + home + ` ` + home + `/aports ` + home + `/packages ` + home + `/.abuild
				chown root:abuild /var/cache/distfiles
				chmod 0775 /var/cache/distfiles
				su buildozer -c 'HOME=` + home + ` abuild-keygen -a -n -q'
				cp ` + home + `/.abuild/*.rsa.pub /etc/apk/keys/
				cat > ` + home + `/abuild.env <<'EOF'
				export HOME=` + home + ` REPODEST=` + home + `/packages
				{{.With.packager}}
				export ABUILD_LAST_COMMIT={{.With.commit}}
				export SOURCE_DATE_EPOCH={{.With.sourceDateEpoch}}
				export DISTFILES_MIRROR=https://distfiles.alpinelinux.org/distfiles/{{.With.branch}}
				EOF
				su buildozer -c '. ` + home + `/abuild.env && cd ` + home + `/aports/{{.With.repo}}/{{.With.origin}} && abuild fetch'`)[1:],
		}},
	},
	{
		// Dependencies are installed, so abuild runs without dependency
		// handling (-d) and needs no repository.
		Name: "alpine/build/abuild",
		Steps: []flow.Step{{
			Runs: textwrap.Dedent(`
				hostname {{.With.builder}}
				sysctl -w net.ipv6.conf.lo.disable_ipv6=0
				su buildozer -c '. ` + home + `/abuild.env && cd ` + home + `/aports/{{.With.repo}}/{{.With.origin}} && abuild -d'
				cp ` + home + `/packages/{{.With.repo}}/{{.With.arch}}/{{.With.artifact}} /src/`)[1:],
		}},
	},
}
