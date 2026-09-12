package imagebuilder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const maxXRDPPackageBytes = 32 << 20

var xrdpChecksumLine = regexp.MustCompile(`\A([0-9a-f]{64})  xrdp\.deb\n\z`)

// This is an operator-installed build artifact, never an API-provided path or
// download URL. The sidecar binds bytes, not publisher trust: obtain both from
// the reviewed source build, not from an untrusted upload.
func verifyXRDPBundle(directory string) (string, error) {
	fail := errors.New("Debian xrdp bundle must contain a regular package and matching SHA-256 sidecar")
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return "", fail
	}
	read := func(name string, limit int64) ([]byte, error) {
		file := filepath.Join(directory, name)
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
			return nil, fail
		}
		stream, err := os.Open(file)
		if err != nil {
			return nil, fail
		}
		defer stream.Close()
		info, err = stream.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, fail
		}
		raw, err := io.ReadAll(io.LimitReader(stream, limit+1))
		if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
			return nil, fail
		}
		return raw, nil
	}
	checksum, err := read("xrdp-package.sha256", 128)
	if err != nil {
		return "", fail
	}
	match := xrdpChecksumLine.FindSubmatch(checksum)
	if len(match) != 2 {
		return "", fail
	}
	bytes, err := read("xrdp.deb", maxXRDPPackageBytes)
	if err != nil {
		return "", fail
	}
	digest := sha256.Sum256(bytes)
	actual := hex.EncodeToString(digest[:])
	if actual != string(match[1]) {
		return "", fail
	}
	return actual, nil
}
