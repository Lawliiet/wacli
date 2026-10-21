package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Rule-based automatic media download: a JSON rules file decides, per live
// message, whether its media is fetched the moment it arrives.

const downloadRulesVersion = 1

// DownloadMediaTypes are the media types wacli records on messages.
var DownloadMediaTypes = []string{"image", "video", "gif", "audio", "sticker", "document"}

var downloadChatKinds = []string{"dm", "group", "channel", "status"}

// StringList decodes either a JSON string or a JSON array of strings.
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a string or a list of strings")
	}
	*s = StringList(many)
	return nil
}

type DownloadMatch struct {
	Chat StringList `json:"chat,omitempty"`
	Kind StringList `json:"kind,omitempty"`
	List StringList `json:"list,omitempty"`
}

type DownloadRule struct {
	Name       string        `json:"name,omitempty"`
	Match      DownloadMatch `json:"match"`
	Types      []string      `json:"types,omitempty"`
	Extensions []string      `json:"extensions,omitempty"`
	Mime       StringList    `json:"mime,omitempty"`
	MaxBytes   int64         `json:"max_bytes,omitempty"`
	Download   *bool         `json:"download,omitempty"`
}

type DownloadLimits struct {
	TotalBytes int64 `json:"total_bytes,omitempty"`
	KeepDays   int   `json:"keep_days,omitempty"`
}

type DownloadRules struct {
	Version int            `json:"version"`
	Rules   []DownloadRule `json:"rules"`
	Limits  DownloadLimits `json:"limits"`
}

// ParseDownloadRules strictly decodes and validates a rules file. Unknown keys
// are errors so a typo cannot silently weaken a rule.
func ParseDownloadRules(data []byte) (*DownloadRules, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r DownloadRules
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	if dec.More() {
		return nil, errors.New("parse rules: unexpected data after the JSON object")
	}
	if r.Version != downloadRulesVersion {
		return nil, fmt.Errorf("rules: version must be %d (got %d)", downloadRulesVersion, r.Version)
	}
	for i := range r.Rules {
		if err := r.Rules[i].normalize(); err != nil {
			label := fmt.Sprintf("rule %d", i+1)
			if r.Rules[i].Name != "" {
				label += fmt.Sprintf(" (%s)", r.Rules[i].Name)
			}
			return nil, fmt.Errorf("%s: %w", label, err)
		}
	}
	if r.Limits.TotalBytes < 0 {
		return nil, errors.New("limits.total_bytes must be >= 0")
	}
	if r.Limits.KeepDays < 0 {
		return nil, errors.New("limits.keep_days must be >= 0")
	}
	return &r, nil
}

func inSet(v string, set []string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

func (r *DownloadRule) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	for i, c := range r.Match.Chat {
		jid, err := types.ParseJID(strings.TrimSpace(c))
		if err != nil || jid.IsEmpty() || jid.User == "" {
			return fmt.Errorf("match.chat %q is not a JID", c)
		}
		r.Match.Chat[i] = jid.String()
	}
	for i, k := range r.Match.Kind {
		k = strings.ToLower(strings.TrimSpace(k))
		if !inSet(k, downloadChatKinds) {
			return fmt.Errorf("match.kind %q must be one of %s", k, strings.Join(downloadChatKinds, ", "))
		}
		r.Match.Kind[i] = k
	}
	for i, l := range r.Match.List {
		l = strings.TrimSpace(l)
		if l == "" {
			return errors.New("match.list must not be empty")
		}
		r.Match.List[i] = l
	}
	for i, t := range r.Types {
		t = strings.ToLower(strings.TrimSpace(t))
		if !inSet(t, DownloadMediaTypes) {
			return fmt.Errorf("types: %q must be one of %s", t, strings.Join(DownloadMediaTypes, ", "))
		}
		r.Types[i] = t
	}
	for i, e := range r.Extensions {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e == "" {
			return errors.New("extensions must not contain an empty entry")
		}
		r.Extensions[i] = e
	}
	for i, m := range r.Mime {
		m = strings.ToLower(strings.TrimSpace(m))
		if _, err := path.Match(m, "x/y"); err != nil || m == "" {
			return fmt.Errorf("mime %q is not a valid glob", m)
		}
		r.Mime[i] = m
	}
	if r.MaxBytes < 0 {
		return errors.New("max_bytes must be >= 0")
	}
	return nil
}

// DownloadCandidate describes one incoming media message.
type DownloadCandidate struct {
	Chat      string // canonical chat JID
	Kind      string // dm | group | channel | status | other
	MediaType string // as stored: image, video, gif, audio, sticker, document
	Size      uint64 // declared file length; 0 = unknown
	Filename  string // used for the extension
	Mime      string
	Exts      []string // extra extensions to consider (test command)
}

// DownloadDecision is the outcome of matching a candidate against the rules.
type DownloadDecision struct {
	Download bool   `json:"download"`
	Rule     string `json:"rule,omitempty"`  // name, or "#N" when unnamed
	Index    int    `json:"index,omitempty"` // 1-based; 0 = no rule matched
	Reason   string `json:"reason"`
}

// ListMembership reports whether a chat is in the list given by id or name.
type ListMembership func(ref, chat string) (bool, error)

// ChatKindForJID maps a chat JID to the rule kinds.
func ChatKindForJID(j types.JID) string {
	switch {
	case j == types.StatusBroadcastJID:
		return "status"
	case j.Server == types.GroupServer:
		return "group"
	case j.Server == types.NewsletterServer:
		return "channel"
	case j.Server == types.DefaultUserServer || j.Server == types.HiddenUserServer:
		return "dm"
	}
	return "other"
}

