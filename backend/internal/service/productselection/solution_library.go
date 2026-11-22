package productselection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

var ErrReport = errors.New("invalid solution report or edit")

type SolutionSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Purpose      string   `json:"purpose"`
	Parts        []string `json:"parts"`
	Prerequisite string   `json:"prerequisite"`
	Gap          string   `json:"gap"`
	Stage        string   `json:"stage"`
	Delivery     string   `json:"delivery"`
	Priority     int      `json:"priority"`
	Channels     []string `json:"channels"`
	Types        []string `json:"types"`
	BaseCost     *float64 `json:"base_cost"`
	Target       *float64 `json:"target"`
	Incomplete   bool     `json:"incomplete"`
	Review       bool     `json:"review"`
	Retired      bool     `json:"retired"`
}

func summarize(s Solution, refs reportRefs) SolutionSummary {
	v := SolutionSummary{ID: s.ID, Name: s.Name, Purpose: s.Purpose, Prerequisite: s.Prerequisite, Gap: s.Gap, Stage: s.Stage,
		Delivery: s.Delivery, Priority: s.Priority, Channels: s.Channels, Target: s.Target}
	items := map[string]Component{}
	types := map[string]bool{}
	for _, c := range s.Components {
		items[c.ID] = c
		if c.Role == "required" {
			v.Parts = append(v.Parts, c.Name)
		}
		for _, id := range c.Offers {
			if o := refs.offers[id]; o != nil {
				types[o.Type] = true
			}
		}
	}
	for typ := range types {
		v.Types = append(v.Types, typ)
	}
	sort.Strings(v.Types)
	seen, charged := map[string]bool{}, map[string]bool{}
	total, priced := 0.0, true
	var include func(string)
	include = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		c := items[id]
		if c.Status == "missing" {
			v.Incomplete = true
			priced = false
		}
		if c.Kind == "product" {
			if len(c.Offers) == 0 || refs.offers[c.Offers[0]] == nil || refs.offers[c.Offers[0]].Cost == nil {
				priced = false
			} else if !charged[c.Offers[0]] {
				charged[c.Offers[0]] = true
				total += *refs.offers[c.Offers[0]].Cost
			}
		}
		for _, dep := range c.DependsOn {
			include(dep)
		}
	}
	for _, c := range s.Components {
		if c.Role == "required" {
			include(c.ID)
		}
	}
	if priced && len(charged) > 0 {
		v.BaseCost = &total
	}
	return v
}

type libraryEntry struct {
	Report        string                     `json:"report"`
	PendingReport string                     `json:"pending_report,omitempty"`
	Revision      int64                      `json:"revision"`
	Summary       SolutionSummary            `json:"summary"`
	Notes         string                     `json:"notes,omitempty"`
	Edits         map[string]json.RawMessage `json:"edits,omitempty"`
	Conflicts     []string                   `json:"conflicts,omitempty"`
}

type libraryIndex struct {
	Schema   int                     `json:"schema"`
	Active   string                  `json:"active"`
	Versions map[string]string       `json:"versions"`
	Entries  map[string]libraryEntry `json:"entries"`
}

type Library struct {
	Dir   string
	write func(string, []byte) error
}

type LibraryList struct {
	Version   string            `json:"version"`
	Solutions []SolutionSummary `json:"solutions"`
}

type SolutionDetail struct {
	Solution   Solution   `json:"solution"`
	Version    string     `json:"version"`
	AnalyzedAt string     `json:"analyzed_at"`
	SnapshotAt string     `json:"snapshot_at"`
	Revision   int64      `json:"revision"`
	Notes      string     `json:"notes"`
	Conflicts  []string   `json:"conflicts"`
	Incoming   *Solution  `json:"incoming,omitempty"`
	Offers     []Offer    `json:"offers"`
	Evidence   []Evidence `json:"evidence"`
}

type SolutionEdit struct {
	Revision    int64    `json:"revision"`
	Solution    Solution `json:"solution"`
	Notes       string   `json:"notes"`
	UseIncoming bool     `json:"use_incoming"`
}

func (l Library) index() (libraryIndex, error) {
	index := libraryIndex{Schema: 1, Versions: map[string]string{}, Entries: map[string]libraryEntry{}}
	raw, err := os.ReadFile(filepath.Join(l.Dir, "library.json"))
	if errors.Is(err, os.ErrNotExist) {
		return index, nil
	}
	if err != nil {
		return index, err
	}
	if err = json.Unmarshal(raw, &index); err != nil {
		return index, err
	}
	if index.Schema != 1 || !validDigest(index.Versions[index.Active]) || index.Entries == nil {
		return index, errors.New("invalid library index")
	}
	return index, nil
}

