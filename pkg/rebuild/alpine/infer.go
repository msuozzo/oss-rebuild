// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/oss-rebuild/internal/httpx"
	"github.com/google/oss-rebuild/internal/urlx"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
	alpinereg "github.com/google/oss-rebuild/pkg/registry/alpine"
	"github.com/google/oss-rebuild/pkg/registry/alpine/apk"
	"github.com/google/oss-rebuild/pkg/registry/alpine/snapshot"
	"github.com/pkg/errors"
)

// maxUploadLag bounds how long after a build inference looks for the
// uploads that list its packages or its dependencies. A builder uploads
// each repository at the end of its pass, normally within hours.
const maxUploadLag = 7 * 24 * time.Hour

// githubURL serves aports commits by abbreviated hash or ref.
var githubURL = urlx.MustParse(AportsRepo + "/commit/")

// Sources are where inference reads from.
type Sources struct {
	Archive snapshot.Archive
	// BuildLogs serves official logs the archive has not stored.
	BuildLogs alpinereg.BuildLogs
	// GitHub resolves abbreviated aports commits.
	GitHub httpx.BasicClient
	// GitHubURL overrides githubURL.
	GitHubURL *url.URL
}

// InferStrategy reconstructs the official build of t.
func (Rebuilder) InferStrategy(ctx context.Context, t rebuild.Target, mux rebuild.RegistryMux, _ *rebuild.RepoConfig, _ rebuild.Strategy) (rebuild.Strategy, error) {
	repo, _, err := ParsePackage(t.Package)
	if err != nil {
		return nil, err
	}
	// TODO: Support other architectures once builds can select their
	// platform.
	if repo.Arch != "x86_64" {
		return nil, errors.Errorf("unsupported arch %s: builds run on x86_64", repo.Arch)
	}
	return Infer(ctx, t, Sources{
		Archive:   mux.Alpine,
		BuildLogs: alpinereg.BuildLogs{Client: mux.Alpine.Client, URL: alpinereg.BuildLogHost},
		GitHub:    mux.Alpine.Client,
	})
}

// Infer reconstructs the official build of t from the snapshot archive.
//
// The official log gives the build's start T and every package its
// dependency installation added. The builder's repositories are, per
// repository, the newest upload at or before T. A package the log installed
// that those uploads do not list comes from the builder's own builds since
// its last upload, and is installed from the upload that first listed it.
func Infer(ctx context.Context, t rebuild.Target, src Sources) (*Abuild, error) {
	repo, name, err := ParsePackage(t.Package)
	if err != nil {
		return nil, err
	}
	if want := apk.Filename(name, t.Version); t.Artifact != want {
		return nil, errors.Errorf("artifact %s, want %s", t.Artifact, want)
	}
	bucket, err := src.Archive.Bucket()
	if err != nil {
		return nil, err
	}
	resp, err := src.Archive.Open(ctx, snapshot.PackageObject(repo, t.Artifact))
	if err != nil {
		return nil, errors.Wrap(err, "fetching the published package")
	}
	info, err := apk.ReadPkgInfo(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, errors.Wrap(err, "reading the published package")
	}
	b := &Abuild{
		Archive:  bucket,
		Branch:   repo.Branch,
		Repo:     repo.Repo,
		Arch:     repo.Arch,
		Origin:   info.Get("origin"),
		Builder:  repo.Builder(),
		Baseline: Baseline(repo),
		Commit:   info.Get("commit"),
	}
	if b.Origin == "" {
		b.Origin = name
	}
	if b.Commit == "" {
		return nil, errors.New("the published package records no commit")
	}
	if b.SourceDateEpoch, err = strconv.ParseInt(info.Get("builddate"), 10, 64); err != nil {
		return nil, errors.Wrap(err, "parsing builddate")
	}
	if p := info.Get("packager"); p != "" && p != "Unknown" {
		b.Packager = p
	}
	l, err := officialLog(ctx, src, repo, b.Origin, t.Version)
	if err != nil {
		return nil, err
	}
	if l.Started.IsZero() {
		return nil, errors.New("the official log has no start time")
	}
	b.Requested = l.Requested
	uploads, err := src.Archive.Uploads(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetching uploads")
	}
	idx := &indexes{archive: src.Archive, byHash: map[string]map[string]bool{}}
	deps := dependencyRepos(repo.Branch, repo.Repo)
	base := map[string]snapshot.Upload{}
	for _, r := range repositories(repo.Branch) {
		rr := alpinereg.Repository{Branch: repo.Branch, Repo: r, Arch: repo.Arch}
		up, ok := uploads.AtOrBefore(rr, l.Started)
		if !ok {
			if slices.Contains(deps, r) {
				return nil, errors.Errorf("the archive has no upload of %s from before the build (%s)", rr, l.Started.Format(time.RFC3339))
			}
			// The build cannot depend on it and the log pins what it installed.
			continue
		}
		base[r] = up
		b.Repositories = append(b.Repositories, UploadRef{Repo: r, Time: up.Time()})
	}
	for _, nv := range l.Installed {
		n, v := apk.SplitNameVersion(nv)
		o, err := override(ctx, idx, uploads, base, deps, n, v)
		if err != nil {
			return nil, errors.Wrapf(err, "locating %s", nv)
		}
		if o != nil {
			b.Overrides = append(b.Overrides, *o)
		}
	}
	b.AportsCommit, err = aportsCommit(ctx, src, idx, uploads, repo, name, t.Version, l.Started)
	if err != nil {
		return nil, err
	}
	if b.AportsCommit == "" {
		b.AportsCommit = b.Commit
	}
	return b, nil
}

