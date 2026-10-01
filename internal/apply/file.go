// Package apply is the declarative form of a project: the file `vink
// apply` sends and `vink export` writes, its JSON Schema, and the diff
// the server answers with. The service applies it; this package only
// knows the shapes.
package apply

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"gopkg.in/yaml.v3"

	"github.com/w4jnl/vink/internal/domain"
)

//go:embed apply-schema.json
var schemaJSON []byte

// Schema returns the JSON Schema of the file, for docs and clients.
func Schema() []byte { return schemaJSON }

// File is the apply document. JSON and YAML use the same names.
type File struct {
	Version     int           `json:"version" yaml:"version"`
	Project     *Project      `json:"project,omitempty" yaml:"project,omitempty"`
	Channels    []Channel     `json:"channels,omitempty" yaml:"channels,omitempty"`
	Routes      []Route       `json:"routes,omitempty" yaml:"routes,omitempty"`
	Maintenance []Maintenance `json:"maintenance,omitempty" yaml:"maintenance,omitempty"`
	Monitors    []Monitor     `json:"monitors,omitempty" yaml:"monitors,omitempty"`
	StatusPages []StatusPage  `json:"status_pages,omitempty" yaml:"status_pages,omitempty"`
}

// Project names the project; slug must match the one applied to.
type Project struct {
	Slug     string `json:"slug,omitempty" yaml:"slug,omitempty"`
	Name     string `json:"name,omitempty" yaml:"name,omitempty"`
	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
}

// Channel is flat: the kind's fields sit beside name and kind.
type Channel struct {
	Name    string
	Kind    string
	Enabled *bool
	Config  map[string]any
}

func (c Channel) flat() map[string]any {
	m := make(map[string]any, len(c.Config)+3)
	for k, v := range c.Config {
		m[k] = v
	}
	m["name"], m["kind"] = c.Name, c.Kind
	if c.Enabled != nil && !*c.Enabled {
		m["enabled"] = false
	}
	return m
}

func (c *Channel) fromFlat(m map[string]any) {
	c.Name, _ = m["name"].(string)
	c.Kind, _ = m["kind"].(string)
	if e, ok := m["enabled"].(bool); ok {
		c.Enabled = &e
	}
	c.Config = map[string]any{}
	for k, v := range m {
		if k != "name" && k != "kind" && k != "enabled" {
			c.Config[k] = v
		}
	}
}

func (c Channel) MarshalJSON() ([]byte, error) { return json.Marshal(c.flat()) }

func (c *Channel) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	c.fromFlat(m)
	return nil
}

// MarshalYAML keeps name and kind first, then the kind's fields by name.
func (c Channel) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
	add := func(k string, v any) error {
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			return err
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
		return nil
	}
	if err := add("name", c.Name); err != nil {
		return nil, err
	}
	if err := add("kind", c.Kind); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(c.Config))
	for k := range c.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := add(k, c.Config[k]); err != nil {
			return nil, err
		}
	}
	if c.Enabled != nil && !*c.Enabled {
		if err := add("enabled", false); err != nil {
			return nil, err
		}
	}
	return node, nil
}

// ConfigJSON is the channel config as the notifiers read it.
func (c Channel) ConfigJSON() (json.RawMessage, error) {
	if c.Config == nil {
		return json.RawMessage("{}"), nil
	}
	return json.Marshal(c.Config)
}

