// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

package alpine

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/internal/textwrap"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestAbuildGenerateFor(t *testing.T) {
	target := rebuild.Target{Ecosystem: rebuild.Alpine, Package: "v3.24/community/x86_64/kiota", Version: "1.32.5-r0", Artifact: "kiota-1.32.5-r0.apk"}
	env := rebuild.BuildEnv{TimewarpHost: "localhost:8080"}
	base := func() *Abuild {
		return &Abuild{
			Archive: "archive-bucket",
			Branch:  "v3.24",
			Repo:    "community",
			Arch:    "x86_64",
			Origin:  "kiota",
			Builder: "build-3-24-x86_64",
			Repositories: []UploadRef{
				{Repo: "main", Time: time.Date(2026, 9, 25, 13, 5, 35, 0, time.UTC)},
				{Repo: "community", Time: time.Date(2026, 9, 25, 14, 45, 26, 0, time.UTC)},
			},
			Overrides: []Override{
				{Name: "dotnet10-sdk", Version: "10.0.112-r0", Repo: "community", Upload: time.Date(2026, 9, 25, 15, 28, 5, 0, time.UTC)},
				{Name: "dotnet10-runtime", Version: "10.0.12-r0", Repo: "community", Upload: time.Date(2026, 9, 25, 15, 28, 5, 0, time.UTC)},
			},
			Baseline:        []string{"git", "pigz"},
			Requested:       []string{"build-base", "dotnet10-sdk>=10"},
			AportsCommit:    "740f5577b9f976333fefa4b32b3292c0a382941b",
			Commit:          "0403b844be5baff0dbfcae3a98194230c6862bff",
			SourceDateEpoch: 1790349605,
			Packager:        "Buildozer <alpine-devel@lists.alpinelinux.org>",
		}
	}
	t.Run("Full", func(t *testing.T) {
		inst, err := base().GenerateFor(target, env)
		if err != nil {
			t.Fatalf("GenerateFor() = %v", err)
		}
		want := rebuild.Instructions{
			Source: textwrap.Dedent(`
				mkdir -p /home/buildozer/aports
				cd /home/buildozer/aports
				git init -q
				git remote add origin https://github.com/alpinelinux/aports.git
				git fetch -q --depth 1 origin 740f5577b9f976333fefa4b32b3292c0a382941b
				git checkout -q FETCH_HEAD`)[1:],
			Deps: textwrap.Dedent(`
				cat > /etc/apk/repositories <<'EOF'
				http://alpine:2026-09-25T13:05:35Z@localhost:8080/archive-bucket/v3.24/main
				http://alpine:2026-09-25T14:45:26Z@localhost:8080/archive-bucket/v3.24/community
				@u1790350085 http://alpine:2026-09-25T15:28:05Z@localhost:8080/archive-bucket/v3.24/community
				EOF
				apk update
				apk upgrade -a
				apk add 'alpine-sdk' 'pigz' 'alpine-base' 'ifupdown-ng'
				apk add 'git' 'pigz' || for p in 'git' 'pigz'; do apk add "$p" || echo "baseline package $p skipped"; done
				apk add 'dotnet10-sdk@u1790350085=10.0.112-r0' 'dotnet10-runtime@u1790350085=10.0.12-r0'
				apk add --virtual .makedepends-kiota 'build-base' 'dotnet10-sdk>=10'
				mkdir -p /home/buildozer/packages /home/buildozer/.abuild /var/cache/distfiles
				adduser -D buildozer
				addgroup buildozer abuild
				chown -R buildozer:buildozer /home/buildozer
				chmod 0755 /home/buildozer /home/buildozer/aports /home/buildozer/packages /home/buildozer/.abuild
				chown root:abuild /var/cache/distfiles
				chmod 0775 /var/cache/distfiles
				su buildozer -c 'HOME=/home/buildozer abuild-keygen -a -n -q'
				cp /home/buildozer/.abuild/*.rsa.pub /etc/apk/keys/
				cat > /home/buildozer/abuild.env <<'EOF'
				export HOME=/home/buildozer REPODEST=/home/buildozer/packages
				export PACKAGER='Buildozer <alpine-devel@lists.alpinelinux.org>'
				export ABUILD_LAST_COMMIT=0403b844be5baff0dbfcae3a98194230c6862bff
				export SOURCE_DATE_EPOCH=1790349605
				export DISTFILES_MIRROR=https://distfiles.alpinelinux.org/distfiles/v3.24
				EOF
				su buildozer -c '. /home/buildozer/abuild.env && cd /home/buildozer/aports/community/kiota && abuild fetch'`)[1:],
			Build: textwrap.Dedent(`
				hostname build-3-24-x86_64
				sysctl -w net.ipv6.conf.lo.disable_ipv6=0
				su buildozer -c '. /home/buildozer/abuild.env && cd /home/buildozer/aports/community/kiota && abuild -d'
				cp /home/buildozer/packages/community/x86_64/kiota-1.32.5-r0.apk /src/`)[1:],
			OutputPath: "kiota-1.32.5-r0.apk",
			Requires:   rebuild.RequiredEnv{SystemDeps: []string{"git"}, Privileged: true},
		}
		if diff := cmp.Diff(want, inst); diff != "" {
			t.Errorf("GenerateFor() mismatch (-want +got):\n%s", diff)
		}
	})
	t.Run("NoOverridesNoPackager", func(t *testing.T) {
		b := base()
		b.Overrides, b.Baseline, b.Packager = nil, nil, ""
		inst, err := b.GenerateFor(target, env)
		if err != nil {
			t.Fatalf("GenerateFor() = %v", err)
		}
		want := textwrap.Dedent(`
			cat > /etc/apk/repositories <<'EOF'
			http://alpine:2026-09-25T13:05:35Z@localhost:8080/archive-bucket/v3.24/main
			http://alpine:2026-09-25T14:45:26Z@localhost:8080/archive-bucket/v3.24/community
			EOF
			apk update
			apk upgrade -a
			apk add 'alpine-sdk' 'pigz' 'alpine-base' 'ifupdown-ng'
			apk add --virtual .makedepends-kiota 'build-base' 'dotnet10-sdk>=10'
			mkdir -p`)[1:]
		if got := inst.Deps[:len(want)]; got != want {
			t.Errorf("Deps begins:\n%s\nwant:\n%s", got, want)
		}
		if !strings.Contains(inst.Deps, "\nunset PACKAGER\n") {
			t.Errorf("Deps does not unset PACKAGER:\n%s", inst.Deps)
		}
	})
	t.Run("Invalid", func(t *testing.T) {
		b := base()
		b.Repositories = nil
		if _, err := b.GenerateFor(target, env); err == nil {
			t.Error("GenerateFor() without repositories succeeded, want error")
		}
	})
}
