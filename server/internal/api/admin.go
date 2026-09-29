package api

import (
	"encoding/json"
	"errors"
	"github.com/rhoods/mountmanager/internal/auth"
	"github.com/rhoods/mountmanager/internal/ui"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AdminMux serves the REST API used by the CLI and web UI.
func (s *Server) AdminMux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", ui.Handler())
	mux.HandleFunc("POST /api/login", s.login)
	// operators manage mounts (templates, groups, memberships, hosts); admins additionally manage enrolment.
	rw := func(h http.HandlerFunc) http.HandlerFunc { return s.auth("operator", h) }
	adm := func(h http.HandlerFunc) http.HandlerFunc { return s.auth("admin", h) }
	ro := func(h http.HandlerFunc) http.HandlerFunc { return s.auth("readonly", h) }

	mux.HandleFunc("GET /api/templates", ro(s.list(`SELECT id,name,fstype,source,mountpoint,options,version FROM templates ORDER BY name`,
		"id", "name", "fstype", "source", "mountpoint", "options", "version")))
	mux.HandleFunc("POST /api/templates", rw(s.putTemplate))
	mux.HandleFunc("DELETE /api/templates/{id}", rw(s.del("templates")))
	mux.HandleFunc("GET /api/groups", ro(s.list(`SELECT g.id,g.name,g.priority,g.host_regex,
	  COALESCE((SELECT array_agg(t.name ORDER BY t.name) FROM group_templates gt JOIN templates t ON t.id=gt.template_id WHERE gt.group_id=g.id),'{}') AS templates,
	  COALESCE((SELECT array_agg(h.hostname ORDER BY h.hostname) FROM group_members m JOIN hosts h ON h.id=m.host_id WHERE m.group_id=g.id),'{}') AS members
	  FROM groups g ORDER BY g.priority DESC,g.name`, "id", "name", "priority", "host_regex", "templates", "members")))
	mux.HandleFunc("POST /api/groups", rw(s.putGroup))
	mux.HandleFunc("DELETE /api/groups/{id}", rw(s.del("groups")))
	mux.HandleFunc("POST /api/groups/{id}/templates/{tid}", rw(s.link(`INSERT INTO group_templates VALUES($1,$2) ON CONFLICT DO NOTHING`)))
	mux.HandleFunc("DELETE /api/groups/{id}/templates/{tid}", rw(s.link(`DELETE FROM group_templates WHERE group_id=$1 AND template_id=$2`)))
	mux.HandleFunc("POST /api/groups/{id}/members/{tid}", rw(s.link(`INSERT INTO group_members VALUES($1,$2) ON CONFLICT DO NOTHING`)))
	mux.HandleFunc("DELETE /api/groups/{id}/members/{tid}", rw(s.link(`DELETE FROM group_members WHERE group_id=$1 AND host_id=$2`)))
	mux.HandleFunc("GET /api/hosts", ro(s.list(`SELECT h.id,h.hostname,h.agent_version,h.last_seen,
	  CASE WHEN h.desired_hash='' THEN 'unknown' WHEN h.desired_hash=h.applied_hash THEN 'in-sync' ELSE 'pending' END AS status,
	  COALESCE((SELECT string_agg(mountpoint||': '||state||CASE WHEN error<>'' THEN ' ('||error||')' ELSE '' END, '; ') FROM host_mounts m WHERE m.host_id=h.id AND m.state<>'ok'),'') AS problems
	  FROM hosts h ORDER BY h.hostname`, "id", "hostname", "agent_version", "last_seen", "status", "problems")))
	mux.HandleFunc("DELETE /api/hosts/{id}", rw(s.del("hosts")))
	mux.HandleFunc("GET /api/hosts/{id}/mounts", ro(s.hostMounts))
	mux.HandleFunc("POST /api/tokens", adm(s.newToken))
	mux.HandleFunc("GET /api/audit", ro(s.auditList))
	return mux
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		fail(w, 400, "bad request")
		return
	}
	ctx, ip := r.Context(), clientIP(r)
	key := strings.ToLower(req.Username) + "|" + ip
	if !s.lim.allow(key) {
		s.audit(ctx, "user:"+req.Username, "", "login-blocked", ip+" too many failed attempts")
		fail(w, 429, "too many failed attempts; try again later")
		return
	}
	var role, method string
	var hash string
	err := s.DB.QueryRow(ctx, `SELECT pass_hash,role FROM users WHERE username=$1 AND source='local'`, req.Username).Scan(&hash, &role)
	switch {
	case err == nil: // local account (also used for break-glass admin when the directory is down)
		method = "local"
		if !checkPassword(req.Password, hash) {
			role = ""
		}
	case s.Dir != nil:
		method = "ldap"
		res, derr := s.Dir.Authenticate(req.Username, req.Password)
		var de *auth.DirectoryError
		switch {
		case derr == nil:
			role = res.Role
		case errors.Is(derr, auth.ErrNoRole):
			s.audit(ctx, "user:"+req.Username, "", "login-denied", ip+" ldap: no group mapped to a role")
			fail(w, 403, "your account is not a member of any group permitted to use Mount Manager")
			return
		case errors.As(derr, &de):
			log.Printf("ldap: %v", derr)
			s.audit(ctx, "user:"+req.Username, "", "login-error", ip+" ldap: directory unavailable")
			fail(w, 503, "directory service unavailable")
			return
		default:
			role = ""
		}
	}
	if role == "" {
		s.lim.fail(key)
		s.audit(ctx, "user:"+req.Username, "", "login-failed", ip)
		fail(w, 401, "invalid credentials")
		return
	}
	s.lim.reset(key)
	tok := randTok()
	s.DB.Exec(ctx, `INSERT INTO sessions VALUES($1,$2,$3,now()+interval '12 hours')`, hashTok(tok), req.Username, role)
	s.audit(ctx, "user:"+req.Username, "", "login", ip+" method="+method+" role="+role)
	jsonOut(w, 200, map[string]string{"token": tok, "role": role})
}

