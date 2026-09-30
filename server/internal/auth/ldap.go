// Package auth implements directory (LDAPS / Active Directory) authentication with group-to-role mapping.
package auth

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// Config is loaded from a JSON file (see packaging/server/ldap.json.example).
type Config struct {
	URL              string            `json:"url"`           // ldaps://host:636 (plain ldap:// is refused)
	CAFile           string            `json:"ca_file"`       // PEM CA bundle used to verify the directory server
	ServerName       string            `json:"server_name"`   // optional TLS name override
	BindDN           string            `json:"bind_dn"`       // service account used to find the user and groups
	BindPassword     string            `json:"bind_password"` // or use bind_password_file
	BindPasswordFile string            `json:"bind_password_file"`
	UserBase         string            `json:"user_base"`
	UserFilter       string            `json:"user_filter"`  // %s = escaped login name; default (sAMAccountName=%s)
	GroupAttr        string            `json:"group_attr"`   // attribute on the user listing groups; default memberOf
	GroupBase        string            `json:"group_base"`   // optional: search groups instead of/in addition to group_attr
	GroupFilter      string            `json:"group_filter"` // %s = escaped user DN, e.g. (&(objectClass=groupOfNames)(member=%s))
	RoleMap          map[string]string `json:"role_map"`     // group DN (or bare CN) -> admin|operator|readonly
	TimeoutSeconds   int               `json:"timeout_seconds"`
}

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNoRole             = errors.New("user is not a member of any group mapped to a role")
)

var roleRank = map[string]int{"readonly": 1, "operator": 2, "admin": 3}

// Result of a successful authentication.
type Result struct {
	DN     string
	Groups []string
	Role   string
}

type LDAP struct {
	cfg Config
	tls *tls.Config
	// Trace, when set, receives a line per step (used by `mmserver -ldap-check` so a slow or failing directory is visible).
	Trace func(format string, args ...any)
}

func (l *LDAP) tracef(format string, args ...any) {
	if l.Trace != nil {
		l.Trace(format, args...)
	}
}

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.BindPasswordFile != "" {
		pw, err := os.ReadFile(c.BindPasswordFile)
		if err != nil {
			return nil, err
		}
		c.BindPassword = strings.TrimRight(string(pw), "\r\n")
	}
	return &c, nil
}

func New(c *Config) (*LDAP, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "ldaps" || u.Host == "" {
		return nil, errors.New("ldap url must be ldaps://host[:port]")
	}
	if c.UserBase == "" {
		return nil, errors.New("user_base is required")
	}
	if len(c.RoleMap) == 0 {
		return nil, errors.New("role_map is required")
	}
	for g, r := range c.RoleMap {
		if roleRank[r] == 0 {
			return nil, fmt.Errorf("role_map[%q]: unknown role %q (admin|operator|readonly)", g, r)
		}
	}
	if c.UserFilter == "" {
		c.UserFilter = "(sAMAccountName=%s)"
	}
	if strings.Count(c.UserFilter, "%s") != 1 {
		return nil, errors.New("user_filter must contain exactly one %s")
	}
	if c.GroupFilter != "" && (strings.Count(c.GroupFilter, "%s") != 1 || c.GroupBase == "") {
		return nil, errors.New("group_filter needs exactly one %s and a group_base")
	}
	if c.GroupAttr == "" {
		c.GroupAttr = "memberOf"
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 10
	}
	t := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if t.ServerName == "" {
		t.ServerName = u.Hostname()
	}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file contains no certificates")
		}
		t.RootCAs = pool
	}
	return &LDAP{cfg: *c, tls: t}, nil
}

func (l *LDAP) dial() (*ldap.Conn, error) {
	d := time.Duration(l.cfg.TimeoutSeconds) * time.Second
	c, err := ldap.DialURL(l.cfg.URL, ldap.DialWithTLSConfig(l.tls), ldap.DialWithDialer(&net.Dialer{Timeout: d}))
	if err != nil {
		return nil, err
	}
	c.SetTimeout(d)
	return c, nil
}