// Route names its channels.
type Route struct {
	MatchTags   []string        `json:"match_tags,omitempty" yaml:"match_tags,flow,omitempty"`
	Channels    []string        `json:"channels" yaml:"channels,flow"`
	On          []domain.State  `json:"on,omitempty" yaml:"on,flow,omitempty"`
	RepeatEvery domain.Duration `json:"repeat_every,omitempty" yaml:"repeat_every,omitempty"`
	Priority    int             `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// Maintenance is one window: once (starts_at, ends_at) or weekly (rrule, from, to).
type Maintenance struct {
	Name      string     `json:"name" yaml:"name"`
	MatchTags []string   `json:"match_tags,omitempty" yaml:"match_tags,flow,omitempty"`
	StartsAt  *time.Time `json:"starts_at,omitempty" yaml:"starts_at,omitempty"`
	EndsAt    *time.Time `json:"ends_at,omitempty" yaml:"ends_at,omitempty"`
	RRule     string     `json:"rrule,omitempty" yaml:"rrule,omitempty"`
	From      string     `json:"from,omitempty" yaml:"from,omitempty"`
	To        string     `json:"to,omitempty" yaml:"to,omitempty"`
	Timezone  string     `json:"timezone,omitempty" yaml:"timezone,omitempty"`
}

// ToDomain converts a window; a bad rrule is a validation error.
func (m Maintenance) ToDomain() (*domain.Maintenance, error) {
	w := &domain.Maintenance{Name: m.Name, MatchTags: m.MatchTags, StartsAt: m.StartsAt, EndsAt: m.EndsAt, From: m.From, To: m.To, Timezone: m.Timezone}
	if m.RRule != "" {
		days, err := domain.ParseRRule(m.RRule)
		if err != nil {
			return nil, (&domain.ValidationError{Errors: []domain.FieldError{{Field: "rrule", Msg: err.Error()}}}).OrNil()
		}
		w.Weekly, w.Days = true, days
	}
	return w, nil
}

// MaintenanceFrom converts a stored window.
func MaintenanceFrom(w *domain.Maintenance) Maintenance {
	m := Maintenance{Name: w.Name, MatchTags: w.MatchTags, Timezone: w.Timezone}
	if w.Weekly {
		m.RRule, m.From, m.To = w.RRule(), w.From, w.To
	} else {
		m.StartsAt, m.EndsAt = w.StartsAt, w.EndsAt
	}
	return m
}

// Monitor is the flat monitor from the design document.
type Monitor struct {
	Slug              string            `json:"slug" yaml:"slug"`
	Name              string            `json:"name,omitempty" yaml:"name,omitempty"`
	Kind              domain.Kind       `json:"kind,omitempty" yaml:"kind,omitempty"`
	Tags              []string          `json:"tags,omitempty" yaml:"tags,flow,omitempty"`
	Schedule          *domain.Schedule  `json:"schedule,omitempty" yaml:"schedule,flow,omitempty"`
	Timezone          string            `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Grace             domain.Duration   `json:"grace,omitempty" yaml:"grace,omitempty"`
	MaxRuntime        domain.Duration   `json:"max_runtime,omitempty" yaml:"max_runtime,omitempty"`
	FailureThreshold  int               `json:"failure_threshold,omitempty" yaml:"failure_threshold,omitempty"`
	RecoveryThreshold int               `json:"recovery_threshold,omitempty" yaml:"recovery_threshold,omitempty"`
	Methods           []string          `json:"methods,omitempty" yaml:"methods,flow,omitempty"`
	BodyLimit         int64             `json:"body_limit,omitempty" yaml:"body_limit,omitempty"`
	Interval          domain.Duration   `json:"interval,omitempty" yaml:"interval,omitempty"`
	Timeout           domain.Duration   `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Confirm           *domain.Confirm   `json:"confirm,omitempty" yaml:"confirm,flow,omitempty"`
	Location          string            `json:"location,omitempty" yaml:"location,omitempty"`
	HTTP              *domain.HTTPCheck `json:"http,omitempty" yaml:"http,omitempty"`
	TCP               *domain.TCPCheck  `json:"tcp,omitempty" yaml:"tcp,omitempty"`
	DNS               *domain.DNSCheck  `json:"dns,omitempty" yaml:"dns,omitempty"`
	TLS               *domain.TLSCheck  `json:"tls,omitempty" yaml:"tls,omitempty"`
	ICMP              *domain.ICMPCheck `json:"icmp,omitempty" yaml:"icmp,omitempty"`
}

// ToDomain builds the monitor with the spec its kind takes.
func (m Monitor) ToDomain() *domain.Monitor {
	d := &domain.Monitor{Slug: m.Slug, Name: m.Name, Kind: m.Kind, Tags: m.Tags}
	if d.Kind == "" {
		d.Kind = domain.KindHeartbeat
	}
	if d.Kind.IsPull() {
		spec := &domain.PullSpec{
			Interval: m.Interval, Timeout: m.Timeout, FailureThreshold: m.FailureThreshold, RecoveryThreshold: m.RecoveryThreshold,
			Location: m.Location, HTTP: m.HTTP, TCP: m.TCP, DNS: m.DNS, TLS: m.TLS, ICMP: m.ICMP,
		}
		if m.Confirm != nil {
			spec.Confirm = *m.Confirm
		}
		d.Pull = spec
		return d
	}
	spec := &domain.HeartbeatSpec{Timezone: m.Timezone, Grace: m.Grace, MaxRuntime: m.MaxRuntime, FailureThreshold: m.FailureThreshold, RecoveryThreshold: m.RecoveryThreshold, Methods: m.Methods, BodyLimit: m.BodyLimit}
	if m.Schedule != nil {
		spec.Schedule = *m.Schedule
	}
	d.Heartbeat = spec
	return d
}

// MonitorFrom writes a stored monitor in the file's form, defaults left out.
func MonitorFrom(d *domain.Monitor) Monitor {
	m := Monitor{Slug: d.Slug, Kind: d.Kind, Tags: d.Tags}
	if d.Name != d.Slug {
		m.Name = d.Name
	}
	if s := d.Heartbeat; s != nil {
		sched := s.Schedule
		m.Schedule = &sched
		m.Timezone, m.MaxRuntime, m.Methods, m.BodyLimit = s.Timezone, s.MaxRuntime, s.Methods, s.BodyLimit
		if s.Grace != domain.DefaultGrace {
			m.Grace = s.Grace
		}
		if s.FailureThreshold != 1 {
			m.FailureThreshold = s.FailureThreshold
		}
		if s.RecoveryThreshold != 1 {
			m.RecoveryThreshold = s.RecoveryThreshold
		}
	}
	if s := d.Pull; s != nil {
		m.Location = s.Location
		if s.Interval != domain.DefaultInterval {
			m.Interval = s.Interval
		}
		shrunk := s.Timeout == s.Interval/2 && domain.DefaultCheckTimeout >= s.Interval
		if s.Timeout != domain.DefaultCheckTimeout && !shrunk {
			m.Timeout = s.Timeout
		}
		if s.FailureThreshold != domain.DefaultPullThreshold {
			m.FailureThreshold = s.FailureThreshold
		}
		if s.RecoveryThreshold != 1 {
			m.RecoveryThreshold = s.RecoveryThreshold
		}
		if s.Confirm.Retries != domain.DefaultConfirmRetries || s.Confirm.Delay != domain.DefaultConfirmDelay {
			c := s.Confirm
			m.Confirm = &c
		}
		m.HTTP, m.TCP, m.DNS, m.TLS, m.ICMP = trimHTTP(s.HTTP), s.TCP, s.DNS, trimTLS(s.TLS), trimICMP(s.ICMP)
	}
	return m
}

func trimHTTP(h *domain.HTTPCheck) *domain.HTTPCheck {
	if h == nil {
		return nil
	}
	c := *h
	if c.Method == "GET" {
		c.Method = ""
	}
	if len(c.ExpectStatus) == 1 && c.ExpectStatus[0] == (domain.StatusRange{Lo: 200, Hi: 299}) {
		c.ExpectStatus = nil
	}
	if c.Redirects() {
		c.FollowRedirects = nil
	}
	if c.Verify() {
		c.VerifyTLS = nil
	}
	if c.ExpectBody.IsZero() {
		c.ExpectBody = nil
	}
	return &c
}

func trimTLS(t *domain.TLSCheck) *domain.TLSCheck {
	if t == nil {
		return nil
	}
	c := *t
	if c.Port == 443 {
		c.Port = 0
	}
	if c.WarnDays == domain.DefaultTLSWarnDays {
		c.WarnDays = 0
	}
	if c.CritDays == domain.DefaultTLSCritDays {
		c.CritDays = 0
	}
	return &c
}

func trimICMP(i *domain.ICMPCheck) *domain.ICMPCheck {
	if i == nil {
		return nil
	}
	c := *i
	if c.Count == domain.DefaultICMPCount {
		c.Count = 0
	}
	if c.LossThreshold == domain.DefaultICMPLoss {
		c.LossThreshold = 0
	}
	return &c
}

// StatusPage is a page; the password is write-only and never exported.
type StatusPage struct {
	Slug         string   `json:"slug" yaml:"slug"`
	Title        string   `json:"title" yaml:"title"`
	MatchTags    []string `json:"match_tags,omitempty" yaml:"match_tags,flow,omitempty"`
	Public       *bool    `json:"public,omitempty" yaml:"public,omitempty"`
	Password     string   `json:"password,omitempty" yaml:"password,omitempty"`
	CustomDomain string   `json:"custom_domain,omitempty" yaml:"custom_domain,omitempty"`
}

// ToDomain converts a page; the password comes back separately.
func (p StatusPage) ToDomain() (*domain.StatusPage, string) {
	d := &domain.StatusPage{Slug: p.Slug, Title: p.Title, MatchTags: p.MatchTags, CustomDomain: p.CustomDomain, Public: true}
	if p.Public != nil {
		d.Public = *p.Public
	}
	if p.Password != "" {
		d.Public = false
	}
	return d, p.Password
}

// Diff is what apply answers: one line per resource, "monitor api-health".
type Diff struct {
	DryRun    bool     `json:"dry_run"`
	Created   []string `json:"created"`
	Updated   []string `json:"updated"`
	Recreated []string `json:"recreated"`
	Deleted   []string `json:"deleted"`
	Unchanged []string `json:"unchanged"`
}

// Changes counts what is not unchanged.
func (d *Diff) Changes() int {
	return len(d.Created) + len(d.Updated) + len(d.Recreated) + len(d.Deleted)
}

var varRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces ${VAR} from lookup. An unset variable is an error, so
// a missing token never lands in the server as an empty string.
func Expand(data []byte, lookup func(string) (string, bool)) ([]byte, error) {
	var missing []string
	out := varRe.ReplaceAllFunc(data, func(m []byte) []byte {
		name := string(varRe.FindSubmatch(m)[1])
		v, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
			return m
		}
		return []byte(v)
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("environment variable%s not set: %s", plural(len(missing)), strings.Join(missing, ", "))
	}
	return out, nil
}

// ExpandEnv expands from the process environment.
func ExpandEnv(data []byte) ([]byte, error) { return Expand(data, os.LookupEnv) }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Decode reads YAML or JSON into a generic document.
func Decode(data []byte) (any, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// round-trip through JSON so numbers and maps have JSON shapes
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var (
	compiled = map[string]*jsonschema.Schema{}
	printer  = message.NewPrinter(language.English)
)

// schema compiles one branch of the document schema: project_file or
// org_file. Validating the branch the document asks for keeps the
// errors about that shape alone.
func schema(branch string) (*jsonschema.Schema, error) {
	if sch, ok := compiled[branch]; ok {
		return sch, nil
	}
	var doc any
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("apply-schema.json", doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile("apply-schema.json#/$defs/" + branch)
	if err != nil {
		return nil, err
	}
	compiled[branch] = sch
	return sch, nil
}

// IsOrgDocument reports whether a decoded document is an org file: it
// lists projects rather than describing one.
func IsOrgDocument(doc any) bool {
	m, ok := doc.(map[string]any)
	if !ok {
		return false
	}
	_, has := m["projects"]
	return has
}

// Validate checks a decoded document against the schema of its shape.
func Validate(doc any) error {
	branch := "project_file"
	if IsOrgDocument(doc) {
		branch = "org_file"
	}
	sch, err := schema(branch)
	if err != nil {
		return err
	}
	if err := sch.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return errors.New(summarise(ve))
		}
		return err
	}
	return nil
}

// summarise turns the validator's tree into one line per leaf cause.
func summarise(ve *jsonschema.ValidationError) string {
	var lines []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			lines = append(lines, loc+": "+e.ErrorKind.LocalizedString(printer))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(lines) > 8 {
		lines = append(lines[:8], fmt.Sprintf("and %d more", len(lines)-8))
	}
	return "apply file is not valid:\n  " + strings.Join(lines, "\n  ")
}

// Parse decodes YAML or JSON into a File. With check, the document is
// validated against the schema first. An org file is refused: use
// ParseOrg, or ParseAny when either shape may arrive.
func Parse(data []byte, check bool) (*File, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if IsOrgDocument(doc) {
		return nil, errors.New("this is an org file (it lists projects); apply it with an org key")
	}
	if check {
		if err := Validate(doc); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var f File
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if f.Version == 0 {
		f.Version = 1
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("version %d is not supported; this vink writes version 1", f.Version)
	}
	return &f, nil
}

// Encode writes the file as YAML with two-space indent.
func Encode(f *File) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// OrgFile is the apply document of a whole org: one entry per project,
// each the same shape as a project file. `vink export --org` writes it
// and an org key applies it.
type OrgFile struct {
	Version  int            `json:"version" yaml:"version"`
	Org      string         `json:"org" yaml:"org"`
	Projects []ProjectEntry `json:"projects" yaml:"projects"`
}

// ProjectEntry is one project inside an org file.
type ProjectEntry struct {
	Slug        string        `json:"slug" yaml:"slug"`
	Name        string        `json:"name,omitempty" yaml:"name,omitempty"`
	Timezone    string        `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Channels    []Channel     `json:"channels,omitempty" yaml:"channels,omitempty"`
	Routes      []Route       `json:"routes,omitempty" yaml:"routes,omitempty"`
	Maintenance []Maintenance `json:"maintenance,omitempty" yaml:"maintenance,omitempty"`
	Monitors    []Monitor     `json:"monitors,omitempty" yaml:"monitors,omitempty"`
	StatusPages []StatusPage  `json:"status_pages,omitempty" yaml:"status_pages,omitempty"`
}

