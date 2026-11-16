// tongs builds a target for debugging and steps through it in lldb.
//
//	tongs
//	tongs //package:binary
//	tongs build/app
//	tongs storefront
//	tongs --dir ~/code/other-project
//	tongs mcp
//	tongs update
//	tongs version
//
// With no argument it opens on the saved targets. The argument is a saved
// target's name, a Bazel label, or a path to a binary. --dir debugs a project
// elsewhere on the machine. `mcp` serves a coding agent on stdio; an agent can
// also drive an open session through its socket (see the agent package).
//
// The debugger itself is downloaded the first time tongs runs (see the lldb
// package); nothing has to be installed beforehand. `update` replaces tongs
// with the newest release (see the update package).
package main

import (
	"fmt"
	"os"

	"github.com/Flicksell-com/tongs/agent"
	"github.com/Flicksell-com/tongs/lldb"
	"github.com/Flicksell-com/tongs/session"
	"github.com/Flicksell-com/tongs/store"
	"github.com/Flicksell-com/tongs/ui"
	"github.com/Flicksell-com/tongs/update"
)

func fail(err error) {
	fmt.Fprintln(os.Stderr, "tongs:", err)
	os.Exit(1)
}

func main() {
	args := os.Args[1:]
	dir := ""
	if len(args) >= 2 && args[0] == "--dir" {
		dir, args = args[1], args[2:]
	}
	if len(args) > 1 || (len(args) == 1 && args[0] != "mcp" && args[0][0] == '-') {
		fail(fmt.Errorf("usage: tongs [--dir <project>] [<saved name | //label | path | mcp | update | version>]"))
	}
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("tongs", update.Version())
		return
	}
	if len(args) == 1 && args[0] == "update" {
		if err := update.Run(func(line string) { fmt.Println(line) }); err != nil {
			fail(err)
		}
		return
	}
	entries, err := store.Load()
	if err != nil {
		fail(err)
	}
	root, err := store.FindRoot(dir, entries)
	if err != nil {
		fail(err)
	}
	adapter, err := lldb.Adapter(func(line string) { fmt.Fprintln(os.Stderr, "tongs:", line) })
	if err != nil {
		fail(err)
	}
	if err := os.Chdir(root); err != nil {
		fail(err)
	}

	if len(args) == 1 && args[0] == "mcp" {
		// If tongs is open in a terminal, the agent drives that session and the
		// person there watches. If not, one is started here with no screen.
		if _, err := agent.Send("help"); err != nil {
			s := session.New(root, adapter)
			server, err := agent.Listen(s, func() []store.Entry { return entries })
			if err != nil {
				fail(err)
			}
			defer s.Close()
			defer server.Close()
		}
		agent.ServeMCP()
		return
	}

	var first *store.Entry
	if len(args) == 1 {
		// A saved name means the one in this project before another's.
		first = &store.Entry{Name: args[0], Target: args[0], Dir: root}
		saved := false
		for _, e := range entries {
			if e.Name == args[0] && (!saved || e.Dir == root) {
				first, saved = &e, true
			}
		}
	}
	if err := ui.Run(root, adapter, entries, first); err != nil {
		fail(err)
	}
}
