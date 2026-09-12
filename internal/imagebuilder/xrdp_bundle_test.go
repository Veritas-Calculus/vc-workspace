package imagebuilder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestXRDPBundle(t *testing.T, payload []byte) string {
	t.Helper()
	root := t.TempDir()
	writeTestXRDPBundle(t, root, payload)
	return root
}

func writeTestXRDPBundle(t *testing.T, root string, payload []byte) {
	t.Helper()
	for name, contents := range map[string][]byte{
		"xrdp.deb":            payload,
		"xrdp-package.sha256": []byte(fmt.Sprintf("%x  xrdp.deb\n", sha256.Sum256(payload))),
	} {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestXRDPBundleRequiresExactLocalBytesAndCanonicalChecksum(t *testing.T) {
	payload := []byte("package fixture, not a Debian installation test")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	for _, test := range []struct{ name, checksum string }{
		{"uppercase", strings.ToUpper(digest) + "  xrdp.deb\n"},
		{"alternate name", digest + "  another.deb\n"},
		{"traversal", digest + "  ../xrdp.deb\n"},
		{"extra entry", digest + "  xrdp.deb\n" + digest + "  xrdp.deb\n"},
		{"missing newline", digest + "  xrdp.deb"},
		{"wrong bytes", strings.Repeat("0", 64) + "  xrdp.deb\n"},
		{"too large", strings.Repeat("x", 129)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := makeTestXRDPBundle(t, payload)
			if err := os.WriteFile(filepath.Join(root, "xrdp-package.sha256"), []byte(test.checksum), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := verifyXRDPBundle(root); err == nil {
				t.Fatal("accepted an unbound package")
			}
		})
	}
	root := makeTestXRDPBundle(t, payload)
	if actual, err := verifyXRDPBundle(root); err != nil || actual != digest {
		t.Fatal("valid bundle rejected", err)
	}
}

func TestXRDPBundleRejectsMissingEmptyOversizedAndLinkedArtifacts(t *testing.T) {
	for _, name := range []string{"xrdp.deb", "xrdp-package.sha256"} {
		for _, mode := range []string{"absent", "empty", "directory", "symlink", "oversized"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				root := makeTestXRDPBundle(t, []byte("fixture"))
				file := filepath.Join(root, name)
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "empty":
					if err := os.WriteFile(file, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(file, 0o700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					other := filepath.Join(makeTestXRDPBundle(t, []byte("fixture")), name)
					if err := os.Symlink(other, file); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					stream, err := os.Create(file)
					if err != nil {
						t.Fatal(err)
					}
					err = stream.Truncate(maxXRDPPackageBytes + 1)
					stream.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := verifyXRDPBundle(root); err == nil {
					t.Fatal("unsafe artifact accepted")
				}
			})
		}
	}
}

func TestDebianBuildRejectsChangedBundleBeforeLaunchingPacker(t *testing.T) {
	for _, updateSidecar := range []bool{false, true} {
		t.Run(fmt.Sprint(updateSidecar), func(t *testing.T) {
			builder := testBuilder(t)
			if updateSidecar {
				writeTestXRDPBundle(t, builder.config.LinuxXRDPBundle, []byte("replacement package"))
			} else if err := os.WriteFile(filepath.Join(builder.config.LinuxXRDPBundle, "xrdp.deb"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := builder.Build(context.Background(), validDebianRequest(), nil); err == nil || !strings.Contains(err.Error(), "bundle changed") {
				t.Fatal("changed artifact must fail before Packer", err)
			}
		})
	}
}

func TestBuilderRequiresXRDPBundleAndDoesNotPassItToWindows(t *testing.T) {
	builder := testBuilder(t)
	config := builder.config
	config.LinuxXRDPBundle = ""
	if _, err := New(config); err == nil {
		t.Fatal("missing package accepted")
	}
	spec, err := builder.commandSpec(validWindowsRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range spec.env {
		if strings.HasPrefix(entry, "PKR_VAR_xrdp_") {
			t.Fatal("Linux runtime passed to Windows")
		}
	}
}
