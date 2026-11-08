// Package command checks what a command run may execute (spec 0009, section 3).
//
// There is no shell: each line becomes a list of arguments run directly, and
// only the stack's engine (terraform or tofu) or terragrunt, with a subcommand
// from a closed list, is accepted. The same check runs when the run is created
// and again in the executor right before running it.
package command

import (
	"fmt"
	"path"
	"strings"
)

// MaxLength is the most a command may hold, all lines together.
const MaxLength = 4096

// Access is what a command run may do to the stack's state.
type Access string

const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

func (a Access) Valid() bool { return a == AccessRead || a == AccessWrite }

// Tools says which programs the stack uses. With Known false (a stack that
// never ran), both engines and terragrunt pass; the executor checks again with
// the configuration of the commit it runs.
type Tools struct {
	Known bool
	// Engine is "terraform" or "opentofu", as in .firstlume/config.yaml.
	Engine     string
	Terragrunt bool
}

// Line is one command, ready to run.
type Line struct {
	// Number is the line where it starts, from 1.
	Number int
	// Args is what runs; Args[0] is terraform, tofu or terragrunt.
	Args []string
	// Subcommand names what it does, such as "state mv".
	Subcommand string
	// Writes says whether it changes the state.
	Writes bool
	// Dirs are the folders it names (-chdir, --working-dir, -var-file), relative
	// to the stack's folder. The executor checks they really stay inside it.
	Dirs []string
}

// Error says which line was refused and why.
type Error struct {
	Line int    `json:"line"`
	Msg  string `json:"message"`
}

func (e *Error) Error() string {
	if e.Line == 0 {
		return e.Msg
	}
	return fmt.Sprintf("Line %d: %s", e.Line, e.Msg)
}

// Check parses text and refuses anything off the list. It returns the lines to
// run, in order.
func Check(text string, tools Tools, access Access) ([]Line, error) {
	if strings.TrimSpace(text) == "" {
		return nil, &Error{Msg: "Type a command."}
	}
	if len(text) > MaxLength {
		return nil, &Error{Msg: fmt.Sprintf("The command is too long: at most %d characters.", MaxLength)}
	}
	if !access.Valid() {
		return nil, &Error{Msg: "state access must be read or write"}
	}
	logical, err := split(text)
	if err != nil {
		return nil, err
	}
	var out []Line
	for _, l := range logical {
		args, err := tokenize(l.text)
		if err != nil {
			return nil, &Error{Line: l.number, Msg: err.Error()}
		}
		if len(args) == 0 {
			continue
		}
		line, err := checkLine(args, tools)
		if err != nil {
			return nil, &Error{Line: l.number, Msg: err.Error()}
		}
		if line.Writes && access == AccessRead {
			return nil, &Error{Line: l.number, Msg: fmt.Sprintf("Read only: choose “May change the state” to run %s.", line.Subcommand)}
		}
		line.Number = l.number
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, &Error{Msg: "Type a command."}
	}
	return out, nil
}

type logicalLine struct {
	number int
	text   string
}

