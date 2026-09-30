package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestResolveRole(t *testing.T) {
	rm := map[string]string{
		"CN=MM Admins,OU=Groups,DC=corp,DC=example,DC=com": "admin",
		"mm-operators": "operator",
		"CN=MM Read,OU=Groups,DC=corp,DC=example,DC=com": "readonly",
	}
	cases := []struct {
		name   string
		groups []string
		want   string
	}{
		{"none", []string{"CN=Other,OU=Groups,DC=corp,DC=example,DC=com"}, ""},
		{"empty", nil, ""},
		{"admin exact", []string{"CN=MM Admins,OU=Groups,DC=corp,DC=example,DC=com"}, "admin"},
		{"admin case and spacing", []string{"cn=mm admins, ou=groups, dc=CORP,dc=example,dc=com"}, "admin"},
		{"bare cn", []string{"CN=MM-Operators,OU=X,DC=corp"}, "operator"},
		{"highest wins", []string{"CN=MM Read,OU=Groups,DC=corp,DC=example,DC=com", "CN=mm-operators,OU=X,DC=corp"}, "operator"},
		{"same cn wrong ou is not a full-DN match", []string{"CN=MM Admins,OU=Evil,DC=corp,DC=example,DC=com"}, ""},
		{"no nested/partial match", []string{"CN=MM Admins Extra,OU=Groups,DC=corp,DC=example,DC=com"}, ""},
	}
	for _, c := range cases {
		if got := ResolveRole(rm, c.groups); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestNewValidation(t *testing.T) {
	ok := Config{URL: "ldaps://dc.example.com", UserBase: "DC=example,DC=com", RoleMap: map[string]string{"g": "admin"}}
	if _, err := New(&ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]Config{
		"plain ldap":   {URL: "ldap://dc.example.com", UserBase: "x", RoleMap: ok.RoleMap},
		"no scheme":    {URL: "dc.example.com", UserBase: "x", RoleMap: ok.RoleMap},
		"no user base": {URL: ok.URL, RoleMap: ok.RoleMap},
		"no role map":  {URL: ok.URL, UserBase: "x"},
		"bad role":     {URL: ok.URL, UserBase: "x", RoleMap: map[string]string{"g": "root"}},
		"bad filter":   {URL: ok.URL, UserBase: "x", RoleMap: ok.RoleMap, UserFilter: "(uid=bob)"},
		"group filter": {URL: ok.URL, UserBase: "x", RoleMap: ok.RoleMap, GroupFilter: "(member=%s)"},
	}
	for n, c := range bad {
		c := c
		if _, err := New(&c); err == nil {
			t.Errorf("%s: expected error", n)
		}
	}
}

func TestEmptyPasswordRejectedWithoutDialling(t *testing.T) {
	c := Config{URL: "ldaps://127.0.0.1:1", UserBase: "x", RoleMap: map[string]string{"g": "admin"}}
	l, _ := New(&c)
	for _, in := range [][2]string{{"bob", ""}, {"", "pw"}} {
		if _, err := l.Authenticate(in[0], in[1]); err != ErrInvalidCredentials {
			t.Errorf("%v: got %v want ErrInvalidCredentials", in, err)
		}
	}
}

func TestTraceReportsProgressAndFailsFast(t *testing.T) {
	c := Config{URL: "ldaps://127.0.0.1:1", UserBase: "x", RoleMap: map[string]string{"g": "admin"}, TimeoutSeconds: 3}
	l, err := New(&c)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	l.Trace = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
	start := time.Now()
	_, err = l.Authenticate("bob", "pw")
	var de *DirectoryError
	if !errors.As(err, &de) {
		t.Fatalf("want a DirectoryError, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("unreachable directory must fail within the timeout, took %s", time.Since(start))
	}
	if len(lines) == 0 || !strings.Contains(lines[0], "connecting to ldaps://127.0.0.1:1") {
		t.Errorf("progress not reported: %v", lines)
	}
}