func (l Library) save(path string, data []byte) error {
	if l.write != nil {
		return l.write(path, data)
	}
	return atomicWrite(path, data)
}

func (l Library) mutate(f func(*libraryIndex) (bool, error)) error {
	if err := os.MkdirAll(l.Dir, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(l.Dir, "library.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err = lockFile(file); err != nil {
		return err
	}
	defer func() { _ = unlockFile(file) }()
	idx, err := l.index()
	if err != nil {
		return err
	}
	change, err := f(&idx)
	if err != nil || !change {
		return err
	}
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return l.save(filepath.Join(l.Dir, "library.json"), b)
}

func (l Library) List() (LibraryList, error) {
	idx, err := l.index()
	list := LibraryList{Version: idx.Active, Solutions: []SolutionSummary{}}
	if err != nil {
		return list, err
	}
	for _, e := range idx.Entries {
		list.Solutions = append(list.Solutions, e.Summary)
	}
	sort.Slice(list.Solutions, func(i, j int) bool {
		a, b := list.Solutions[i], list.Solutions[j]
		if a.Retired != b.Retired {
			return !a.Retired
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return a.ID < b.ID
	})
	return list, nil
}

// The index is the only commit point. A crash after writing an immutable report
// leaves it invisible until a retry publishes the index.
func (l Library) report(idx libraryIndex, version string) (*SolutionReport, error) {
	hash := idx.Versions[version]
	if hash == "" {
		return nil, ErrNotFound
	}
	if !validDigest(hash) {
		return nil, errors.New("invalid stored report hash")
	}
	raw, err := os.ReadFile(filepath.Join(l.Dir, hash+".json"))
	if err != nil {
		return nil, err
	}
	if digest(raw) != hash {
		return nil, errors.New("stored report checksum mismatch")
	}
	var report SolutionReport
	if err = json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	if report.Version != version {
		return nil, errors.New("stored report version mismatch")
	}
	return &report, nil
}

type reportCache map[string]*SolutionReport

func (cache reportCache) load(l Library, idx libraryIndex, version string) (*SolutionReport, error) {
	if r := cache[version]; r != nil {
		return r, nil
	}
	r, err := l.report(idx, version)
	if err == nil {
		cache[version] = r
	}
	return r, err
}
func findSolution(r *SolutionReport, id string) (Solution, error) {
	for _, s := range r.Solutions {
		if s.ID == id {
			return s, nil
		}
	}
	return Solution{}, ErrNotFound
}

func fields(s Solution) map[string]json.RawMessage {
	b, _ := json.Marshal(s)
	var out map[string]json.RawMessage
	_ = json.Unmarshal(b, &out)
	return out
}

func applyEdits(s Solution, edits map[string]json.RawMessage) (Solution, error) {
	if len(edits) == 0 {
		return s, nil
	}
	// Decode onto the baseline so absent fields retain report values.
	b, err := json.Marshal(edits)
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	return s, err
}

func differences(base, edited Solution) map[string]json.RawMessage {
	original := fields(base)
	changes := fields(edited)
	for key, value := range changes {
		if bytes.Equal(original[key], value) {
			delete(changes, key)
		}
	}
	return changes
}

func (l Library) Import(r *SolutionReport) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrReport, err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	if len(raw) > MaxReportBytes {
		return false, ErrLimit
	}
	hash := digest(raw)
	added := false
	err = l.mutate(func(idx *libraryIndex) (bool, error) {
		if existing := idx.Versions[r.Version]; existing != "" {
			if existing != hash {
				return false, ErrConflict
			}
			return false, nil
		}
		cache := reportCache{}
		if idx.Active != "" {
			previous, err := cache.load(l, *idx, idx.Active)
			if err != nil {
				return false, err
			}
			before, _ := time.Parse(time.RFC3339Nano, previous.AnalyzedAt)
			next, _ := time.Parse(time.RFC3339Nano, r.AnalyzedAt)
			if !next.After(before) {
				return false, fmt.Errorf("%w: report precedes current analysis", ErrConflict)
			}
		}
		refs := r.references()
		current := map[string]bool{}
		for _, s := range r.Solutions {
			current[s.ID] = true
			e := idx.Entries[s.ID]
			e.Revision++
			if len(e.Edits) > 0 {
				previousEdits, previousConflicts := e.Edits, e.Conflicts
				e.Edits, e.Conflicts = maps.Clone(e.Edits), slices.Clone(e.Conflicts)
				old, err := cache.load(l, *idx, e.Report)
				if err != nil {
					return false, err
				}
				base, err := findSolution(old, s.ID)
				if err != nil {
					return false, err
				}
				oldFields, newFields := fields(base), fields(s)
				for k, custom := range e.Edits {
					if bytes.Equal(newFields[k], custom) {
						delete(e.Edits, k)
						e.Conflicts = slices.DeleteFunc(e.Conflicts, func(key string) bool { return key == k })
						continue
					}
					if !bytes.Equal(oldFields[k], newFields[k]) && !slices.Contains(e.Conflicts, k) {
						e.Conflicts = append(e.Conflicts, k)
					}
				}
				merged, err := applyEdits(s, e.Edits)
				if err != nil {
					return false, err
				}
				if validateSolution(merged, refs) != nil {
					// Preserve the old source and its evidence while an editor
					// reconciles removed products or changed dependencies.
					e.Edits, e.Conflicts = previousEdits, previousConflicts
					e.PendingReport = r.Version
					e.Summary.Review = true
					e.Summary.Retired = false
					idx.Entries[s.ID] = e
					continue
				}
				s = merged
			}
			sort.Strings(e.Conflicts)
			e.Report, e.PendingReport = r.Version, ""
			e.Summary = summarize(s, refs)
			e.Summary.Review = len(e.Conflicts) > 0
			idx.Entries[s.ID] = e
		}
		for id, e := range idx.Entries {
			if !current[id] && !e.Summary.Retired {
				e.Revision++
				e.Summary.Retired = true
				idx.Entries[id] = e
			}
		}
		if err := l.save(filepath.Join(l.Dir, hash+".json"), raw); err != nil {
			return false, err
		}
		idx.Versions[r.Version] = hash
		idx.Active = r.Version
		added = true
		return true, nil
	})
	return added && err == nil, err
}

func (l Library) ActiveReport(version string) (*SolutionReport, error) {
	idx, err := l.index()
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = idx.Active
	}
	return l.report(idx, version)
}