// split joins lines ending in a backslash with the next one, and drops empty
// lines and comments.
func split(text string) ([]logicalLine, error) {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []logicalLine
	var cur strings.Builder
	start := 0
	for i, l := range raw {
		if cur.Len() == 0 {
			start = i + 1
			if t := strings.TrimSpace(l); t == "" || strings.HasPrefix(t, "#") {
				continue
			}
		}
		if strings.HasSuffix(l, `\`) && !strings.HasSuffix(l, `\\`) {
			cur.WriteString(strings.TrimSuffix(l, `\`))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(l)
		out = append(out, logicalLine{number: start, text: cur.String()})
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, logicalLine{number: start, text: cur.String()})
	}
	return out, nil
}

// forbidden are the characters a shell would give meaning to. There is no
// shell, so outside quotes they are refused rather than taken literally,
// which would surprise whoever typed them.
const forbidden = "|;&$`<>(){}*?~!"

// tokenize splits a line into arguments. Single quotes keep everything
// literal; double quotes too, except \" and \\. Nothing is expanded.
func tokenize(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		case c == '#' && !inArg:
			return args, nil // a comment to the end of the line
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return nil, fmt.Errorf("a quote ' isn’t closed.")
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 1
			inArg = true
		case c == '"':
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' && j+1 < len(s) && (s[j+1] == '"' || s[j+1] == '\\') {
					j++
				}
				cur.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, fmt.Errorf("a quote \" isn’t closed.")
			}
			i = j
			inArg = true
		case c == '\\':
			return nil, fmt.Errorf("“\\” isn’t allowed outside quotes: put the argument in single quotes.")
		case strings.IndexByte(forbidden, c) >= 0:
			return nil, fmt.Errorf("“%c” isn’t allowed: there is no shell, so no pipes, ;, &&, $( ), redirects or wildcards. Put it in quotes if it is part of an argument.", c)
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

func engineProgram(engine string) string {
	if engine == "opentofu" {
		return "tofu"
	}
	return "terraform"
}

// starters names what a line may start with, for messages.
func starters(t Tools) string {
	switch {
	case !t.Known:
		return "terraform, tofu or terragrunt"
	case t.Terragrunt:
		return "terragrunt"
	default:
		return engineProgram(t.Engine)
	}
}

func checkLine(args []string, t Tools) (Line, error) {
	prog := args[0]
	if strings.ContainsAny(prog, `/\`) {
		return Line{}, fmt.Errorf("start with the program’s name, not a path: %s.", starters(t))
	}
	switch prog {
	case "terraform", "tofu":
		// In a Terragrunt stack only Terragrunt sets up a unit's state: the
		// engine alone would find no backend and work on an empty local state,
		// so an import would "succeed" and be lost.
		if t.Known && t.Terragrunt {
			return Line{}, fmt.Errorf("this stack uses Terragrunt, which sets up each unit’s state: start with terragrunt, like terragrunt --working-dir <unit> state list or terragrunt run --all -- state list.")
		}
		if t.Known && prog != engineProgram(t.Engine) {
			return Line{}, fmt.Errorf("this stack uses %s: start with %s.", map[string]string{"terraform": "Terraform", "opentofu": "OpenTofu"}[t.Engine], engineProgram(t.Engine))
		}
		line, err := checkEngine(args[1:], true)
		line.Args = args
		return line, err
	case "terragrunt":
		if t.Known && !t.Terragrunt {
			return Line{}, fmt.Errorf("this stack doesn’t use Terragrunt: start with %s.", engineProgram(t.Engine))
		}
		line, err := checkTerragrunt(args[1:])
		line.Args = args
		return line, err
	}
	return Line{}, fmt.Errorf("%s isn’t allowed. Commands start with %s and a subcommand from the list.", prog, starters(t))
}

// subcommand is one entry of the list (S3).
type subcommand struct {
	writes bool
	// positional is how many arguments it takes besides flags: min and max (-1: any).
	min, max int
	// noJSON refuses -json and -raw, which print sensitive values (S4).
	noJSON bool
}

var subcommands = map[string]subcommand{
	"state list":             {min: 0, max: -1},
	"state show":             {min: 1, max: 1},
	"output":                 {min: 0, max: 1, noJSON: true},
	"show":                   {min: 0, max: 0, noJSON: true},
	"validate":               {min: 0, max: 0},
	"providers":              {min: 0, max: 0},
	"version":                {min: 0, max: 0},
	"workspace list":         {min: 0, max: 0},
	"workspace show":         {min: 0, max: 0},
	"state mv":               {writes: true, min: 2, max: 2},
	"state rm":               {writes: true, min: 1, max: -1},
	"state replace-provider": {writes: true, min: 2, max: 2},
	"import":                 {writes: true, min: 2, max: 2},
	"taint":                  {writes: true, min: 1, max: 1},
	"untaint":                {writes: true, min: 1, max: 1},
	"force-unlock":           {writes: true, min: 1, max: 1},
}

// refusedReasons explains the subcommands people are most likely to try.
var refusedReasons = map[string]string{
	"apply":   "apply isn’t allowed here: use Plan & apply, so the plan is reviewed.",
	"destroy": "destroy isn’t allowed here: use Destroy, so the plan is reviewed.",
	"plan":    "plan isn’t allowed here: use Plan only.",
	"refresh": "refresh isn’t allowed here: use Plan & apply, so the change to the state is reviewed.",
	"init":    "init isn’t needed: Firstlume already ran it.",
	"state pull": "state pull isn’t allowed: it would print the whole state, secrets included, to the log. " +
		"Use state list and state show.",
	"state push": "state push isn’t allowed: it replaces the state. Import a state from the stack’s State tab instead.",
}

// refusedFlags can't be used with any subcommand (S4).
var refusedFlags = map[string]string{
	"state":      "-state isn’t allowed: the state is always the stack’s.",
	"state-out":  "-state-out isn’t allowed: the state is always the stack’s.",
	"backup":     "-backup isn’t allowed: Firstlume keeps every version of the state.",
	"plugin-dir": "-plugin-dir isn’t allowed: the providers are the ones init installed.",
	"config":     "-config isn’t allowed: the code is the stack’s folder.",
}

// checkEngine checks the arguments after terraform or tofu. allowChdir is
// false after terragrunt run --, where Terragrunt picks the folder.
func checkEngine(args []string, allowChdir bool) (Line, error) {
	var line Line
	i := 0
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		name, value, hasValue := cutFlag(args[i])
		if name != "chdir" || !allowChdir {
			return line, fmt.Errorf("%s isn’t allowed before the subcommand.", args[i])
		}
		if !hasValue {
			return line, fmt.Errorf("write -chdir=<folder>, with the folder inside the stack.")
		}
		if err := insideStack("-chdir", value); err != nil {
			return line, err
		}
		line.Dirs = append(line.Dirs, value)
	}
	if i == len(args) {
		return line, fmt.Errorf("add a subcommand, such as state list.")
	}
	name := args[i]
	rest := args[i+1:]
	if name == "state" || name == "workspace" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return line, fmt.Errorf("add what to do with %s, such as %s list.", name, name)
		}
		name += " " + rest[0]
		rest = rest[1:]
	}
	sub, ok := subcommands[name]
	if !ok {
		if reason, known := refusedReasons[name]; known {
			return line, fmt.Errorf("%s", reason)
		}
		return line, fmt.Errorf("%s isn’t on the list of allowed subcommands.", name)
	}
	line.Subcommand = name
	line.Writes = sub.writes
	positional := 0
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional++
			continue
		}
		if a == "--" {
			positional += len(rest) - j - 1
			break
		}
		flag, value, hasValue := cutFlag(a)
		if reason, refused := refusedFlags[flag]; refused {
			return line, fmt.Errorf("%s", reason)
		}
		switch {
		case flag == "lock" && hasValue && value != "true":
			return line, fmt.Errorf("-lock=false isn’t allowed: the state must be locked while it changes.")
		case flag == "chdir":
			return line, fmt.Errorf("-chdir goes before the subcommand: terraform -chdir=<folder> %s.", name)
		case (flag == "json" || flag == "raw") && sub.noJSON:
			return line, fmt.Errorf("-%s would show sensitive values in the log. Without it, they show as (sensitive value).", flag)
		case flag == "var-file":
			if !hasValue {
				return line, fmt.Errorf("write -var-file=<file>, with the file inside the stack.")
			}
			if err := insideStack("-var-file", value); err != nil {
				return line, err
			}
			line.Dirs = append(line.Dirs, path.Dir(value))
		}
	}
	switch {
	case positional < sub.min:
		return line, fmt.Errorf("%s needs %s.", name, plural(sub.min, "argument"))
	case sub.max >= 0 && positional > sub.max:
		if sub.max == 0 {
			return line, fmt.Errorf("%s takes no arguments here.", name)
		}
		return line, fmt.Errorf("%s takes at most %s.", name, plural(sub.max, "argument"))
	}
	return line, nil
}

// checkTerragrunt accepts terragrunt <subcommand> and
// terragrunt run [--all] -- <subcommand>, with --working-dir (S6).
func checkTerragrunt(args []string) (Line, error) {
	var dirs []string
	run, all := false, false
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "run" && !run:
			run = true
		case a == "--all" && run:
			all = true
		case a == "--working-dir" || strings.HasPrefix(a, "--working-dir="):
			value := strings.TrimPrefix(a, "--working-dir=")
			if a == "--working-dir" {
				if i+1 == len(args) {
					return Line{}, fmt.Errorf("--working-dir needs a folder inside the stack.")
				}
				i++
				value = args[i]
			}
			if err := insideStack("--working-dir", value); err != nil {
				return Line{}, err
			}
			dirs = append(dirs, value)
		case a == "--" && run:
			line, err := checkEngine(args[i+1:], false)
			line.Dirs = append(dirs, line.Dirs...)
			if all && err == nil {
				line.Subcommand = "run --all -- " + line.Subcommand
			}
			return line, err
		case strings.HasPrefix(a, "-"):
			return Line{}, fmt.Errorf("%s isn’t allowed with terragrunt.", a)
		default:
			if run {
				return Line{}, fmt.Errorf("write terragrunt run --all -- <subcommand>, with the subcommand after --.")
			}
			line, err := checkEngine(args[i:], false)
			line.Dirs = append(dirs, line.Dirs...)
			return line, err
		}
	}
	if run {
		return Line{}, fmt.Errorf("write terragrunt run --all -- <subcommand>, with the subcommand after --.")
	}
	return Line{}, fmt.Errorf("add a subcommand, such as state list.")
}

// cutFlag splits -name=value or --name=value.
func cutFlag(a string) (name, value string, hasValue bool) {
	a = strings.TrimLeft(a, "-")
	name, value, hasValue = strings.Cut(a, "=")
	return name, value, hasValue
}

// insideStack refuses absolute paths and paths that climb out of the stack's
// folder. Symbolic links are checked by the executor, on disk.
func insideStack(flag, p string) error {
	clean := path.Clean(strings.ReplaceAll(p, `\`, "/"))
	if p == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("the folder of %s must stay inside the stack.", flag)
	}
	return nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
