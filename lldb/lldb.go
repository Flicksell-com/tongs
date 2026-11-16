// Package lldb gets the debugger tongs drives: CodeLLDB's adapter and the lldb
// it bundles, downloaded once into the user's cache. Nothing installed on the
// machine is used.
package lldb

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pinned is the version tongs is tested with and uses unless another is chosen.
const Pinned = "1.12.3"

// The SHA-256 of Pinned's archive for each platform.
var sums = map[string]string{
	"darwin-arm64": "2f114a990e1b368dd1dbd33c80c0e719767af2d228391ec0df0571c957f9ac91",
	"darwin-x64":   "e25cc716b94c62c07fec268ff2785d2b797245b160502baef8b9c970a0c4d8e8",
	"linux-arm64":  "0887f67d440554617894266f80706b700907c36b95e6e49d23b95a0e05318101",
	"linux-x64":    "1cd7f386598022b51a5b93b9ffa23e812b23f519cfe1833384ec4bef4bfd1be1",
}

const (
	releases = "https://github.com/vadimcn/codelldb/releases/download"
	api      = "https://api.github.com/repos/vadimcn/codelldb/releases"
)

// platform is the host as CodeLLDB names it in its archives.
func platform() (string, error) {
	cpu := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	if cpu == "" || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		return "", fmt.Errorf("no debugger is published for %s %s", runtime.GOOS, runtime.GOARCH)
	}
	return runtime.GOOS + "-" + cpu, nil
}

// home is where the downloaded debuggers are kept.
func home() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, ".cache", "tongs", "codelldb")
}

// Version is the one in use: the one last chosen, else Pinned.
func Version() string {
	if raw, err := os.ReadFile(filepath.Join(home(), "version")); err == nil {
		if version := strings.TrimSpace(string(raw)); version != "" {
			return version
		}
	}
	return Pinned
}

func adapterIn(dir string) string { return filepath.Join(dir, "extension", "adapter", "codelldb") }

// Adapter is the path of the adapter to start, downloading the version in use
// if it isn't here yet. say is told what is going on while it downloads.
func Adapter(say func(string)) (string, error) {
	// For pointing tongs at a debugger of one's own.
	if path := os.Getenv("TONGS_ADAPTER"); path != "" {
		return path, nil
	}
	return Use(Version(), false, say)
}

// Use makes a version the one in use and returns its adapter, downloading it
// if it isn't here, or again if fresh.
func Use(version string, fresh bool, say func(string)) (string, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	plat, err := platform()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home(), version+"-"+plat)
	if _, err := os.Stat(adapterIn(dir)); err != nil || fresh {
		if err := download(version, plat, dir, say); err != nil {
			return "", fmt.Errorf("getting the debugger (CodeLLDB %s): %w", version, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home(), "version"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}
	return adapterIn(dir), nil
}

// Versions lists the releases that have an archive for this machine, newest
// first.
func Versions() ([]string, error) {
	plat, err := platform()
	if err != nil {
		return nil, err
	}
	var found []release
	if err := getJSON(api+"?per_page=40", &found); err != nil {
		return nil, err
	}
	var versions []string
	for _, r := range found {
		if _, ok := r.asset(plat); ok {
			versions = append(versions, strings.TrimPrefix(r.Tag, "v"))
		}
	}
	return versions, nil
}

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"` // "sha256:…", on releases GitHub has one for
	} `json:"assets"`
}

// asset is the SHA-256 GitHub gives for a platform's archive ("" if none),
// and whether the release has that archive at all.
func (r release) asset(plat string) (sum string, ok bool) {
	for _, a := range r.Assets {
		if a.Name == "codelldb-"+plat+".vsix" {
			return strings.TrimPrefix(a.Digest, "sha256:"), true
		}
	}
	return "", false
}

var client = &http.Client{Timeout: 30 * time.Minute}

func get(url string) (*http.Response, error) {
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "tongs")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, response.Status)
	}
	return response, nil
}

func getJSON(url string, into any) error {
	response, err := get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return json.NewDecoder(response.Body).Decode(into)
}

// download fetches a version's archive, checks it, and unpacks it into dir -
// whole or not at all, so a directory that is there is one that works.
func download(version, plat, dir string, say func(string)) error {
	// What the archive must hash to: compiled in for Pinned; for any other
	// version, what GitHub says, which guards against a broken download but
	// comes from the same place the archive does.
	want := ""
	if version == Pinned {
		want = sums[plat]
	} else {
		var r release
		if err := getJSON(api+"/tags/v"+version, &r); err != nil {
			return err
		}
		sum, ok := r.asset(plat)
		if !ok {
			return fmt.Errorf("that release has no archive for %s", plat)
		}
		want = sum
	}

	if err := os.MkdirAll(home(), 0o755); err != nil {
		return err
	}
	archive, err := os.CreateTemp(home(), "download-*.vsix")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()

	response, err := get(fmt.Sprintf("%s/v%s/codelldb-%s.vsix", releases, version, plat))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	say(fmt.Sprintf("downloading the debugger: CodeLLDB %s for %s, %d MB", version, plat, response.ContentLength>>20))
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(archive, hash), &progress{from: response.Body, total: response.ContentLength, say: say})
	if err != nil {
		return err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	switch {
	case want == "":
		say("this release publishes no checksum; the download was not verified")
	case got != want:
		return fmt.Errorf("the download is not what was expected (sha256 %s, wanted %s)", got, want)
	}

	part := dir + ".part"
	os.RemoveAll(part)
	defer os.RemoveAll(part)
	if err := unpack(archive, size, part); err != nil {
		return err
	}
	if _, err := os.Stat(adapterIn(part)); err != nil {
		return errors.New("the archive has no adapter in it; this version is laid out differently")
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Rename(part, dir); err != nil {
		return err
	}
	say("the debugger is ready")
	return nil
}

// progress reports each tenth of a download as it is read.
type progress struct {
	from        io.Reader
	total, read int64
	told        int64
	say         func(string)
}

func (p *progress) Read(buf []byte) (int, error) {
	n, err := p.from.Read(buf)
	p.read += int64(n)
	if p.total > 0 {
		if tenth := p.read * 10 / p.total; tenth > p.told && tenth < 10 {
			p.told = tenth
			p.say(fmt.Sprintf("  %d%%", tenth*10))
		}
	}
	return n, err
}

// unpack writes the adapter and its lldb out of the archive into dir.
func unpack(archive *os.File, size int64, dir string) error {
	files, err := zip.NewReader(archive, size)
	if err != nil {
		return err
	}
	for _, f := range files.File {
		if !strings.HasPrefix(f.Name, "extension/adapter/") && !strings.HasPrefix(f.Name, "extension/lldb/") {
			continue
		}
		if !filepath.IsLocal(f.Name) {
			return fmt.Errorf("the archive holds a path that leaves it: %s", f.Name)
		}
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		mode := f.Mode()
		if mode.IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		from, err := f.Open()
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			target, err := io.ReadAll(from)
			from.Close()
			if err != nil {
				return err
			}
			if err := os.Symlink(string(target), path); err != nil {
				return err
			}
			continue
		}
		// The programs must run whether or not the archive kept their modes.
		perm := mode.Perm() | 0o644
		if f.Name == "extension/adapter/codelldb" || strings.HasPrefix(f.Name, "extension/lldb/bin/") {
			perm |= 0o111
		}
		to, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			from.Close()
			return err
		}
		_, err = io.Copy(to, from)
		from.Close()
		if closeErr := to.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