// Authenticate verifies the password against the directory and resolves the user's role.
// A DirectoryError means the directory could not be used (as opposed to bad credentials).
func (l *LDAP) Authenticate(user, password string) (*Result, error) {
	// An empty password would turn the final bind into an unauthenticated bind, which many servers accept.
	if user == "" || password == "" || len(user) > 256 || len(password) > 1024 {
		return nil, ErrInvalidCredentials
	}
	l.tracef("connecting to %s (TLS verified against %s, %ds timeout per step)", l.cfg.URL, l.caDescription(), l.cfg.TimeoutSeconds)
	c, err := l.dial()
	if err != nil {
		return nil, &DirectoryError{err}
	}
	defer c.Close()
	l.tracef("connected; TLS handshake and certificate verification succeeded")

	if l.cfg.BindDN != "" {
		l.tracef("binding as service account %s", l.cfg.BindDN)
		if err := c.Bind(l.cfg.BindDN, l.cfg.BindPassword); err != nil {
			return nil, &DirectoryError{fmt.Errorf("service bind failed: %w", err)}
		}
	} else {
		l.tracef("no bind_dn configured: searching anonymously")
	}
	l.tracef("searching %s with filter %s", l.cfg.UserBase, fmt.Sprintf(l.cfg.UserFilter, ldap.EscapeFilter(user)))
	attrs := []string{"dn", l.cfg.GroupAttr}
	sr, err := c.Search(ldap.NewSearchRequest(l.cfg.UserBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, l.cfg.TimeoutSeconds, false,
		fmt.Sprintf(l.cfg.UserFilter, ldap.EscapeFilter(user)), attrs, nil))
	if err != nil {
		return nil, &DirectoryError{fmt.Errorf("user search failed: %w", err)}
	}
	if len(sr.Entries) != 1 { // none, or ambiguous
		l.tracef("user search matched %d entries (need exactly 1): check user_base and user_filter", len(sr.Entries))
		return nil, ErrInvalidCredentials
	}
	dn := sr.Entries[0].DN
	l.tracef("found %s", dn)
	groups := append([]string{}, sr.Entries[0].GetAttributeValues(l.cfg.GroupAttr)...)

	if l.cfg.GroupFilter != "" {
		l.tracef("searching groups under %s", l.cfg.GroupBase)
		gs, err := c.Search(ldap.NewSearchRequest(l.cfg.GroupBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, l.cfg.TimeoutSeconds, false,
			fmt.Sprintf(l.cfg.GroupFilter, ldap.EscapeFilter(dn)), []string{"dn"}, nil))
		if err != nil {
			return nil, &DirectoryError{fmt.Errorf("group search failed: %w", err)}
		}
		for _, e := range gs.Entries {
			groups = append(groups, e.DN)
		}
	}
	// Verify the user's password last, on the same connection.
	l.tracef("verifying the password by binding as %s", dn)
	if err := c.Bind(dn, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, ErrInvalidCredentials
		}
		return nil, &DirectoryError{fmt.Errorf("user bind failed: %w", err)}
	}
	role := ResolveRole(l.cfg.RoleMap, groups)
	if role == "" {
		return &Result{DN: dn, Groups: groups}, ErrNoRole
	}
	return &Result{DN: dn, Groups: groups, Role: role}, nil
}

func (l *LDAP) caDescription() string {
	if l.cfg.CAFile != "" {
		return l.cfg.CAFile
	}
	return "the system trust store"
}

// DirectoryError wraps connectivity/configuration failures (not the user's fault).
type DirectoryError struct{ Err error }

func (e *DirectoryError) Error() string { return "directory error: " + e.Err.Error() }
func (e *DirectoryError) Unwrap() error { return e.Err }

func normDN(s string) string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, ",")
}

func bareCN(dn string) string {
	first := strings.SplitN(dn, ",", 2)[0]
	if i := strings.Index(first, "="); i >= 0 && strings.EqualFold(strings.TrimSpace(first[:i]), "cn") {
		return strings.ToLower(strings.TrimSpace(first[i+1:]))
	}
	return ""
}

// ResolveRole maps direct group memberships (no nesting) to the highest-ranked role.
// role_map keys may be a full group DN or a bare CN; matching is case-insensitive.
func ResolveRole(roleMap map[string]string, groups []string) string {
	best := ""
	for key, role := range roleMap {
		for _, g := range groups {
			match := false
			if strings.Contains(key, "=") {
				match = normDN(key) == normDN(g)
			} else {
				match = strings.EqualFold(strings.TrimSpace(key), bareCN(g))
			}
			if match && roleRank[role] > roleRank[best] {
				best = role
			}
		}
	}
	return best
}
