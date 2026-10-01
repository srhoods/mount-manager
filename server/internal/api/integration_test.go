package api

// Database-backed tests for mount resolution and role enforcement. Skipped unless MM_TEST_DSN is set.
// Each run works in its own throwaway schema, so it is safe to point at a shared development database.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/rhoods/mountmanager/internal/pki"
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

// ---- hosts: pagination, filtering, summary ----

func getJSON(t *testing.T, h http.Handler, path, token string) (int, http.Header, []byte) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Header(), w.Body.Bytes()
}

func seedUser(t *testing.T, s *Server, ctx context.Context, name, role string) {
	t.Helper()
	if _, err := s.DB.Exec(ctx, `INSERT INTO users(username,pass_hash,role) VALUES($1,$2,$3)`, name, HashPassword("pw-"+name), role); err != nil {
		t.Fatal(err)
	}
}

func loginTok(t *testing.T, h http.Handler, u string) string {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"`+u+`","password":"pw-`+u+`"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Token == "" {
		t.Fatalf("login %s failed: %d %s", u, w.Code, w.Body.String())
	}
	return out.Token
}

func TestHostsPaginationFilterSummary(t *testing.T) {
	s, ctx := testServer(t)
	seedUser(t, s, ctx, "ro", "readonly")
	h := s.AdminMux()
	tok := loginTok(t, h, "ro")
	exec := func(q string, a ...any) {
		if _, err := s.DB.Exec(ctx, q, a...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("node-%02d", i)
		switch {
		case i < 10: // in sync
			exec(`INSERT INTO hosts(hostname,last_seen,desired_hash,applied_hash) VALUES($1,now(),'a','a')`, name)
		case i < 15: // pending
			exec(`INSERT INTO hosts(hostname,last_seen,desired_hash,applied_hash) VALUES($1,now(),'a','b')`, name)
		case i < 20: // silent for an hour
			exec(`INSERT INTO hosts(hostname,last_seen,desired_hash,applied_hash) VALUES($1,now()-interval '1 hour','a','a')`, name)
		case i < 25: // never seen
			exec(`INSERT INTO hosts(hostname,desired_hash,applied_hash) VALUES($1,'a','a')`, name)
		default: // no desired state computed yet
			exec(`INSERT INTO hosts(hostname,last_seen) VALUES($1,now())`, name)
		}
	}
	exec(`INSERT INTO host_mounts(host_id,mountpoint,state,error) SELECT id,'/mnt/x','failed','no route' FROM hosts WHERE hostname='node-10'`)

	type host struct {
		ID       int64  `json:"id"`
		Hostname string `json:"hostname"`
		State    string `json:"state"`
		Problems string `json:"problems"`
	}
	list := func(query string) (int, string, []host) {
		code, hdr, body := getJSON(t, h, "/api/hosts"+query, tok)
		var out []host
		if code == 200 {
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatalf("%s: %v %s", query, err, body)
			}
		}
		return code, hdr.Get("X-Total-Count"), out
	}

	// pages are disjoint, ordered, and together cover every host
	seen := map[string]bool{}
	var prev string
	for off := 0; off < 30; off += 12 {
		code, total, page := list(fmt.Sprintf("?limit=12&offset=%d", off))
		if code != 200 || total != "30" {
			t.Fatalf("offset %d: code=%d total=%s", off, code, total)
		}
		wantLen := 12
		if off == 24 {
			wantLen = 6
		}
		if len(page) != wantLen {
			t.Errorf("offset %d: got %d rows want %d", off, len(page), wantLen)
		}
		for _, p := range page {
			if seen[p.Hostname] || p.Hostname <= prev {
				t.Errorf("duplicate or unordered row %s after %s", p.Hostname, prev)
			}
			seen[p.Hostname], prev = true, p.Hostname
		}
	}
	if len(seen) != 30 {
		t.Errorf("pages covered %d hosts, want 30", len(seen))
	}
	if _, total, page := list("?limit=5&offset=100"); total != "30" || len(page) != 0 {
		t.Errorf("past the end: total=%s rows=%d", total, len(page))
	}
	if _, total, all := list(""); total != "30" || len(all) != 30 {
		t.Errorf("no limit should return everything: total=%s rows=%d", total, len(all))
	}

	// state semantics
	counts := map[string]int{"in-sync": 10, "pending": 5, "offline": 10, "unknown": 5}
	for st, want := range counts {
		if _, total, rows := list("?state=" + st); total != fmt.Sprint(want) || len(rows) != want {
			t.Errorf("state=%s: total=%s rows=%d want %d", st, total, len(rows), want)
		} else {
			for _, r := range rows {
				if r.State != st {
					t.Errorf("state=%s returned a %s host", st, r.State)
				}
			}
		}
	}
	if _, total, _ := list("?state=attention"); total != "20" {
		t.Errorf("attention should be everything not in sync: %s", total)
	}
	// combined filters, and pagination of a filtered set
	if _, total, rows := list("?q=NODE-1&state=pending&limit=2&offset=1"); total != "5" || len(rows) != 2 {
		t.Errorf("combined filter: total=%s rows=%d", total, len(rows))
	}
	if _, total, rows := list("?q=node-1"); total != "10" || len(rows) != 10 {
		t.Errorf("substring filter: total=%s rows=%d", total, len(rows))
	}
	if _, total, _ := list("?q=zzz"); total != "0" {
		t.Errorf("no match: %s", total)
	}
	// problems are reported for the failing host only
	_, _, rows := list("?q=node-10")
	if len(rows) != 1 || !strings.Contains(rows[0].Problems, "no route") {
		t.Errorf("node-10 problems: %+v", rows)
	}
	// brief mode is lightweight but still filtered and counted
	if code, hdr, body := getJSON(t, h, "/api/hosts?brief=1&state=offline", tok); code != 200 || hdr.Get("X-Total-Count") != "10" || strings.Contains(string(body), "last_seen") {
		t.Errorf("brief: %d %s %.80s", code, hdr.Get("X-Total-Count"), body)
	}
	// validation
	for _, bad := range []string{"?state=bogus", "?limit=0", "?limit=abc", "?limit=5000"} {
		if code, _, _ := list(bad); code != 400 {
			t.Errorf("%s should be rejected, got %d", bad, code)
		}
	}
	// summary agrees with the list
	code, _, body := getJSON(t, h, "/api/hosts/summary", tok)
	var sum map[string]int
	json.Unmarshal(body, &sum)
	if code != 200 || sum["total"] != 30 || sum["in_sync"] != 10 || sum["pending"] != 5 || sum["offline"] != 10 || sum["unknown"] != 5 {
		t.Errorf("summary: %d %v", code, sum)
	}
	if code, _, _ := getJSON(t, h, "/api/hosts", ""); code != 401 {
		t.Errorf("unauthenticated list: %d", code)
	}
}