func (l Library) Detail(id string) (SolutionDetail, error) {
	var d SolutionDetail
	idx, err := l.index()
	if err != nil {
		return d, err
	}
	e, ok := idx.Entries[id]
	if !ok {
		return d, ErrNotFound
	}
	r, err := l.report(idx, e.Report)
	if err != nil {
		return d, err
	}
	s, err := findSolution(r, id)
	if err != nil {
		return d, err
	}
	s, err = applyEdits(s, e.Edits)
	if err != nil {
		return d, err
	}
	d = SolutionDetail{Solution: s, Version: r.Version, AnalyzedAt: r.AnalyzedAt, SnapshotAt: r.Snapshot.At, Revision: e.Revision,
		Notes: e.Notes, Conflicts: e.Conflicts, Offers: []Offer{}, Evidence: []Evidence{}}
	refs := r.references()
	seen := map[string]bool{}
	for _, c := range s.Components {
		for _, id := range c.Offers {
			if !seen[id] && refs.offers[id] != nil {
				d.Offers = append(d.Offers, *refs.offers[id])
				seen[id] = true
			}
		}
	}
	for _, id := range s.Evidence {
		d.Evidence = append(d.Evidence, refs.evidence[id])
	}
	if e.PendingReport != "" || len(e.Conflicts) > 0 {
		incoming := r
		if e.PendingReport != "" {
			incoming, err = l.report(idx, e.PendingReport)
			if err != nil {
				return d, err
			}
		}
		s, err := findSolution(incoming, id)
		if err != nil {
			return d, err
		}
		d.Incoming = &s
	}
	return d, nil
}

func (l Library) Edit(id string, req SolutionEdit) error {
	return l.mutate(func(idx *libraryIndex) (bool, error) {
		e, ok := idx.Entries[id]
		if !ok {
			return false, ErrNotFound
		}
		if e.Revision != req.Revision {
			return false, ErrConflict
		}
		version := e.Report
		if e.PendingReport != "" {
			if !req.UseIncoming {
				return false, fmt.Errorf("%w: reconcile updated report first", ErrConflict)
			}
			version = e.PendingReport
		}
		r, err := l.report(*idx, version)
		if err != nil {
			return false, err
		}
		base, err := findSolution(r, id)
		if err != nil {
			return false, err
		}
		s := req.Solution
		if s.ID != id || s.GoalKey != base.GoalKey || s.PathKey != base.PathKey || len(req.Notes) > 32000 || strings.ContainsRune(req.Notes, 0) {
			return false, ErrReport
		}
		refs := r.references()
		if err := validateSolution(s, refs); err != nil {
			return false, fmt.Errorf("%w: %v", ErrReport, err)
		}
		newSummary := summarize(s, refs)
		newSummary.Retired = e.Summary.Retired
		e = libraryEntry{Report: version, Revision: e.Revision + 1, Notes: req.Notes, Edits: differences(base, s), Summary: newSummary}
		idx.Entries[id] = e
		return true, nil
	})
}