func (r DownloadRule) label(i int) string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("#%d", i+1)
}

func (r DownloadRule) matches(c DownloadCandidate, lists ListMembership) (bool, error) {
	m := r.Match
	// Status broadcasts are never downloaded unless a rule asks for them by
	// kind or by chat, so an empty match cannot sweep them in.
	if c.Kind == "status" && !inSet("status", m.Kind) && !inSet(c.Chat, m.Chat) {
		return false, nil
	}
	if len(m.Chat) > 0 && !inSet(c.Chat, m.Chat) {
		return false, nil
	}
	if len(m.Kind) > 0 && !inSet(c.Kind, m.Kind) {
		return false, nil
	}
	if len(m.List) > 0 {
		any := false
		for _, ref := range m.List {
			if lists == nil {
				break
			}
			ok, err := lists(ref, c.Chat)
			if err != nil {
				return false, err
			}
			if ok {
				any = true
				break
			}
		}
		if !any {
			return false, nil
		}
	}
	if len(r.Types) > 0 && !inSet(strings.ToLower(c.MediaType), r.Types) {
		return false, nil
	}
	if len(r.Extensions) > 0 {
		ok := false
		for _, e := range candidateExtensions(c) {
			if inSet(e, r.Extensions) {
				ok = true
				break
			}
		}
		if !ok {
			return false, nil
		}
	}
	if len(r.Mime) > 0 {
		mt := strings.ToLower(strings.TrimSpace(c.Mime))
		if i := strings.Index(mt, ";"); i >= 0 {
			mt = strings.TrimSpace(mt[:i])
		}
		ok := false
		for _, g := range r.Mime {
			if matched, _ := path.Match(g, mt); matched {
				ok = true
				break
			}
		}
		if !ok {
			return false, nil
		}
	}
	// An unknown (0) declared size passes: there is nothing to compare, and the
	// total-size limit still bounds what is kept.
	if r.MaxBytes > 0 && c.Size > uint64(r.MaxBytes) {
		return false, nil
	}
	return true, nil
}

func candidateExtensions(c DownloadCandidate) []string {
	out := append([]string(nil), c.Exts...)
	if e := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(c.Filename)), ".")); e != "" {
		return append(out, e)
	}
	if mt := strings.TrimSpace(c.Mime); mt != "" {
		if exts, err := mime.ExtensionsByType(mt); err == nil {
			for _, e := range exts {
				out = append(out, strings.TrimPrefix(strings.ToLower(e), "."))
			}
		}
	}
	return out
}

// Decide returns the outcome of the first rule that matches. Filters are part
// of matching: a message that fails a rule's types, extensions, mime or size
// falls through to the next rule. No match means no download. A list that
// cannot be resolved (unknown or ambiguous name) never matches.
func (rs *DownloadRules) Decide(c DownloadCandidate, lists ListMembership) DownloadDecision {
	if rs == nil {
		return DownloadDecision{Reason: "no rules loaded"}
	}
	for i, r := range rs.Rules {
		ok, err := r.matches(c, lists)
		if err != nil || !ok {
			continue
		}
		d := DownloadDecision{Download: r.Download == nil || *r.Download, Rule: r.label(i), Index: i + 1}
		if d.Download {
			d.Reason = "matched rule " + d.Rule
		} else {
			d.Reason = "excluded by rule " + d.Rule
		}
		return d
	}
	return DownloadDecision{Reason: "no rule matched"}
}

// downloadRulesSource serves the current rules from a file, re-reading it when
// its mtime or size changes. A broken edit keeps the last good rules; a missing
// file means no auto-download.
type downloadRulesSource struct {
	path string
	warn func(code, message string)

	mu      sync.Mutex
	mtime   time.Time
	size    int64
	seen    bool
	missing bool
	rules   *DownloadRules
}

func newDownloadRulesSource(p string, warn func(code, message string)) *downloadRulesSource {
	return &downloadRulesSource{path: p, warn: warn}
}

func (s *downloadRulesSource) Current() *DownloadRules {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if !s.missing {
				s.missing = true
				s.rules = nil
				s.seen = false
				s.warn("download_rules_missing", fmt.Sprintf("download rules file %s does not exist; no media is downloaded automatically", s.path))
			}
		} else if s.seen {
			s.warn("download_rules_unreadable", fmt.Sprintf("download rules file %s: %v; keeping the previous rules", s.path, err))
		}
		return s.rules
	}
	s.missing = false
	if s.seen && st.ModTime().Equal(s.mtime) && st.Size() == s.size {
		return s.rules
	}
	s.seen, s.mtime, s.size = true, st.ModTime(), st.Size()
	data, err := os.ReadFile(s.path)
	var rules *DownloadRules
	if err == nil {
		rules, err = ParseDownloadRules(data)
	}
	if err != nil {
		if s.rules != nil {
			s.warn("download_rules_invalid", fmt.Sprintf("download rules file %s is invalid, keeping the previous rules: %v", s.path, err))
		} else {
			s.warn("download_rules_invalid", fmt.Sprintf("download rules file %s is invalid, no media is downloaded automatically: %v", s.path, err))
		}
		return s.rules
	}
	s.rules = rules
	return s.rules
}

// LoadDownloadRulesFile reads and validates a rules file.
func LoadDownloadRulesFile(p string) (*DownloadRules, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return ParseDownloadRules(data)
}
