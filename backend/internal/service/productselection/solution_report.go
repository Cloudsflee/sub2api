package productselection

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

const MaxReportBytes = 96 << 20

type Snapshot struct {
	At             string `json:"at"`
	Source         string `json:"source"`
	SHA256         string `json:"sha256"`
	IdentitySHA256 string `json:"identity_sha256"`
	Count          int    `json:"count"`
	Shops          int    `json:"shops"`
}

type Offer struct {
	ID           string   `json:"id"`
	ShopID       string   `json:"shop_id"`
	Shop         string   `json:"shop"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Type         string   `json:"type"`
	Category     string   `json:"category"`
	Minimum      int      `json:"minimum"`
	Cost         *float64 `json:"cost"` // Cash outlay for the minimum purchase, CNY.
	QuoteAt      string   `json:"quote_at"`
	DetailState  string   `json:"detail_state"`
	DetailAt     string   `json:"detail_at"`
	Original     string   `json:"original,omitempty"`
	DetailReason string   `json:"detail_reason,omitempty"`
	Disposition  string   `json:"disposition"`
	Reason       string   `json:"reason"`
}

type Evidence struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	URL   string `json:"url"`
	At    string `json:"at"`
	Fact  string `json:"fact"`
}

type Component struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Condition string   `json:"condition"`
	DependsOn []string `json:"depends_on"`
	Offers    []string `json:"offers"` // First offer is the representative cost basis.
}

type Problem struct {
	Trigger   string `json:"trigger"`
	Check     string `json:"check"`
	Action    string `json:"action"`
	Fallback  string `json:"fallback"`
	Component string `json:"component"`
	Status    string `json:"status"`
	Offer     string `json:"offer,omitempty"` // Merchant whose terms triggered this node.
}

type Journey struct {
	Purchase  string `json:"purchase"`
	Activate  string `json:"activate"`
	Use       string `json:"use"`
	Exception string `json:"exception"`
	Renew     string `json:"renew"`
}

type Commercial struct {
	Competition string `json:"competition"`
	Information string `json:"information"`
	Acquisition string `json:"acquisition"`
	Repeat      string `json:"repeat"`
	Costs       string `json:"costs"`
	Aftercare   string `json:"aftercare"`
}

type Solution struct {
	ID           string      `json:"id"`
	GoalKey      string      `json:"goal_key"`
	PathKey      string      `json:"path_key"`
	Name         string      `json:"name"`
	Purpose      string      `json:"purpose"`
	Customer     string      `json:"customer"`
	Difference   string      `json:"difference"`
	Prerequisite string      `json:"prerequisite"`
	Gap          string      `json:"gap"`
	Stage        string      `json:"stage"`
	Delivery     string      `json:"delivery"`
	Priority     int         `json:"priority"`
	Channels     []string    `json:"channels"`
	Target       *float64    `json:"target"`
	Observed     *float64    `json:"observed"`
	Journey      Journey     `json:"journey"`
	Problems     []Problem   `json:"problems"`
	Components   []Component `json:"components"`
	Commercial   Commercial  `json:"commercial"`
	Evidence     []string    `json:"evidence"`
}

type Merge struct {
	SolutionID string `json:"solution_id"`
	Variants   string `json:"variants"`
	Reason     string `json:"reason"`
}

type SolutionReport struct {
	Schema     int        `json:"schema"`
	Version    string     `json:"version"`
	AnalyzedAt string     `json:"analyzed_at"`
	Snapshot   Snapshot   `json:"snapshot"`
	Offers     []Offer    `json:"offers"`
	Evidence   []Evidence `json:"evidence"`
	Solutions  []Solution `json:"solutions"`
	Merges     []Merge    `json:"merges"`
}

type Coverage struct {
	Type     string         `json:"type"`
	Total    int            `json:"total"`
	Uses     map[string]int `json:"uses"`
	Details  map[string]int `json:"details"`
	Unpriced int            `json:"unpriced"`
}

type ReportAudit struct {
	Version    string     `json:"version"`
	AnalyzedAt string     `json:"analyzed_at"`
	Snapshot   Snapshot   `json:"snapshot"`
	Valid      int        `json:"valid"`
	Incomplete int        `json:"incomplete"`
	Verified   int        `json:"verified"`
	Coverage   []Coverage `json:"coverage"`
	Merges     []Merge    `json:"merges"`
}

func hasText(values ...string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" || strings.ContainsRune(v, 0) {
			return false
		}
	}
	return true
}

func dateOK(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }
func priceOK(p *float64) bool {
	return p == nil || (*p >= 0 && *p <= 1e10 && !math.IsNaN(*p) && !math.IsInf(*p, 0))
}
func urlOK(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
func oneOf(s string, options ...string) bool { return slices.Contains(options, s) }
func digest(data []byte) string              { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validDigest(s string) bool              { _, err := hex.DecodeString(s); return len(s) == 64 && err == nil }

type reportRefs struct {
	offers   map[string]*Offer
	evidence map[string]Evidence
}

func (r *SolutionReport) references() reportRefs {
	refs := reportRefs{make(map[string]*Offer, len(r.Offers)), make(map[string]Evidence, len(r.Evidence))}
	for i := range r.Offers {
		o := &r.Offers[i]
		refs.offers[o.ID] = o
	}
	for _, e := range r.Evidence {
		refs.evidence[e.ID] = e
	}
	return refs
}

func validateSolution(s Solution, refs reportRefs) error {
	if !idPattern.MatchString(s.ID) || !idPattern.MatchString(s.GoalKey) || !idPattern.MatchString(s.PathKey) ||
		!hasText(s.Name, s.Purpose, s.Customer, s.Difference, s.Prerequisite, s.Gap, s.Journey.Purchase, s.Journey.Activate,
			s.Journey.Use, s.Journey.Exception, s.Journey.Renew, s.Commercial.Competition, s.Commercial.Information,
			s.Commercial.Acquisition, s.Commercial.Repeat, s.Commercial.Costs, s.Commercial.Aftercare) ||
		!oneOf(s.Stage, "priority", "trial", "review", "hold") || !oneOf(s.Delivery, "untested", "verified", "failed") ||
		s.Priority < 1 || !priceOK(s.Observed) || !priceOK(s.Target) || len(s.Channels) == 0 ||
		len(s.Components) == 0 || len(s.Problems) == 0 || len(s.Evidence) == 0 {
		return errors.New("incomplete solution")
	}
	for _, channel := range s.Channels {
		if !oneOf(channel, "private", "taobao", "xianyu", "xiaohongshu") {
			return errors.New("invalid channel")
		}
	}
	for _, id := range s.Evidence {
		if _, ok := refs.evidence[id]; !ok {
			return fmt.Errorf("unknown evidence %q", id)
		}
	}
	components := map[string]Component{}
	linked := map[string]bool{}
	required := false
	for _, item := range s.Components {
		if !idPattern.MatchString(item.ID) || components[item.ID].ID != "" || !hasText(item.Name, item.Condition) ||
			!oneOf(item.Kind, "product", "guide", "service") || !oneOf(item.Role, "required", "optional", "alternative", "backup") ||
			!oneOf(item.Status, "verified", "merchant", "untested", "missing") {
			return errors.New("invalid component")
		}
		required = required || item.Role == "required"
		components[item.ID] = item
		if item.Kind == "product" && item.Status != "missing" && len(item.Offers) == 0 {
			return errors.New("product component requires a source")
		}
		seen := map[string]bool{}
		for _, id := range item.Offers {
			if refs.offers[id] == nil || seen[id] {
				return fmt.Errorf("invalid product reference %q", id)
			}
			seen[id], linked[id] = true, true
		}
	}
	if len(linked) == 0 || !required {
		return errors.New("solution requires a real offer and a required component")
	}
	visited := map[string]uint8{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visited[id] == 2 {
			return true
		}
		if visited[id] == 1 || components[id].ID == "" {
			return false
		}
		visited[id] = 1
		for _, dep := range components[id].DependsOn {
			if !visit(dep) {
				return false
			}
		}
		visited[id] = 2
		return true
	}
	for id := range components {
		if !visit(id) {
			return errors.New("missing or cyclic dependency")
		}
	}
	for _, p := range s.Problems {
		if !hasText(p.Trigger, p.Check, p.Action, p.Fallback) || components[p.Component].ID == "" ||
			(p.Offer != "" && !linked[p.Offer]) || !oneOf(p.Status, "verified", "merchant", "untested", "missing") {
			return errors.New("invalid problem or handling reference")
		}
	}
	return nil
}

func (r *SolutionReport) Validate() error {
	if r.Schema != 1 || !idPattern.MatchString(r.Version) || !dateOK(r.AnalyzedAt) || !dateOK(r.Snapshot.At) ||
		!validDigest(r.Snapshot.SHA256) || !hasText(r.Snapshot.Source) || len(r.Offers) == 0 || len(r.Solutions) == 0 ||
		r.Snapshot.Count != len(r.Offers) || r.Snapshot.Shops < 1 {
		return errors.New("invalid report version or snapshot")
	}
	refs := r.references()
	if len(refs.offers) != len(r.Offers) || len(refs.evidence) != len(r.Evidence) {
		return errors.New("duplicate product or evidence identity")
	}
	ids := make([]string, 0, len(r.Offers))
	for _, o := range r.Offers {
		if !idPattern.MatchString(o.ID) || !idPattern.MatchString(o.ShopID) || !urlOK(o.URL) || !hasText(o.Name, o.Shop, o.Type, o.Reason) ||
			!oneOf(o.Disposition, "component", "backup", "pending", "unsuitable") || o.Minimum < 0 || !priceOK(o.Cost) ||
			(o.Minimum == 0 && (o.Cost != nil || oneOf(o.Disposition, "component", "backup"))) ||
			(o.Cost != nil && !dateOK(o.QuoteAt)) || (o.Cost == nil && o.QuoteAt != "") ||
			!oneOf(o.DetailState, "read", "missing", "failed", "blocked", "pending") || (o.DetailAt != "" && !dateOK(o.DetailAt)) ||
			(o.DetailState == "read" && (!dateOK(o.DetailAt) || !hasText(o.Original))) ||
			(o.DetailState != "read" && (o.Original != "" || !hasText(o.DetailReason))) {
			return fmt.Errorf("invalid offer %q", o.ID)
		}
		ids = append(ids, o.ShopID+"/"+o.ID)
	}
	sort.Strings(ids)
	if digest([]byte(strings.Join(ids, "\n"))) != r.Snapshot.IdentitySHA256 {
		return errors.New("product identities differ from frozen snapshot")
	}
	for _, e := range r.Evidence {
		if !idPattern.MatchString(e.ID) || !hasText(e.Title, e.Fact) || !dateOK(e.At) || !urlOK(e.URL) ||
			!oneOf(e.Kind, "official", "merchant", "observation", "assumption", "unverified") {
			return fmt.Errorf("invalid evidence %q", e.ID)
		}
	}
	solutions, paths, linked, primary := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, s := range r.Solutions {
		key := strings.ToLower(s.GoalKey + "/" + s.PathKey)
		if solutions[s.ID] || paths[key] {
			return errors.New("duplicate solution or goal and necessary path")
		}
		solutions[s.ID], paths[key] = true, true
		if err := validateSolution(s, refs); err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		for _, item := range s.Components {
			for _, id := range item.Offers {
				linked[id] = true
				if item.Role != "backup" {
					primary[id] = true
				}
			}
		}
	}
	for _, o := range r.Offers {
		if (o.Disposition == "component") != primary[o.ID] || (o.Disposition == "backup") != (linked[o.ID] && !primary[o.ID]) {
			return fmt.Errorf("disposition does not match references for %s", o.ID)
		}
	}
	for _, m := range r.Merges {
		if !solutions[m.SolutionID] || !hasText(m.Variants, m.Reason) {
			return errors.New("invalid merge record")
		}
	}
	return nil
}

func (r *SolutionReport) Audit() ReportAudit {
	a := ReportAudit{Version: r.Version, AnalyzedAt: r.AnalyzedAt, Snapshot: r.Snapshot, Merges: r.Merges}
	counts := map[string]*Coverage{}
	for _, o := range r.Offers {
		c := counts[o.Type]
		if c == nil {
			c = &Coverage{Type: o.Type, Uses: map[string]int{}, Details: map[string]int{}}
			counts[o.Type] = c
		}
		c.Total++
		c.Uses[o.Disposition]++
		c.Details[o.DetailState]++
		if o.Cost == nil {
			c.Unpriced++
		}
	}
	for _, c := range counts {
		a.Coverage = append(a.Coverage, *c)
	}
	sort.Slice(a.Coverage, func(i, j int) bool { return a.Coverage[i].Type < a.Coverage[j].Type })
	refs := r.references()
	for _, s := range r.Solutions {
		if summarize(s, refs).Incomplete {
			a.Incomplete++
		} else {
			a.Valid++
		}
		if s.Delivery == "verified" {
			a.Verified++
		}
	}
	return a
}
