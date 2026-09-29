package api

// Database-backed tests for mount resolution and role enforcement. Skipped unless MM_TEST_DSN is set.
// Each run works in its own throwaway schema, so it is safe to point at a shared development database.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhoods/mountmanager/internal/db"
)

func testServer(t *testing.T) (*Server, context.Context) {
	dsn := getenv("MM_TEST_DSN")
	if dsn == "" {
		t.Skip("MM_TEST_DSN not set")
	}
	ctx := context.Background()
	b := make([]byte, 4)
	rand.Read(b)
	schema := "mmtest_" + hex.EncodeToString(b)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, db.Schema); err != nil {
		t.Fatal(err)
	}
	return New(pool, nil), ctx
}

type fx struct {
	s   *Server
	ctx context.Context
	t   *testing.T
}

func (f fx) one(q string, args ...any) int64 {
	var id int64
	if err := f.s.DB.QueryRow(f.ctx, q, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}
func (f fx) host(name string) int64 {
	return f.one(`INSERT INTO hosts(hostname) VALUES($1) RETURNING id`, name)
}
func (f fx) tpl(name, src, mp, opts string) int64 {
	return f.one(`INSERT INTO templates(name,source,mountpoint,options) VALUES($1,$2,$3,$4) RETURNING id`, name, src, mp, opts)
}
func (f fx) group(name string, prio int, rx string, tpls ...int64) int64 {
	g := f.one(`INSERT INTO groups(name,priority,host_regex) VALUES($1,$2,$3) RETURNING id`, name, prio, rx)
	for _, t := range tpls {
		if _, err := f.s.DB.Exec(f.ctx, `INSERT INTO group_templates VALUES($1,$2)`, g, t); err != nil {
			f.t.Fatal(err)
		}
	}
	return g
}
func (f fx) member(g, h int64) {
	if _, err := f.s.DB.Exec(f.ctx, `INSERT INTO group_members VALUES($1,$2)`, g, h); err != nil {
		f.t.Fatal(err)
	}
}
func (f fx) mounts(id int64, name string) map[string]Mount {
	m, err := f.s.DesiredMounts(f.ctx, id, name)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]Mount{}
	for _, x := range m {
		out[x.Mountpoint] = x
	}
	return out
}

func TestDesiredMountsResolution(t *testing.T) {
	s, ctx := testServer(t)
	f := fx{s, ctx, t}
	web1, web2, db1 := f.host("web-01.example.com"), f.host("web-02.example.com"), f.host("db-01.example.com")

	data := f.tpl("data-hard", "nfs1:/data", "/mnt/data", "rw,hard")
	dataSoft := f.tpl("data-soft", "nfs2:/data", "/mnt/data", "rw,soft")
	scratch := f.tpl("scratch", "nfs1:/scratch", "/mnt/scratch", "rw")
	weka := f.tpl("weka", "backend0/fs1", "/sqpc", "rw")
	f.group("everyone", 100, ".", data)
	f.group("web-override", 200, `^web-\d+\.`, dataSoft, scratch)
	f.group("weka-static", 50, "", weka)
	g := f.one(`SELECT id FROM groups WHERE name='weka-static'`)
	f.member(g, db1)

	// regex-matching hosts get the higher-priority template for the contested mountpoint
	m := f.mounts(web1, "web-01.example.com")
	if len(m) != 2 || m["/mnt/data"].Source != "nfs2:/data" || m["/mnt/data"].Options != "rw,soft" {
		t.Errorf("web-01: priority winner wrong: %+v", m)
	}
	if _, ok := m["/mnt/scratch"]; !ok {
		t.Errorf("web-01 missing scratch: %+v", m)
	}
	if m2 := f.mounts(web2, "web-02.example.com"); m2["/mnt/data"].Source != "nfs2:/data" {
		t.Errorf("web-02 should match the regex too: %+v", m2)
	}
	// non-matching host gets only the low-priority template plus its static group
	d := f.mounts(db1, "db-01.example.com")
	if d["/mnt/data"].Source != "nfs1:/data" || d["/sqpc"].Source != "backend0/fs1" || len(d) != 2 {
		t.Errorf("db-01: %+v", d)
	}
	// a host in no group gets nothing when the catch-all is removed
	if _, err := s.DB.Exec(ctx, `DELETE FROM groups WHERE name='everyone'`); err != nil {
		t.Fatal(err)
	}
	if m := f.mounts(db1, "db-01.example.com"); len(m) != 1 || m["/sqpc"].Source == "" {
		t.Errorf("after removing catch-all: %+v", m)
	}
	other := f.host("other-01.example.com")
	if m := f.mounts(other, "other-01.example.com"); len(m) != 0 {
		t.Errorf("ungrouped host should have no mounts: %+v", m)
	}
}

