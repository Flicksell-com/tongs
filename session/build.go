package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Flicksell-com/tongs/store"
)

// Set by `bazel run`. The nested bazelisk and the program being debugged must
// not inherit this tool's own runfiles.
var scrubbed = map[string]bool{
	"BUILD_WORKSPACE_DIRECTORY": true, "BUILD_WORKING_DIRECTORY": true, "BUILD_ID": true,
	"BUILD_EXECROOT": true, "RUNFILES_DIR": true, "RUNFILES_MANIFEST_FILE": true,
	"JAVA_RUNFILES": true, "PYTHON_RUNFILES": true, "BAZELISK_SKIP_WRAPPER": true,
	"TONGS_ADAPTER": true,
}

const executableQuery = "target.files_to_run.executable.path if target.files_to_run.executable else ''"

func cleanEnv() []string {
	var env []string
	for _, item := range os.Environ() {
		if name, _, _ := strings.Cut(item, "="); !scrubbed[name] {
			env = append(env, item)
		}
	}
	return env
}

// resolve gives the binary to debug. A label is built with Bazel; a path is
// used as it is, after its build command if it has one.
func resolve(root string, entry store.Entry, onLine func(string)) (string, error) {
	target := entry.Target
	if !store.IsLabel(target) {
		if entry.Build != "" {
			onLine(entry.Build)
			if _, err := run(root, onLine, "sh", "-c", entry.Build); err != nil {
				return "", fmt.Errorf("`%s` failed", entry.Build)
			}
		}
		path := target
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return "", fmt.Errorf("%s: no such file", target)
		}
		return path, nil
	}

	onLine("bazelisk build -c dbg " + target)
	if _, err := run(root, onLine, "bazelisk", "build", "-c", "dbg", target); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("bazelisk is not on PATH")
		}
		return "", fmt.Errorf("build of %s failed", target)
	}
	lines, err := run(root, nil, "bazelisk", "cquery", "-c", "dbg", "--output=starlark",
		"--starlark:expr="+executableQuery, target)
	path := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "bazel-out/") || strings.HasPrefix(line, "external/") {
			path = line
		}
	}
	if err != nil || path == "" {
		return "", fmt.Errorf("%s does not build an executable", target)
	}
	// Relative to the execroot; the bazel-out link at the root points into it.
	return filepath.Join(root, path), nil
}

// run runs a command at the root. Lines go to onLine if given (stderr too:
// Bazel reports progress there); otherwise stdout's lines are returned.
func run(root string, onLine func(string), name string, args ...string) ([]string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Env = cleanEnv()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if onLine != nil {
		cmd.Stderr = cmd.Stdout
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if onLine != nil {
			onLine(scanner.Text())
		} else {
			lines = append(lines, scanner.Text())
		}
	}
	return lines, cmd.Wait()
}