// File is the entry as a project file, the shape the project apply takes.
func (e ProjectEntry) File() *File {
	return &File{
		Version: 1, Project: &Project{Slug: e.Slug, Name: e.Name, Timezone: e.Timezone},
		Channels: e.Channels, Routes: e.Routes, Maintenance: e.Maintenance, Monitors: e.Monitors, StatusPages: e.StatusPages,
	}
}

// EntryFrom turns an exported project file into an org file entry.
func EntryFrom(f *File) ProjectEntry {
	e := ProjectEntry{Channels: f.Channels, Routes: f.Routes, Maintenance: f.Maintenance, Monitors: f.Monitors, StatusPages: f.StatusPages}
	if f.Project != nil {
		e.Slug, e.Name, e.Timezone = f.Project.Slug, f.Project.Name, f.Project.Timezone
	}
	return e
}

// ParseOrg decodes an org file, validated against the schema when check
// is set. A project file is refused.
func ParseOrg(data []byte, check bool) (*OrgFile, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	if !IsOrgDocument(doc) {
		return nil, errors.New("this is a project file; an org file lists projects")
	}
	if check {
		if err := Validate(doc); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var f OrgFile
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&f); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if f.Version == 0 {
		f.Version = 1
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("version %d is not supported; this vink writes version 1", f.Version)
	}
	return &f, nil
}

// ParseAny decodes either shape; exactly one of the results is set.
func ParseAny(data []byte, check bool) (*File, *OrgFile, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, nil, err
	}
	if IsOrgDocument(doc) {
		o, err := ParseOrg(data, check)
		return nil, o, err
	}
	f, err := Parse(data, check)
	return f, nil, err
}

// EncodeOrg writes the org file as YAML.
func EncodeOrg(f *OrgFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// OrgDiff is what an org apply did, per project.
type OrgDiff struct {
	DryRun   bool          `json:"dry_run"`
	Org      string        `json:"org"`
	Projects []ProjectDiff `json:"projects"`
}

// ProjectDiff is one project's diff inside an org apply; Created says
// the project itself was created.
type ProjectDiff struct {
	Slug    string `json:"slug"`
	Created bool   `json:"project_created"`
	Diff
}

// Changes counts what is not unchanged across every project.
func (d *OrgDiff) Changes() int {
	n := 0
	for _, p := range d.Projects {
		n += p.Changes()
		if p.Created {
			n++
		}
	}
	return n
}