var roleRank = map[string]int{"readonly": 1, "operator": 2, "admin": 3}

func (s *Server) auth(min string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("Authorization")
		if len(tok) < 8 || tok[:7] != "Bearer " {
			fail(w, 401, "authentication required")
			return
		}
		var user, role string
		err := s.DB.QueryRow(r.Context(), `SELECT username,role FROM sessions WHERE token_hash=$1 AND expires>now()`, hashTok(tok[7:])).Scan(&user, &role)
		if err != nil {
			fail(w, 401, "invalid or expired session")
			return
		}
		if roleRank[role] < roleRank[min] {
			fail(w, 403, "insufficient role")
			return
		}
		r.Header.Set("X-MM-User", user)
		h(w, r)
	}
}

func user(r *http.Request) string { return "user:" + r.Header.Get("X-MM-User") }

// list runs a query and returns rows as JSON objects keyed by cols.
func (s *Server) list(q string, cols ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.DB.Query(r.Context(), q)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				fail(w, 500, err.Error())
				return
			}
			m := map[string]any{}
			for i, c := range cols {
				m[c] = vals[i]
			}
			out = append(out, m)
		}
		jsonOut(w, 200, out)
	}
}

func (s *Server) putTemplate(w http.ResponseWriter, r *http.Request) {
	var t struct{ Name, FSType, Source, Mountpoint, Options string }
	if json.NewDecoder(r.Body).Decode(&t) != nil || t.Name == "" || t.Source == "" || t.Mountpoint == "" || t.Mountpoint[0] != '/' {
		fail(w, 400, "name, source and absolute mountpoint required")
		return
	}
	for _, f := range []string{t.Name, t.Source, t.Mountpoint, t.Options} {
		if strings.ContainsAny(f, "\t\r\n\x00") {
			fail(w, 400, "control characters not allowed in template fields")
			return
		}
	}
	if t.FSType == "" {
		t.FSType = "nfs"
	}
	if msg := ValidateTemplate(t.FSType, t.Source); msg != "" {
		fail(w, 400, msg)
		return
	}
	var id int64
	err := s.DB.QueryRow(r.Context(), `INSERT INTO templates(name,fstype,source,mountpoint,options) VALUES($1,$2,$3,$4,$5)
	  ON CONFLICT (name) DO UPDATE SET fstype=$2,source=$3,mountpoint=$4,options=$5,version=templates.version+1,updated_at=now() RETURNING id`,
		t.Name, t.FSType, t.Source, t.Mountpoint, t.Options).Scan(&id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), user(r), "", "template-set", t.Name+" "+t.Source+" -> "+t.Mountpoint+" ("+t.Options+")")
	jsonOut(w, 200, map[string]int64{"id": id})
}

