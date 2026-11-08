package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/gitprovider"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
	"github.com/bsteigleder/firstlume/internal/stateid"
	"github.com/bsteigleder/firstlume/internal/store"
)

// Identifying uploaded states (spec 0002, R9.7 to R9.16). Nothing here writes.

// identifyNewStackState identifies states for a stack not created yet, from the
// repository, branch and folder in the form.
func (s *Server) identifyNewStackState(w http.ResponseWriter, r *http.Request) {
	form, err := readImportForm(w, r, 4*s.cfg.StateMaxBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRef(form.ref); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !strings.Contains(form.gitRepository, "/") {
		writeError(w, http.StatusUnprocessableEntity, "git_repository must be the repository's path, like owner/name")
		return
	}
	units, note := s.readStackCode(r.Context(), orgID(r), form.gitRepository, "", form.ref, form.path)
	s.writeIdentified(w, r, form, units, note, "", form.path)
}

// identifyStackState identifies states for an existing stack.
func (s *Server) identifyStackState(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetStack(r.Context(), orgID(r), id(r))
	if err != nil {
		s.storeError(w, r, err, "stack")
		return
	}
	form, err := readImportForm(w, r, 4*s.cfg.StateMaxBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var units []stateid.Unit
	note := "this stack doesn’t use the Git connection, so only the paths in the files were compared"
	if st.ConnectionID != "" {
		units, note = s.readStackCode(r.Context(), orgID(r), "", st.Repository, st.Ref, st.Path)
	}
	s.writeIdentified(w, r, form, units, note, st.ID, st.Path)
}

// readStackCode reads a folder's units through the organization's Git
// connection, from a repository path or a stack's clone URL. Code that can't
// be read isn't an error: only the paths count then (R9.12), and note says why.
func (s *Server) readStackCode(ctx context.Context, orgID, fullName, cloneURL, ref, stackPath string) (units []stateid.Unit, note string) {
	conn, err := s.store.GetGitConnection(ctx, orgID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "this organization has no Git connection, so only the paths in the files were compared"
	}
	if err == nil && cloneURL != "" {
		fullName = repoPathOf(conn.URL, cloneURL)
	}
	// The connection's token never goes to a repository of another account.
	if err != nil || !gitprovider.InAccount(conn.Provider, conn.Account, fullName) {
		return nil, "the code couldn’t be read, so only the paths in the files were compared"
	}
	client, err := gitprovider.New(conn.Provider, conn.URL, conn.Token)
	if err == nil {
		units, err = readUnitsFromGit(ctx, client, fullName, ref, stackPath)
	}
	if err != nil {
		s.log.Warn("reading a stack's code to identify states", "repository", fullName, "ref", ref, "err", err)
		if errors.Is(err, stateid.ErrTooMuchCode) || errors.Is(err, gitprovider.ErrTreeTooLarge) {
			return nil, "the repository is too large to compare, so only the paths in the files were compared"
		}
		return nil, "the code couldn’t be read, so only the paths in the files were compared"
	}
	return units, ""
}

func readUnitsFromGit(ctx context.Context, client gitprovider.Provider, fullName, ref, stackPath string) ([]stateid.Unit, error) {
	sha, err := client.ResolveBranch(ctx, fullName, ref)
	if err != nil {
		return nil, err
	}
	entries, err := client.Tree(ctx, fullName, sha)
	if err != nil {
		return nil, err
	}
	repo := stateid.Repo{Read: func(ctx context.Context, p string) ([]byte, error) { return client.File(ctx, fullName, sha, p) }}
	for _, e := range entries {
		if e.Type == "blob" {
			repo.Files = append(repo.Files, e.Path)
		}
	}
	data, err := client.File(ctx, fullName, sha, path.Join(stackPath, stackconfig.Dir, stackconfig.File))
	if err != nil {
		return nil, err
	}
	cfg, err := stackconfig.Parse(data)
	if err != nil {
		return nil, err
	}
	return stateid.ReadUnits(ctx, repo, stackPath, cfg.TerragruntVersion != "", cfg.RunAll)
}

// uploadedState is one state of an upload, under a name unique in that upload.
type uploadedState struct {
	source, dir, stem string
	data              []byte
}

// readUploads lists the states in the uploaded files: tar.gz bundles and loose
// states, in any order of folders (R9.8).
func readUploads(files []importFile, maxState, maxTotal int64) (found []uploadedState, ignored []string, problems []api.StateImportProblem) {
	used := map[string]int{}
	add := func(name, dir string, data []byte) {
		source := name
		if used[name]++; used[name] > 1 {
			source = fmt.Sprintf("%s (%d)", name, used[name])
		}
		if dir == "." {
			dir = ""
		}
		found = append(found, uploadedState{source: source, dir: dir, stem: stateStem(name), data: data})
	}
	for _, f := range files {
		if isLooseState(f.data) {
			switch err := store.CheckState(f.data); {
			case int64(len(f.data)) > maxState:
				problems = append(problems, api.StateImportProblem{Path: f.name, Error: fmt.Sprintf("state larger than the limit of %d bytes", maxState)})
			case err != nil:
				problems = append(problems, api.StateImportProblem{Path: f.name, Error: err.Error()})
			default:
				add(f.name, "", f.data)
			}
			continue
		}
		states, ign, probs := scanBundle(f.data, true, maxState, maxTotal)
		ignored = append(ignored, ign...)
		for _, p := range probs {
			if p.Path == "file" {
				p.Path = f.name
			}
			problems = append(problems, p)
		}
		for _, b := range states {
			add(path.Clean(b.name), b.dir, b.data)
		}
	}
	return found, ignored, problems
}

// stateStem is a state file's name without extension, empty for the usual one.
func stateStem(name string) string {
	stem := strings.TrimSuffix(path.Base(name), ".tfstate")
	if stem == "terraform" {
		return ""
	}
	return stem
}

// readPlaced turns an upload and its placement into the states to import.
func readPlaced(form *importForm, maxState, maxTotal int64) (items []store.StateImport, ignored []string, problems []api.StateImportProblem) {
	var placements []api.StatePlacement
	if err := json.Unmarshal([]byte(form.placement), &placements); err != nil {
		return nil, nil, []api.StateImportProblem{{Path: "placement", Error: "not a valid list of placements"}}
	}
	found, ignored, problems := readUploads(form.files, maxState, maxTotal)
	bySource := make(map[string]api.StatePlacement, len(placements))
	for _, p := range placements {
		bySource[p.Source] = p
	}
	seen := map[string]string{}
	for _, f := range found {
		p, ok := bySource[f.source]
		if !ok {
			problems = append(problems, api.StateImportProblem{Path: f.source, Error: "no placement says where this state goes"})
			continue
		}
		delete(bySource, f.source)
		if p.Skip {
			continue
		}
		key, err := stateKey(p.Unit)
		if err != nil {
			problems = append(problems, api.StateImportProblem{Path: f.source, Error: err.Error()})
			continue
		}
		if other, dup := seen[key]; dup {
			problems = append(problems, api.StateImportProblem{Path: f.source, Error: "the same unit as " + other})
			continue
		}
		seen[key] = f.source
		items = append(items, store.StateImport{Key: key, Data: f.data})
	}
	for source := range bySource {
		problems = append(problems, api.StateImportProblem{Path: source, Error: "placed, but the upload has no such state"})
	}
	if len(items) == 0 && len(problems) == 0 {
		problems = append(problems, api.StateImportProblem{Path: "placement", Error: "every state was left out"})
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Path < problems[j].Path })
	return items, ignored, problems
}

func (s *Server) writeIdentified(w http.ResponseWriter, r *http.Request, form *importForm, units []stateid.Unit, note, stackID, stackPath string) {
	found, ignored, problems := readUploads(form.files, s.cfg.StateMaxBytes, 4*s.cfg.StateMaxBytes)
	res := api.StateIdentifyResult{Units: []string{}, CodeNote: note, States: []api.IdentifiedState{}, Ignored: ignored, Problems: problems}
	for _, u := range units {
		res.Units = append(res.Units, u.Key)
	}
	sort.Strings(res.Units)

	states := make([]stateid.State, 0, len(found))
	lineages := make([]string, 0, len(found))
	for _, f := range found {
		st, err := stateid.Parse(f.source, f.dir, f.stem, f.data)
		if err != nil {
			res.Problems = append(res.Problems, api.StateImportProblem{Path: f.source, Error: err.Error()})
			continue
		}
		states = append(states, st)
		lineages = append(lineages, st.Lineage)
	}
	known, err := s.store.StateLineages(r.Context(), orgID(r), lineages)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	inStack := map[string]string{}
	elsewhere := map[string][]api.StateLocation{}
	for _, l := range known {
		if l.StackID == stackID {
			inStack[l.Lineage] = l.Unit
		} else {
			elsewhere[l.Lineage] = append(elsewhere[l.Lineage], l.StateLocation)
		}
	}
	results := stateid.Identify(states, units, inStack, stackPath)
	for i, st := range states {
		out := results[i]
		res.States = append(res.States, api.IdentifiedState{
			Source: st.Source, Resources: st.Resources, Serial: st.Serial, Lineage: st.Lineage,
			Placed: out.Placed, Unit: out.Unit, Reason: out.Reason, Matched: out.Matched, Total: out.Total,
			Why: out.Why, Candidates: out.Candidates, PathUnit: stateid.DirUnit(st.Dir, stackPath), AlreadyIn: elsewhere[st.Lineage],
		})
	}
	writeJSON(w, http.StatusOK, res)
}