// ---- enrolment tokens ----

func csrFor(t *testing.T, host string) string {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: host}}, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func TestEnrolmentTokenLifecycle(t *testing.T) {
	s, ctx := testServer(t)
	c, k, _ := pki.NewCA()
	s.CA, _ = pki.LoadCA(c, k)
	seedUser(t, s, ctx, "adm", "admin")
	seedUser(t, s, ctx, "op", "operator")
	admin, agent := s.AdminMux(), s.AgentMux()
	adm, op := loginTok(t, admin, "adm"), loginTok(t, admin, "op")

	call := func(h http.Handler, method, path, tok, body string) (int, []byte) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	create := func(note string, hours, uses int) (int64, string) {
		code, b := call(admin, "POST", "/api/tokens", adm, fmt.Sprintf(`{"note":%q,"hours":%d,"uses":%d}`, note, hours, uses))
		var out struct {
			Token string
			ID    int64
		}
		json.Unmarshal(b, &out)
		if code != 200 || out.Token == "" || out.ID == 0 {
			t.Fatalf("create: %d %s", code, b)
		}
		return out.ID, out.Token
	}
	enrol := func(tok, host string) int {
		body, _ := json.Marshal(map[string]string{"token": tok, "hostname": host, "csr": csrFor(t, host), "version": "test"})
		code, _ := call(agent, "POST", "/v1/enroll", "", string(body))
		return code
	}
	type tokRow struct {
		ID        int64  `json:"id"`
		Note      string `json:"note"`
		CreatedBy string `json:"created_by"`
		UsesLeft  int    `json:"uses_left"`
		UsesTotal int    `json:"uses_total"`
		Status    string `json:"status"`
		Secs      int64  `json:"seconds_left"`
	}
	list := func(q string) map[int64]tokRow {
		code, b := call(admin, "GET", "/api/tokens"+q, adm, "")
		var rows []tokRow
		if code != 200 || json.Unmarshal(b, &rows) != nil {
			t.Fatalf("list: %d %s", code, b)
		}
		m := map[int64]tokRow{}
		for _, r := range rows {
			m[r.ID] = r
		}
		return m
	}

	// listing shows metadata and never the secret
	id1, tok1 := create("batch one", 2, 3)
	if _, b := call(admin, "GET", "/api/tokens", adm, ""); strings.Contains(string(b), tok1) || strings.Contains(string(b), "token_hash") {
		t.Errorf("list leaks token material: %s", b)
	}
	r := list("")[id1]
	if r.Status != "active" || r.UsesLeft != 3 || r.UsesTotal != 3 || r.Note != "batch one" || r.CreatedBy != "adm" || r.Secs < 7100 || r.Secs > 7200 {
		t.Errorf("fresh token: %+v", r)
	}
	// only administrators can list, create or revoke
	for _, m := range [][2]string{{"GET", "/api/tokens"}, {"POST", "/api/tokens"}, {"DELETE", fmt.Sprintf("/api/tokens/%d", id1)}} {
		if code, _ := call(admin, m[0], m[1], op, "{}"); code != 403 {
			t.Errorf("operator %s %s: %d", m[0], m[1], code)
		}
		if code, _ := call(admin, m[0], m[1], "", "{}"); code != 401 {
			t.Errorf("anonymous %s %s: %d", m[0], m[1], code)
		}
	}
	// enrolment spends a use
	if code := enrol(tok1, "h1.example.com"); code != 200 {
		t.Fatalf("enrol: %d", code)
	}
	if r := list("")[id1]; r.UsesLeft != 2 || r.Status != "active" {
		t.Errorf("after one enrolment: %+v", r)
	}
	// revoke: gone from the active list, present with ?all, cannot be used, cannot be revoked twice
	if code, _ := call(admin, "DELETE", fmt.Sprintf("/api/tokens/%d", id1), adm, ""); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if _, ok := list("")[id1]; ok {
		t.Error("revoked token still listed as active")
	}
	if r := list("?all=1")[id1]; r.Status != "revoked" || r.UsesLeft != 2 {
		t.Errorf("revoked token in history: %+v", r)
	}
	if code := enrol(tok1, "h2.example.com"); code != 403 {
		t.Errorf("revoked token must not enrol: %d", code)
	}
	if code, _ := call(admin, "DELETE", fmt.Sprintf("/api/tokens/%d", id1), adm, ""); code != 404 {
		t.Errorf("second revoke: %d", code)
	}
	// a single-use token is used up after one enrolment, and then cannot be revoked
	id2, tok2 := create("single", 1, 1)
	if enrol(tok2, "h3.example.com") != 200 || enrol(tok2, "h4.example.com") != 403 {
		t.Error("single-use token should work exactly once")
	}
	if r := list("?all=1")[id2]; r.Status != "used" || r.UsesLeft != 0 {
		t.Errorf("used-up token: %+v", r)
	}
	if _, ok := list("")[id2]; ok {
		t.Error("used-up token listed as active")
	}
	if code, _ := call(admin, "DELETE", fmt.Sprintf("/api/tokens/%d", id2), adm, ""); code != 404 {
		t.Errorf("revoking a used-up token: %d", code)
	}
	// expired tokens are not active and cannot enrol
	id3, tok3 := create("stale", 1, 5)
	if _, err := s.DB.Exec(ctx, `UPDATE enroll_tokens SET expires = now() - interval '1 minute' WHERE id=$1`, id3); err != nil {
		t.Fatal(err)
	}
	if _, ok := list("")[id3]; ok {
		t.Error("expired token listed as active")
	}
	if r := list("?all=1")[id3]; r.Status != "expired" || r.Secs != 0 {
		t.Errorf("expired token: %+v", r)
	}
	if enrol(tok3, "h5.example.com") != 403 {
		t.Error("expired token must not enrol")
	}
	// validation and unknown tokens
	if code, _ := call(admin, "POST", "/api/tokens", adm, `{"hours":99999,"uses":1}`); code != 400 {
		t.Errorf("absurd lifetime accepted: %d", code)
	}
	if enrol("not-a-token", "h6.example.com") != 403 {
		t.Error("unknown token must not enrol")
	}
	if code, _ := call(admin, "DELETE", "/api/tokens/99999", adm, ""); code != 404 {
		t.Errorf("unknown id: %d", code)
	}
	// audit trail names the token that was used, and records revocation and refusals
	var n int
	pattern := fmt.Sprintf(`%%via token #%d "batch one"%%`, id1)
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM audit WHERE action='enrol' AND host='h1.example.com' AND detail LIKE $1`, pattern).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("enrol audit should cite the token: %d", n)
	}
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM audit WHERE action IN ('token-create','token-revoke','enrol-refused')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 7 {
		t.Errorf("token activity not audited: %d", n)
	}
}

// A host that matches no group has no mounts. The API must return empty lists (not null), or clients that iterate
// over them fail: the web UI once went blank when such a host was opened.
func TestHostWithNoMountsReturnsEmptyLists(t *testing.T) {
	s, ctx := testServer(t)
	seedUser(t, s, ctx, "ro", "readonly")
	h := s.AdminMux()
	tok := loginTok(t, h, "ro")
	id := (fx{s, ctx, t}).host("lonely.example.com")
	code, _, body := getJSON(t, h, fmt.Sprintf("/api/hosts/%d/mounts", id), tok)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(body, &raw)
	for _, k := range []string{"desired", "state"} {
		if string(raw[k]) != "[]" {
			t.Errorf("%s = %s, want []", k, raw[k])
		}
	}
}

func TestCloneMount(t *testing.T) {
	s, ctx := testServer(t)
	seedUser(t, s, ctx, "adm", "admin")
	seedUser(t, s, ctx, "op", "operator")
	seedUser(t, s, ctx, "ro", "readonly")
	h := s.AdminMux()
	adm, op, ro := loginTok(t, h, "adm"), loginTok(t, h, "op"), loginTok(t, h, "ro")
	f := fx{s, ctx, t}
	src := f.tpl("data", "nfs1:/data", "/mnt/data", "rw,_netdev,hard")
	if _, err := s.DB.Exec(ctx, `UPDATE templates SET fstype='nfs4', version=7 WHERE id=$1`, src); err != nil {
		t.Fatal(err)
	}
	g := f.group("everyone", 100, ".", src)
	_ = g

	post := func(tok, path, body string) (int, string) {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	path := fmt.Sprintf("/api/templates/%d/clone", src)

	code, body := post(adm, path, `{"name":"  data-copy  "}`)
	if code != 200 {
		t.Fatalf("clone: %d %s", code, body)
	}
	var out struct{ ID int64 }
	json.Unmarshal([]byte(body), &out)
	var name, fst, source, mp, opts string
	var ver int
	if err := s.DB.QueryRow(ctx, `SELECT name,fstype,source,mountpoint,options,version FROM templates WHERE id=$1`, out.ID).Scan(&name, &fst, &source, &mp, &opts, &ver); err != nil {
		t.Fatal(err)
	}
	if out.ID == src || name != "data-copy" || fst != "nfs4" || source != "nfs1:/data" || mp != "/mnt/data" || opts != "rw,_netdev,hard" || ver != 1 {
		t.Errorf("clone contents: id=%d %q %s %s %s %q v%d", out.ID, name, fst, source, mp, opts, ver)
	}
	// the clone starts unattached; the original keeps its group and version
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM group_templates WHERE template_id=$1`, out.ID).Scan(&n)
	if n != 0 {
		t.Errorf("group assignments must not be copied (%d)", n)
	}
	s.DB.QueryRow(ctx, `SELECT version FROM templates WHERE id=$1`, src).Scan(&ver)
	if ver != 7 {
		t.Errorf("source changed: version %d", ver)
	}

	// never overwrites: an existing name (the source itself, or the clone) is a conflict and changes nothing
	for _, nm := range []string{"data", "data-copy"} {
		if code, _ := post(adm, path, fmt.Sprintf(`{"name":%q}`, nm)); code != 409 {
			t.Errorf("clone onto existing %q: %d", nm, code)
		}
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM templates`).Scan(&n)
	if n != 2 {
		t.Errorf("templates after refused clones: %d", n)
	}
	// validation, unknown source, permissions
	for _, bad := range []string{`{"name":""}`, `{"name":"   "}`, `{}`, `{"name":"a\tb"}`, `{"name":"` + strings.Repeat("x", 201) + `"}`, `not json`} {
		if code, _ := post(adm, path, bad); code != 400 {
			t.Errorf("body %.30q: %d", bad, code)
		}
	}
	if code, _ := post(adm, "/api/templates/99999/clone", `{"name":"x"}`); code != 404 {
		t.Errorf("unknown source: %d", code)
	}
	if code, _ := post(op, path, `{"name":"by-operator"}`); code != 200 {
		t.Errorf("operators manage mounts and may clone: %d", code)
	}
	if code, _ := post(ro, path, `{"name":"by-readonly"}`); code != 403 {
		t.Errorf("readonly clone: %d", code)
	}
	if code, _ := post("", path, `{"name":"anon"}`); code != 401 {
		t.Errorf("anonymous clone: %d", code)
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM audit WHERE action='template-clone'`).Scan(&n)
	if n != 2 {
		t.Errorf("clones should be audited (2 successful): %d", n)
	}
}