func (s *Server) putGroup(w http.ResponseWriter, r *http.Request) {
	var g struct {
		Name      string
		Priority  int
		HostRegex string `json:"host_regex"`
	}
	if json.NewDecoder(r.Body).Decode(&g) != nil || g.Name == "" {
		fail(w, 400, "name required")
		return
	}
	if g.HostRegex != "" && s.regex(g.HostRegex) == nil {
		fail(w, 400, "invalid host_regex")
		return
	}
	if g.Priority == 0 {
		g.Priority = 100
	}
	var id int64
	err := s.DB.QueryRow(r.Context(), `INSERT INTO groups(name,priority,host_regex) VALUES($1,$2,$3)
	  ON CONFLICT (name) DO UPDATE SET priority=$2,host_regex=$3 RETURNING id`, g.Name, g.Priority, g.HostRegex).Scan(&id)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), user(r), "", "group-set", g.Name+" prio="+strconv.Itoa(g.Priority)+" regex="+g.HostRegex)
	jsonOut(w, 200, map[string]int64{"id": id})
}

func (s *Server) del(table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if _, err := s.DB.Exec(r.Context(), `DELETE FROM `+table+` WHERE id=$1`, id); err != nil {
			fail(w, 500, err.Error())
			return
		}
		s.audit(r.Context(), user(r), "", table+"-delete", strconv.FormatInt(id, 10))
		jsonOut(w, 200, map[string]string{"status": "ok"})
	}
}

func (s *Server) link(q string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		b, _ := strconv.ParseInt(r.PathValue("tid"), 10, 64)
		if _, err := s.DB.Exec(r.Context(), q, a, b); err != nil {
			fail(w, 500, err.Error())
			return
		}
		s.audit(r.Context(), user(r), "", "link", r.Method+" "+r.URL.Path)
		jsonOut(w, 200, map[string]string{"status": "ok"})
	}
}

func (s *Server) hostMounts(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var host string
	if s.DB.QueryRow(r.Context(), `SELECT hostname FROM hosts WHERE id=$1`, id).Scan(&host) != nil {
		fail(w, 404, "no such host")
		return
	}
	desired, _ := s.DesiredMounts(r.Context(), id, host)
	rows, _ := s.DB.Query(r.Context(), `SELECT mountpoint,state,error,updated_at FROM host_mounts WHERE host_id=$1`, id)
	defer rows.Close()
	state := []map[string]any{}
	for rows.Next() {
		var mp, st, e string
		var t time.Time
		rows.Scan(&mp, &st, &e, &t)
		state = append(state, map[string]any{"mountpoint": mp, "state": st, "error": e, "updated_at": t})
	}
	jsonOut(w, 200, map[string]any{"hostname": host, "desired": desired, "state": state})
}

func (s *Server) newToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note  string
		Hours int
		Uses  int
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Hours <= 0 {
		req.Hours = 24
	}
	if req.Uses <= 0 {
		req.Uses = 1
	}
	tok := randTok()
	s.DB.Exec(r.Context(), `INSERT INTO enroll_tokens(token_hash,note,expires,uses_left) VALUES($1,$2,now()+make_interval(hours=>$3),$4)`,
		hashTok(tok), req.Note, req.Hours, req.Uses)
	s.audit(r.Context(), user(r), "", "token-create", req.Note)
	jsonOut(w, 200, map[string]string{"token": tok})
}

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 1000 {
		n = 100
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id,ts,actor,host,action,detail FROM audit
	  WHERE ($1='' OR strpos(lower(host), lower($1)) > 0) ORDER BY id DESC LIMIT $2`, r.URL.Query().Get("host"), n)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var ts time.Time
		var actor, host, action, detail string
		rows.Scan(&id, &ts, &actor, &host, &action, &detail)
		out = append(out, map[string]any{"id": id, "ts": ts, "actor": actor, "host": host, "action": action, "detail": detail})
	}
	jsonOut(w, 200, out)
}

// SupportedFSTypes are the filesystem types the agent will mount (agents enforce their own allow-list too).
var SupportedFSTypes = []string{"nfs", "nfs4", "wekafs"}

// ValidateTemplate returns a human-readable problem with the type/source combination, or "".
func ValidateTemplate(fstype, source string) string {
	switch fstype {
	case "nfs", "nfs4":
		if i := strings.Index(source, ":/"); i <= 0 {
			return "nfs source must look like server:/export/path"
		}
	case "wekafs":
		// mount -t wekafs backend[,backend...]/filesystem MOUNTPOINT
		if i := strings.LastIndex(source, "/"); i <= 0 || i == len(source)-1 || strings.HasPrefix(source, "/") {
			return "wekafs source must look like backend/filesystem (e.g. weka01/fs1)"
		}
	default:
		return "unsupported fstype (" + strings.Join(SupportedFSTypes, ", ") + ")"
	}
	return ""
}
