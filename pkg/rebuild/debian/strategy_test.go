package debian

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

func TestDebianPackage(t *testing.T) {
	tests := []struct {
		name     string
		strategy *DebianPackage
		target   rebuild.Target
		env      rebuild.BuildEnv
		want     rebuild.Instructions
	}{
		{
			name: "StandardPackage",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.dsc",
					MD5: "abc123",
				},
				Orig: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.orig.tar.gz",
					MD5: "def456",
				},
				Debian: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.debian.tar.xz",
					MD5: "ghi789",
				},
				Requirements: []string{"build-dep1", "build-dep2"},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0-1",
				Artifact:  "pkg_1.0-1_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `set -eux
wget https://example.com/pkg_1.0-1.dsc
wget https://example.com/pkg_1.0.orig.tar.gz
wget https://example.com/pkg_1.0-1.debian.tar.xz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0-1.dsc")`,
				Deps: `set -eux
apt update
apt install -y build-dep1 build-dep2`,
				Build: `set -eux
cd */
debuild -b -uc -us`,
				SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				OutputPath: "pkg_1.0-1_amd64.deb",
			},
		},
		{
			name: "NativePackage",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.dsc",
					MD5: "abc123",
				},
				Native: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.tar.gz",
					MD5: "def456",
				},
				Requirements: []string{"build-dep1"},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0",
				Artifact:  "pkg_1.0_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `set -eux
wget https://example.com/pkg_1.0.dsc
wget https://example.com/pkg_1.0.tar.gz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0.dsc")`,
				Deps: `set -eux
apt update
apt install -y build-dep1`,
				Build: `set -eux
cd */
debuild -b -uc -us`,
				SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				OutputPath: "pkg_1.0_amd64.deb",
			},
		},
		{
			name: "BinaryOnlyRebuild",
			strategy: &DebianPackage{
				DSC: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.dsc",
					MD5: "abc123",
				},
				Orig: FileWithChecksum{
					URL: "https://example.com/pkg_1.0.orig.tar.gz",
					MD5: "def456",
				},
				Debian: FileWithChecksum{
					URL: "https://example.com/pkg_1.0-1.debian.tar.xz",
					MD5: "ghi789",
				},
			},
			target: rebuild.Target{
				Ecosystem: rebuild.Debian,
				Package:   "pkg",
				Version:   "1.0-1+b1",
				Artifact:  "pkg_1.0-1+b1_amd64.deb",
			},
			env: rebuild.BuildEnv{},
			want: rebuild.Instructions{
				Source: `set -eux
wget https://example.com/pkg_1.0-1.dsc
wget https://example.com/pkg_1.0.orig.tar.gz
wget https://example.com/pkg_1.0-1.debian.tar.xz

dpkg-source -x --no-check $(basename "https://example.com/pkg_1.0-1.dsc")`,
				Deps: `set -eux
apt update
apt install -y `,
				Build: `set -eux
cd */
debuild -b -uc -us
mv /src/pkg_1.0-1_amd64.deb /src/pkg_1.0-1+b1_amd64.deb`,
				SystemDeps: []string{"wget", "git", "build-essential", "fakeroot", "devscripts"},
				OutputPath: "pkg_1.0-1+b1_amd64.deb",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.strategy.GenerateFor(tc.target, tc.env)
			if err != nil {
				t.Fatalf("DebianPackage.GenerateFor() failed unexpectedly: %v", err)
			}
			if diff := cmp.Diff(got, tc.want); diff != "" {
				t.Errorf("DebianPackage.GenerateFor() returned diff (-got +want):\n%s", diff)
			}
		})
	}
}

func TestBinaryVersionRegex(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    bool
		wantMap map[string]string
	}{
		{
			name:  "StandardBinaryPackage",
			input: "pkg_1.0-1+b1_amd64.deb",
			want:  true,
			wantMap: map[string]string{
				"name":              "pkg",
				"nonbinary_version": "1.0-1",
				"arch":              "amd64",
			},
		},
		{
			name:    "NonBinaryPackage",
			input:   "pkg_1.0-1_amd64.deb",
			want:    false,
			wantMap: map[string]string{},
		},
		{
			name:    "InvalidFormat",
			input:   "invalid-package-name",
			want:    false,
			wantMap: map[string]string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			matches := binaryVersionRegex.FindStringSubmatch(tc.input)
			got := matches != nil
			if got != tc.want {
				t.Errorf("binaryVersionRegex.FindStringSubmatch(%q) = %v, want %v", tc.input, got, tc.want)
			}

			if got {
				gotMap := make(map[string]string)
				for i, name := range binaryVersionRegex.SubexpNames() {
					if i != 0 && name != "" {
						gotMap[name] = matches[i]
					}
				}
				if diff := cmp.Diff(gotMap, tc.wantMap); diff != "" {
					t.Errorf("binaryVersionRegex capture groups returned diff (-got +want):\n%s", diff)
				}
			}
		})
	}
}