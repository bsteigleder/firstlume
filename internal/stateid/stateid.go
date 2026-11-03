// Package stateid deduces where an uploaded Terraform state belongs (spec 0002,
// R9.9). A state doesn't record where it came from, so this compares what it
// does have with the stack's code: the path it came in, its resource addresses
// and, between folders with the same code, its identifiers. The result is only
// a suggestion: a person confirms it, and the next plan proves it.
package stateid

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/reslabel"
)

// Reasons a state was placed.
const (
	ReasonLineage   = "lineage"   // the same lineage is already in this unit
	ReasonPath      = "path"      // its path or file name matches the unit
	ReasonResources = "resources" // its addresses are in the unit's code
	ReasonValues    = "values"    // tied on code; its identifiers match this unit's values
	ReasonOnlyUnit  = "only_unit" // the stack has one unit and nothing contradicts it
)

// Reasons a state was left for a person to place.
const (
	WhyTie         = "tie"         // several folders fit equally
	WhyNoMatch     = "no_match"    // no folder fits
	WhyContradicts = "contradicts" // its path fits a folder, its resources don't
	WhyOlderCopy   = "older_copy"  // another upload is a newer copy of the same state
	WhyCompeting   = "competing"   // another upload, of another lineage, fits the same unit
)

const (
	// Share of a state's readable addresses a folder's code must have.
	fitShare = 0.9
	// Below this share, the code contradicts a matching path (R9.10).
	contradictShare = 0.5
	// Shorter values are too common to tell two folders apart.
	minToken = 3
)

// State is what identifying reads from one uploaded state.
type State struct {
	// Source names it in the upload.
	Source string
	// Dir is the folder it came in, empty for a loose file; Stem is the file's
	// name without extension, empty for the usual terraform.tfstate.
	Dir, Stem string
	Serial    int64
	Lineage   string
	Resources int
	// Addresses are its managed resources, without instance keys.
	Addresses []string
	// Identifiers are the values reslabel allows to be read, lower case.
	Identifiers []string
}

