// Package update tells when a newer tongs has been released, and puts it in
// place of the one running.
//
// A release is a tag like v1.2.3 on GitHub with a tongs built for each
// platform attached. A tongs built from the source as it stood, with no such
// version, is never offered one.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/Flicksell-com/tongs/fetch"
)

const repo = "Flicksell-com/tongs"

// version is set when a release is built (-ldflags -X); `go install` of a
// tagged version records it in the build's own information instead.
var version string

// Version is the version of this tongs: v1.2.3 for a release, otherwise
// whatever Go recorded of the source it was built from.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(unknown)"
}

var release = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// Released says whether this tongs is a release, and so can be updated.
func Released() bool { return release.MatchString(Version()) }

// newer says whether release a comes after release b.
func newer(a, b string) bool {
	x, y := release.FindStringSubmatch(a), release.FindStringSubmatch(b)
	if x == nil || y == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		p, _ := strconv.Atoi(x[i])
		q, _ := strconv.Atoi(y[i])
		if p != q {
			return p > q
		}
	}
	return false
}

// heard is what GitHub last said, kept so that it is asked once a day and
// not at every start.
type heard struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

func heardPath() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, ".cache", "tongs", "update.json")
}

// Newer is the release that is newer than this tongs, "" when there is none.
// With now it asks GitHub; without, it asks at most once a day and otherwise
// answers from what it last heard.
func Newer(now bool) (string, error) {
	if !Released() {
		return "", nil
	}
	var last heard
	if raw, err := os.ReadFile(heardPath()); err == nil {
		json.Unmarshal(raw, &last)
	}
	if now || time.Since(last.Checked) > 24*time.Hour {
		found, err := fetch.Latest(repo)
		if err != nil {
			return "", err
		}
		last = heard{time.Now(), found.Tag}
		if raw, err := json.Marshal(last); err == nil {
			os.MkdirAll(filepath.Dir(heardPath()), 0o755)
			os.WriteFile(heardPath(), raw, 0o644)
		}
	}
	if newer(last.Latest, Version()) {
		return last.Latest, nil
	}
	return "", nil
}

// Apply downloads a release's tongs for this machine, checks it, and puts it
// where the running one is. The one running goes on until it is quit; the
// next start is the new one.
func Apply(tag string, say func(string)) error {
	name := "tongs-" + runtime.GOOS + "-" + runtime.GOARCH
	found, err := fetch.Tagged(repo, tag)
	if err != nil {
		return err
	}
	// The checksum comes from GitHub, as the program does: it guards against
	// a broken download, not against a release that was tampered with.
	want, ok := found.Sum(name)
	switch {
	case !ok:
		return fmt.Errorf("%s has no tongs built for %s %s", tag, runtime.GOOS, runtime.GOARCH)
	case want == "":
		return fmt.Errorf("%s publishes no checksum for %s, so the download could not be checked", tag, name)
	}

	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		return err
	}
	// Beside the old one, so that the new one takes its place in one step.
	next, err := os.CreateTemp(filepath.Dir(self), ".tongs-*")
	if err != nil {
		return fmt.Errorf("tongs can't replace itself at %s: %w", self, err)
	}
	defer os.Remove(next.Name())
	got, _, err := fetch.File(repo, tag, name, "tongs "+tag, next, say)
	if closeErr := next.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("the download is not what was expected (sha256 %s, wanted %s)", got, want)
	}
	if err := os.Chmod(next.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(next.Name(), self); err != nil {
		return fmt.Errorf("tongs can't replace itself at %s: %w", self, err)
	}
	return nil
}

// Run is `tongs update`: it updates to the newest release if there is a
// newer one, and says what it did.
func Run(say func(string)) error {
	if !Released() {
		return errors.New("this tongs is " + Version() + ", built from source and not a release; build it again to update it")
	}
	tag, err := Newer(true)
	if err != nil {
		return err
	}
	if tag == "" {
		say("tongs " + Version() + " is the newest release")
		return nil
	}
	if err := Apply(tag, say); err != nil {
		return err
	}
	say("tongs is now " + tag + " (was " + Version() + ")")
	return nil
}
