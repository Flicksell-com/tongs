// Package fetch gets what a project has released on GitHub: the list of its
// releases, and the files in them.
package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Release is one release of a project, as GitHub describes it.
type Release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"` // "sha256:…", on releases GitHub has one for
	} `json:"assets"`
}

// Sum is the SHA-256 GitHub gives for one of the release's files ("" if it
// gives none), and whether the release has that file at all.
func (r Release) Sum(name string) (sum string, ok bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			if len(a.Digest) > 7 && a.Digest[:7] == "sha256:" {
				return a.Digest[7:], true
			}
			return "", true
		}
	}
	return "", false
}

func api(repo string) string { return "https://api.github.com/repos/" + repo + "/releases" }

// Latest is the newest release of a repository ("owner/name") that is neither
// a draft nor a prerelease.
func Latest(repo string) (Release, error) {
	var r Release
	return r, getJSON(api(repo)+"/latest", &r)
}

// Tagged is the release of a repository with a tag.
func Tagged(repo, tag string) (Release, error) {
	var r Release
	return r, getJSON(api(repo)+"/tags/"+tag, &r)
}

// Recent lists a repository's releases, newest first.
func Recent(repo string) ([]Release, error) {
	var found []Release
	return found, getJSON(api(repo)+"?per_page=40", &found)
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

// File downloads one file of a release into to, and returns its SHA-256 and
// size. say is told what is being downloaded (what names it) and how big it
// is, then each tenth of it as "  40%".
func File(repo, tag, name, what string, to io.Writer, say func(string)) (sum string, size int64, err error) {
	response, err := get("https://github.com/" + repo + "/releases/download/" + tag + "/" + name)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	say(fmt.Sprintf("downloading %s, %d MB", what, response.ContentLength>>20))
	hash := sha256.New()
	size, err = io.Copy(io.MultiWriter(to, hash), &progress{from: response.Body, total: response.ContentLength, say: say})
	return hex.EncodeToString(hash.Sum(nil)), size, err
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
