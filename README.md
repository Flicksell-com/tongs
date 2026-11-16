# tongs

A terminal debugger for native programs (C, C++, Rust) that keeps the current
frame in view, and that a coding agent can drive alongside you.

It is a screen around lldb, not a new debugger. You type ordinary lldb
commands. The ones that move the program (`n`, `s`, `c`, `finish`) move the
source view; the ones that print (`p`, `bt`, `b`) print into a log, the way
lldb would. Beside the source sit the stack and, in one pane with tabs, the
variables, the breakpoints and the threads.

tongs downloads its own debugger the first time it runs. Nothing has to be
installed beforehand, and no lldb already on the machine is used.

## Install

```sh
go install github.com/Flicksell-com/tongs@latest
```

Needs Go 1.25 or later. Runs on macOS and Linux, arm64 and x86-64.

Or download the `tongs` built for your machine from the
[latest release](https://github.com/Flicksell-com/tongs/releases/latest), make
it executable and put it on your `PATH`.

- On macOS, the Xcode Command Line Tools must be installed
  (`xcode-select --install`); lldb uses their `debugserver` to control
  processes.
- Go to definition, references and hover use `clangd` if it is on your `PATH`.
  Everything else works without it.

## Use

```sh
tongs                       # open on the saved targets
tongs build/app             # open a binary
tongs //package:binary      # open a Bazel target, built with -c dbg when run
tongs storefront            # a saved target, by name
tongs --dir ~/code/other    # a project somewhere else
```

Run it from inside the project. The project is the folder of a saved target
you are inside, else the nearest directory above you that is a Bazel workspace
or a git repository.

Opening a target builds nothing and runs nothing. Its files can be read and
its breakpoints set; Run (`ctrl+r`) then builds it and starts it under the
debugger, and it runs until a breakpoint stops it.

A target is a binary plus, optionally, the shell command that builds it, its
arguments and its environment. Every project's targets are kept together, in
`~/.config/tongs/targets.json`, so they are there wherever tongs is started.

`ctrl+t` opens them, grouped by project folder, with the project you are in
first and its first target selected. From there `ctrl+n` adds a target beside
the selected one, `ctrl+e` edits and `ctrl+d` deletes. A new target's folder
can be typed, picked with the system's file dialog (`f2`), or chosen from the
folders other targets are already in (`f3`, or a click). For Cargo, CMake,
Make and Bazel projects the form fills in a likely build command.

A target is come back to as it was left: its breakpoints, the files open and
the one in front, the watches, the variables opened out, and the commands
typed. The sizes the panes were dragged to and the colours chosen are kept
too, for every target alike.

### Keys

The prompt has focus to begin with: type an lldb command and press enter.
Enter on an empty prompt repeats the last command. Tab completes.

| Key | Does |
| --- | --- |
| `f5` `f6` `f7` `f8` | continue, step over, step in, step out |
| `ctrl+c` | pause the program |
| `ctrl+r` | run: build and start, or start again, breakpoints kept |
| `ctrl+t` | choose what to debug |
| `ctrl+p` | open one of the program's source files |
| `ctrl+n` | find a function and break there |
| `ctrl+b` | list breakpoints, to go to one or change it |
| `f1` | search everything that can be done |
| `f10` | the menu bar; arrows move, enter chooses |
| `esc` | move into the source |
| `ctrl+q` | quit |

In the source the keys are vim's: `h j k l`, `w b e`, `0 ^ $`, `gg G`,
`ctrl+d ctrl+u`, `/ ? n N * #`, `%`, counts. `space` toggles a breakpoint on
the cursor line and `R` runs to it. `gd` goes to the definition, `gr` lists
references, `K` says what is under the cursor, `ctrl+o` goes back. `gt` and
`gT` go to the next file open and the one before. `i` or `:` returns to the
prompt.

The mouse works too: click a line, click the margin for a breakpoint, click a
stack frame or a thread to select it, click a variable to open or close it,
click a tab, scroll any pane. What a click would act on is picked out as the
pointer passes over it. Drag the edge between two panes to resize them;
"Panes: back to their usual sizes" in the View menu undoes it.

Along the top is a menu bar, and under it the buttons. Run builds the program
and starts it, and becomes Restart while it is alive; Stop ends it. Continue,
the steps and Pause are lit only while they would do something: the first
four while the program is stopped, Pause while it runs.

"Colours: choose a theme" in the View menu changes the colours. Until one is
chosen they are dark or light as the terminal is.

### Values

While the program is stopped, the variables of the frame are written, dimmed,
after the lines of source that mention them, from where each is declared down
to the line the program is on. Rest the pointer on a name, or on a member
(`here.x`, `p->next`), and its value is shown beside it.

In the variables pane, a value that is not what it was the last time the
program stopped in that function is picked out in yellow.

A watch is an expression worked out again at every stop, at the top of the
variables pane. `=` in the source watches what is under the cursor; "+ watch"
on the pane's edge, or "Watch an expression" in the Watch menu, asks for one. The ×
beside a watch drops it.

### Breakpoints

`space` on a line, or a click in its margin, sets a breakpoint or removes the
one there. Any line will do: on one with no code (a blank line, a brace, the
line a function's name is on) the breakpoint stays marked where it was put,
and stops the program on the next line that has code. They can be set and
removed at any time: before the program has run, while it is stopped or
running, and after it has ended. They are the target's own, kept as they are
changed, and given to the debugger each time the program starts. One set or
deleted at the prompt (`b`, `breakpoint delete`) is taken in the same.

The breakpoints tab lists them with how often each was hit. There, or with
the keys shown in `ctrl+b`'s list:

- click the mark to turn one off without losing it, and on again;
- **if** gives it a condition, an expression it stops only when true (`C` in
  the source, or a right click on the line, does the same);
- **print** has it print a message and run on instead of stopping, with the
  values of `{expressions}` in braces written in: `i is {i}, total {total}`
  (`P` in the source). The message goes to the lldb log;
- × deletes it.

In the margin a breakpoint is ● when it stops every time, ◆ with a condition
or a message, ○ when turned off.

### Threads

The threads tab lists the program's threads and where each is. A click
selects one; the stack, the variables and the source follow.

### Editing while you debug

tongs watches the files on screen, in the stack and with breakpoints. When
one is edited it is shown as it now is, straight away, and the cursor, the
arrow on the current line and the breakpoint marks stay on the code they were
on, wherever the edit moved it. The pane's title says the file was edited
since the build.

The running program is still the one built before the edit. `ctrl+r` rebuilds,
and the breakpoints are set again where their lines went. A line written
since the build can't take a breakpoint until then.

Two limits: a file and line typed at the prompt (`b main.c:40`) go to lldb as
they are, which means the lines of the build, and breakpoints follow only
the edits made while tongs is open.

## Agents

A coding agent can drive the same session you are looking at. You see every
step it takes and can take over at any point.

**Over MCP.** Add tongs as a stdio server; it offers one tool, `debug`, which
takes a line.

```json
{"mcpServers": {"tongs": {"command": "tongs", "args": ["--dir", "/path/to/project", "mcp"]}}}
```

If tongs is open in a terminal, the agent drives that session. If it is not,
one is started with no screen.

**Over the socket.** An open session listens on `/tmp/tongs-<uid>.sock`: one
line in, a plain-text report out.

```sh
echo 'b main' | nc -U /tmp/tongs-$(id -u).sock
```

A line is an lldb command, or one of:

```
status              where the program is: source, locals, stack
wait [seconds]      wait for a running program to stop, then status
targets             the saved targets
start <target>      debug a saved name, a Bazel label or a binary; then --
                    and arguments for it. Stops at entry: set breakpoints, c
restart             rebuild and start again, breakpoints kept
```

A command that moves the program answers with the new status: where it
stopped, the source around it, the locals and the stack.

## The debugger

tongs drives [CodeLLDB](https://github.com/vadimcn/codelldb), which bundles
its own lldb. The first run downloads the release tongs is tested with, checks
it against a checksum compiled into tongs, and unpacks it into
`~/.cache/tongs/codelldb`.

From the Debugger menu:

- **Debugger: download it again** fetches the version in use afresh.
- **Debugger: choose a version** lists CodeLLDB's releases and switches to
  one, downloading it if need be. Other versions are checked against the
  checksum GitHub publishes for the release, where there is one. Only the
  default version is tested.

To use a debugger of your own, set `TONGS_ADAPTER` to the path of a `codelldb`
adapter binary.

## Updating

Once a day, when it starts, tongs asks GitHub whether a newer release is out.
If one is, a green **Update** button appears at the top right. The button,
**Tongs: update to the newest release** in the Debugger menu, and
`tongs update` in a shell all do the same thing: download the release's tongs
for this machine, check it against the checksum GitHub publishes for it, and
put it in place of the one installed. The tongs that is open keeps running;
the next start is the new one.

`tongs version` prints the version. A tongs built from a checkout rather than
from a release is never offered an update: build it again instead.

### Releasing

Pushing a tag releases that version:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

The `release` workflow builds tongs for each platform and attaches the four
binaries to a GitHub release named after the tag.

## Notes

- Bazel targets are built with `bazelisk build -c dbg`, so `bazelisk` must be
  on your `PATH` to debug them.
- For go to definition outside Bazel, clangd needs a `compile_commands.json`
  at the top of the project or in `build/`.
- Windows is not supported.