func TestDesiredMountsEdgeCases(t *testing.T) {
	s, ctx := testServer(t)
	f := fx{s, ctx, t}
	h := f.host("node-1")
	a := f.tpl("a", "s1:/x", "/mnt/x", "rw")
	b := f.tpl("b", "s2:/x", "/mnt/x", "ro")

	// equal priority: the lower group id wins, deterministically
	f.group("first", 100, "^node", a)
	f.group("second", 100, "^node", b)
	for i := 0; i < 5; i++ {
		if m := f.mounts(h, "node-1"); m["/mnt/x"].Source != "s1:/x" {
			t.Fatalf("tie-break not deterministic: %+v", m)
		}
	}
	// an invalid stored regex must not match anything or break resolution
	c := f.tpl("c", "s3:/y", "/mnt/y", "rw")
	f.group("broken", 300, "([", c)
	m := f.mounts(h, "node-1")
	if _, ok := m["/mnt/y"]; ok || len(m) != 1 {
		t.Errorf("invalid regex should be ignored: %+v", m)
	}
	// regex is unanchored, like the UI preview: "node" matches node-1
	f.group("unanchored", 1, "de-", f.tpl("d", "s4:/z", "/mnt/z", "rw"))
	if _, ok := f.mounts(h, "node-1")["/mnt/z"]; !ok {
		t.Errorf("unanchored regex should match")
	}
	// hash is stable for identical state and changes when a template changes
	x, _ := s.DesiredMounts(ctx, h, "node-1")
	y, _ := s.DesiredMounts(ctx, h, "node-1")
	if mountsHash(x) != mountsHash(y) {
		t.Errorf("hash unstable")
	}
	if _, err := s.DB.Exec(ctx, `UPDATE templates SET options='rw,soft' WHERE name='a'`); err != nil {
		t.Fatal(err)
	}
	z, _ := s.DesiredMounts(ctx, h, "node-1")
	if mountsHash(x) == mountsHash(z) {
		t.Errorf("hash should change when options change")
	}
}

func TestRoleEnforcement(t *testing.T) {
	s, ctx := testServer(t)
	for _, u := range []struct{ name, role string }{{"adm", "admin"}, {"op", "operator"}, {"ro", "readonly"}} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO users(username,pass_hash,role) VALUES($1,$2,$3)`, u.name, HashPassword("pw-"+u.name), u.role); err != nil {
			t.Fatal(err)
		}
	}
	h := s.AdminMux()
	do := func(method, path, token, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	login := func(u string) string {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"`+u+`","password":"pw-`+u+`"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out struct{ Token string }
		json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 || out.Token == "" {
			t.Fatalf("login %s: %d %s", u, w.Code, w.Body.String())
		}
		return out.Token
	}
	adm, op, ro := login("adm"), login("op"), login("ro")

	tpl := `{"name":"t1","fstype":"wekafs","source":"weka01/fs1","mountpoint":"/sqpc","options":"rw"}`
	grp := `{"name":"g1","priority":10}`
	type c struct {
		name, method, path, body string
		tok                      map[string]int // token label -> expected status
	}
	labels := map[string]string{"none": "", "ro": ro, "op": op, "adm": adm}
	cases := []c{
		{"read hosts", "GET", "/api/hosts", "", map[string]int{"none": 401, "ro": 200, "op": 200, "adm": 200}},
		{"write template", "POST", "/api/templates", tpl, map[string]int{"none": 401, "ro": 403, "op": 200, "adm": 200}},
		{"write group", "POST", "/api/groups", grp, map[string]int{"none": 401, "ro": 403, "op": 200, "adm": 200}},
		{"create enrolment token", "POST", "/api/tokens", `{"note":"x"}`, map[string]int{"none": 401, "ro": 403, "op": 403, "adm": 200}},
		{"bad template rejected", "POST", "/api/templates", `{"name":"bad","fstype":"ext4","source":"/dev/sda1","mountpoint":"/mnt/x"}`, map[string]int{"op": 400}},
		{"bad weka source rejected", "POST", "/api/templates", `{"name":"bad","fstype":"wekafs","source":"nofs","mountpoint":"/mnt/x"}`, map[string]int{"op": 400}},
		{"unknown api path", "GET", "/api/nope", "", map[string]int{"adm": 404}},
	}
	for _, cs := range cases {
		for lbl, want := range cs.tok {
			if got := do(cs.method, cs.path, labels[lbl], cs.body); got != want {
				t.Errorf("%s as %s: got %d want %d", cs.name, lbl, got, want)
			}
		}
	}
	var fst string
	if err := s.DB.QueryRow(ctx, `SELECT fstype FROM templates WHERE name='t1'`).Scan(&fst); err != nil || fst != "wekafs" {
		t.Errorf("wekafs template not stored: %v %q", err, fst)
	}
	_ = http.StatusOK
}