// Parse reads a state. The caller has checked it is valid; an attribute format
// it doesn't know is skipped, never guessed at.
func Parse(source, dir, stem string, data []byte) (State, error) {
	var doc struct {
		Serial    int64  `json:"serial"`
		Lineage   string `json:"lineage"`
		Resources []struct {
			Module    string `json:"module"`
			Mode      string `json:"mode"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			Instances []struct {
				Attributes map[string]json.RawMessage `json:"attributes"`
				Sensitive  json.RawMessage            `json:"sensitive_attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return State{}, fmt.Errorf("reading the state: %w", err)
	}
	st := State{Source: source, Dir: dir, Stem: stem, Serial: doc.Serial, Lineage: doc.Lineage}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range doc.Resources {
		if r.Mode != "managed" {
			continue
		}
		st.Resources++
		addr := r.Type + "." + r.Name
		if r.Module != "" {
			addr = instanceKey.ReplaceAllString(r.Module, "") + "." + addr
		}
		if !seen[addr] {
			seen[addr] = true
			st.Addresses = append(st.Addresses, addr)
		}
		for _, inst := range r.Instances {
			attrs, ok := reslabel.FromState(inst.Attributes, inst.Sensitive)
			if !ok {
				continue
			}
			for _, v := range attrs.Identifiers() {
				ids[strings.ToLower(v)] = true
			}
		}
	}
	for v := range ids {
		st.Identifiers = append(st.Identifiers, v)
	}
	sort.Strings(st.Identifiers)
	return st, nil
}

var instanceKey = regexp.MustCompile(`\[[^\]]*\]`)

// Unit is one place a state can go, as read from the code.
type Unit struct {
	// Key is the state's key; empty for a stack's single root unit.
	Key string
	// Addresses are the resources its code declares.
	Addresses map[string]bool
	// Opaque are the address prefixes of modules that can't be read, like
	// "module.vpc."; an empty prefix means all of it can't be read.
	Opaque []string
	// Literals are the fixed values in its own files, lower case.
	Literals map[string]bool
}

// Result is where a state goes, or why that is left to a person.
type Result struct {
	Source string
	// Placed says Unit holds a suggestion, since "" is the root unit.
	Placed bool
	Unit   string
	Reason string
	// Matched of Total of its readable addresses are in the unit's code.
	Matched, Total int
	Candidates     []string
	Why            string
}

// Identify places each state. inStack maps the lineages this stack already has
// to their unit. With no units (the code couldn't be read) only paths count.
func Identify(states []State, units []Unit, inStack map[string]string, stackPath string) []Result {
	out := make([]Result, len(states))
	for i, st := range states {
		out[i] = place(st, units, inStack, stackPath)
	}
	settleCollisions(states, out)
	return out
}

func place(st State, units []Unit, inStack map[string]string, stackPath string) Result {
	res := Result{Source: st.Source}
	if key, ok := inStack[st.Lineage]; ok {
		res.Placed, res.Unit, res.Reason = true, key, ReasonLineage
		return res
	}
	if len(units) == 0 {
		res.Placed, res.Unit, res.Reason = true, DirUnit(st.Dir, stackPath), ReasonPath
		return res
	}

	fits := make([]fit, len(units))
	for i := range units {
		fits[i] = measure(st, &units[i])
	}

	if len(units) == 1 {
		f := fits[0]
		res.Candidates = []string{units[0].Key}
		res.Matched, res.Total = f.matched, f.readable
		switch {
		case f.contradicts():
			res.Why = WhyNoMatch
		case f.fits():
			res.Placed, res.Unit, res.Reason = true, units[0].Key, ReasonResources
		default:
			res.Placed, res.Unit, res.Reason = true, units[0].Key, ReasonOnlyUnit
		}
		return res
	}

	byPath := -1
	if p := pathMatch(st, units); p >= 0 {
		if !fits[p].contradicts() {
			f := fits[p]
			res.Placed, res.Unit, res.Reason, res.Matched, res.Total = true, units[p].Key, ReasonPath, f.matched, f.readable
			return res
		}
		byPath = p
	}

	// The folders whose code has this state's resources; of those, the ones
	// closest to it, so a small state doesn't land in a big folder that
	// happens to share a name.
	var tied []int
	best := 0.0
	for i, f := range fits {
		if !f.fits() {
			continue
		}
		switch {
		case f.closeness > best+1e-9:
			best, tied = f.closeness, []int{i}
		case f.closeness > best-1e-9:
			tied = append(tied, i)
		}
	}
	if len(tied) == 1 {
		f := fits[tied[0]]
		res.Placed, res.Unit, res.Reason, res.Matched, res.Total = true, units[tied[0]].Key, ReasonResources, f.matched, f.readable
		return res
	}

	// Nothing to compare on code: folders whose modules can't be read may
	// still hold it, so their values decide.
	onlyOpaque := len(tied) == 0
	if onlyOpaque {
		for i, f := range fits {
			if f.readable == 0 && len(st.Addresses) > 0 {
				tied = append(tied, i)
			}
		}
	}
	if w := byValues(st, units, tied); w >= 0 {
		f := fits[w]
		res.Placed, res.Unit, res.Reason, res.Matched, res.Total = true, units[w].Key, ReasonValues, f.matched, f.readable
		return res
	}

	switch {
	case byPath >= 0:
		res.Why = WhyContradicts
		res.Candidates = []string{units[byPath].Key}
	case len(tied) > 1 && !onlyOpaque:
		res.Why = WhyTie
		for _, i := range tied {
			res.Candidates = append(res.Candidates, units[i].Key)
		}
	default:
		res.Why = WhyNoMatch
	}
	return res
}

type fit struct {
	matched, readable, declared int
	closeness                   float64
}

func (f fit) fits() bool {
	return f.readable > 0 && float64(f.matched) >= fitShare*float64(f.readable)
}

func (f fit) contradicts() bool {
	return f.readable > 0 && float64(f.matched) < contradictShare*float64(f.readable)
}

func measure(st State, u *Unit) fit {
	f := fit{declared: len(u.Addresses)}
	for _, a := range st.Addresses {
		if u.opaque(a) {
			continue
		}
		f.readable++
		if u.Addresses[a] {
			f.matched++
		}
	}
	if union := f.declared + f.readable - f.matched; union > 0 {
		f.closeness = float64(f.matched) / float64(union)
	}
	return f
}

func (u *Unit) opaque(addr string) bool {
	for _, p := range u.Opaque {
		if strings.HasPrefix(addr, p) {
			return true
		}
	}
	return false
}

// pathMatch returns the unit whose key the state's folder ends in, the longest
// one, or else the only one named like the file.
func pathMatch(st State, units []Unit) int {
	best, bestLen := -1, 0
	for i, u := range units {
		if u.Key == "" {
			continue
		}
		if (st.Dir == u.Key || strings.HasSuffix(st.Dir, "/"+u.Key)) && len(u.Key) > bestLen {
			best, bestLen = i, len(u.Key)
		}
	}
	if best >= 0 || st.Stem == "" {
		return best
	}
	for i, u := range units {
		if u.Key != "" && path.Base(u.Key) == st.Stem {
			if best >= 0 {
				return -1
			}
			best = i
		}
	}
	return best
}

// byValues returns which of the candidates has the most values only it has
// among the state's identifiers, or -1 if none stands out. A value all of them
// share says nothing about which one this is.
func byValues(st State, units []Unit, candidates []int) int {
	if len(candidates) < 2 || len(st.Identifiers) == 0 {
		return -1
	}
	seenIn := map[string]int{}
	tokens := make([]map[string]bool, len(candidates))
	for i, c := range candidates {
		tokens[i] = map[string]bool{}
		for l := range units[c].Literals {
			tokens[i][l] = true
		}
		for _, seg := range strings.Split(strings.ToLower(units[c].Key), "/") {
			tokens[i][seg] = true
		}
		for t := range tokens[i] {
			seenIn[t]++
		}
	}
	bestAt, bestHits, tie := -1, 0, false
	for i, c := range candidates {
		hits := 0
		for t := range tokens[i] {
			if seenIn[t] == 1 && len(t) >= minToken && foundIn(st.Identifiers, t) {
				hits++
			}
		}
		switch {
		case hits > bestHits:
			bestAt, bestHits, tie = c, hits, false
		case hits == bestHits && hits > 0:
			tie = true
		}
	}
	if tie {
		return -1
	}
	return bestAt
}

// foundIn says a value is one of the identifiers or a whole word of one, so
// "east" finds "acme-east-logs" but not "northeast".
func foundIn(identifiers []string, token string) bool {
	for _, id := range identifiers {
		for from := 0; ; {
			at := strings.Index(id[from:], token)
			if at < 0 {
				break
			}
			at += from
			end := at + len(token)
			if (at == 0 || !isWord(id[at-1])) && (end == len(id) || !isWord(id[end])) {
				return true
			}
			from = at + 1
		}
	}
	return false
}

func isWord(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// settleCollisions handles two states placed on one unit (R9.11): the newest
// copy of a lineage stays; states of different lineages are left to a person.
func settleCollisions(states []State, out []Result) {
	byUnit := map[string][]int{}
	for i, r := range out {
		if r.Placed {
			byUnit[r.Unit] = append(byUnit[r.Unit], i)
		}
	}
	unplace := func(i int, why, unit string) {
		out[i].Placed, out[i].Unit, out[i].Reason, out[i].Why = false, "", "", why
		out[i].Candidates = []string{unit}
	}
	for unit, idx := range byUnit {
		if len(idx) < 2 {
			continue
		}
		sameLineage := true
		newest := idx[0]
		for _, i := range idx[1:] {
			sameLineage = sameLineage && states[i].Lineage == states[newest].Lineage
			if states[i].Serial > states[newest].Serial {
				newest = i
			}
		}
		for _, i := range idx {
			switch {
			case !sameLineage:
				unplace(i, WhyCompeting, unit)
			case i != newest:
				unplace(i, WhyOlderCopy, unit)
			}
		}
	}
}

// DirUnit is the unit a state's folder would be by its path alone: what comes
// after the stack's own path, when the path contains it.
func DirUnit(dir, stackPath string) string {
	stackPath = strings.Trim(path.Clean("/"+stackPath), "/")
	if stackPath == "" {
		return dir
	}
	if dir == stackPath {
		return ""
	}
	if after, ok := strings.CutPrefix(dir, stackPath+"/"); ok {
		return after
	}
	if _, after, ok := strings.Cut(dir, "/"+stackPath+"/"); ok {
		return after
	}
	if strings.HasSuffix(dir, "/"+stackPath) {
		return ""
	}
	return dir
}