// buildLog is what inference reads from an official build log.
type buildLog struct {
	Started   time.Time
	Requested []string // dependency specs abuild installed
	Installed []string // name-version of each package the installation added or moved
}

var (
	logStartRegex   = regexp.MustCompile(`^>>> \S+: Building \S+ \S+ \(using abuild \S+\) started (.+)$`)
	logRequestRegex = regexp.MustCompile(`^>>> \S+: Installing for build: (.*)$`)
	logInstallRegex = regexp.MustCompile(`^\(\s*\d+/\d+\) Installing (\S+) \(([^)]+)\)$`)
	logUpgradeRegex = regexp.MustCompile(`^\(\s*\d+/\d+\) (?:Upgrading|Downgrading|Replacing) (\S+) \([^)]*? -> ([^)]+)\)$`)
)

// makedepends prefixes abuild's virtual package for a build's dependencies.
const makedepends = ".makedepends-"

// parseLog parses the lines of a build log that describe its environment.
func parseLog(text string) *buildLog {
	l := &buildLog{}
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := logStartRegex.FindStringSubmatch(line); m != nil && l.Started.IsZero() {
			if t, err := mail.ParseDate(m[1]); err == nil {
				l.Started = t.UTC()
			}
		} else if m := logRequestRegex.FindStringSubmatch(line); m != nil {
			l.Requested = strings.Fields(m[1])
		} else if m := logInstallRegex.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], makedepends) {
			l.Installed = append(l.Installed, m[1]+"-"+m[2])
		} else if m := logUpgradeRegex.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], makedepends) {
			l.Installed = append(l.Installed, m[1]+"-"+m[2])
		}
	}
	return l
}

// officialLog reads the build's log from the archive, else from the build
// log host.
func officialLog(ctx context.Context, src Sources, repo alpinereg.Repository, origin, version string) (*buildLog, error) {
	text, err := src.Archive.Read(ctx, snapshot.LogObject(repo.Builder(), repo.Repo, origin, version))
	if alpinereg.IsNotFound(err) {
		text, err = src.BuildLogs.Get(ctx, repo.Builder(), repo.Repo, origin, version)
	}
	if err != nil {
		return nil, errors.Wrap(err, "fetching the official log")
	}
	return parseLog(string(text)), nil
}

// indexes lists the packages of uploads, fetching each index once.
type indexes struct {
	archive snapshot.Archive
	byHash  map[string]map[string]bool // "name-version" listed, by index SHA-256
}

func (x *indexes) lists(ctx context.Context, up snapshot.Upload, name, version string) (bool, error) {
	listed, ok := x.byHash[up.SHA256]
	if !ok {
		b, err := x.archive.Read(ctx, snapshot.BlobObject(up.SHA256))
		if err != nil {
			return false, errors.Wrap(err, "fetching index")
		}
		idx, err := apk.ParseIndex(bytes.NewReader(b))
		if err != nil {
			return false, err
		}
		listed = map[string]bool{}
		for _, r := range idx.Records {
			listed[r.Name()+"-"+r.Version()] = true
		}
		x.byHash[up.SHA256] = listed
	}
	return listed[name+"-"+version], nil
}

// after returns the uploads of repos after t and within maxUploadLag of
// it, oldest first.
func after(uploads snapshot.Uploads, branch, arch string, repos []string, t time.Time) []snapshot.Upload {
	var out []snapshot.Upload
	for _, up := range uploads {
		if up.Branch == branch && up.Arch == arch && slices.Contains(repos, up.Repo) && up.Time().After(t) && up.Time().Before(t.Add(maxUploadLag)) {
			out = append(out, up)
		}
	}
	slices.SortStableFunc(out, func(a, b snapshot.Upload) int { return cmp.Compare(a.LastModified, b.LastModified) })
	return out
}

// override returns nil if a base upload lists name at version, else the
// override that installs it from the first later upload listing it.
func override(ctx context.Context, idx *indexes, uploads snapshot.Uploads, base map[string]snapshot.Upload, deps []string, name, version string) (*Override, error) {
	var earliest time.Time
	var branch, arch string
	for _, r := range deps {
		up, ok := base[r]
		if !ok {
			continue
		}
		branch, arch = up.Branch, up.Arch
		if listed, err := idx.lists(ctx, up, name, version); err != nil {
			return nil, err
		} else if listed {
			return nil, nil
		}
		if earliest.IsZero() || up.Time().Before(earliest) {
			earliest = up.Time()
		}
	}
	for _, up := range after(uploads, branch, arch, deps, earliest) {
		if b, ok := base[up.Repo]; !ok || !up.Time().After(b.Time()) {
			continue
		}
		if listed, err := idx.lists(ctx, up, name, version); err != nil {
			return nil, err
		} else if listed {
			return &Override{Name: name, Version: version, Repo: up.Repo, Upload: up.Time()}, nil
		}
	}
	return nil, errors.New("no archived upload lists it")
}

// describeRegex matches `git describe` output, capturing the abbreviated
// commit.
var describeRegex = regexp.MustCompile(`-g([0-9a-f]{7,40})$`)

// aportsCommit finds the aports commit the builder had checked out. The
// builder writes `git describe` of its checkout into each index it
// uploads, and the first upload of the build's repository that lists the
// package comes at the end of the build's pass. It returns "" when no
// upload describes a commit.
//
// NOTE: If the pass that built the package failed to upload, the listing
// upload comes from a later pass, whose checkout may be newer.
func aportsCommit(ctx context.Context, src Sources, idx *indexes, uploads snapshot.Uploads, repo alpinereg.Repository, name, version string, start time.Time) (string, error) {
	for _, up := range after(uploads, repo.Branch, repo.Arch, []string{repo.Repo}, start) {
		listed, err := idx.lists(ctx, up, name, version)
		if err != nil {
			return "", err
		}
		if !listed {
			continue
		}
		m := describeRegex.FindStringSubmatch(up.Description)
		if m == nil {
			return "", nil
		}
		return resolveCommit(ctx, src, m[1])
	}
	return "", nil
}

// patchFromRegex matches the first line of a commit in `git format-patch`
// form, which GitHub serves for any ref.
var patchFromRegex = regexp.MustCompile(`^From ([0-9a-f]{40}) `)

// resolveCommit expands an abbreviated aports commit. Git fetches only by
// full hash.
func resolveCommit(ctx context.Context, src Sources, abbrev string) (string, error) {
	base := githubURL
	if src.GitHubURL != nil {
		base = src.GitHubURL
	}
	u := base.JoinPath(abbrev + ".patch").String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := src.GitHub.Do(req)
	if err != nil {
		return "", errors.Wrapf(err, "resolving aports commit %s", abbrev)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf("resolving aports commit %s: %s", abbrev, resp.Status)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	m := patchFromRegex.FindStringSubmatch(line)
	if m == nil {
		return "", errors.Errorf("resolving aports commit %s: unexpected response", abbrev)
	}
	return m[1], nil
}
