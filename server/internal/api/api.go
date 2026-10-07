// Package api implements the agent (mTLS) and admin (REST) HTTP APIs.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/rhoods/mountmanager/internal/auth"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhoods/mountmanager/internal/pki"
	"golang.org/x/crypto/argon2"
)

// Directory authenticates users against an external directory (LDAPS/AD) and returns their role.
type Directory interface {
	Authenticate(user, password string) (*auth.Result, error)
}

// BuildInfo identifies the running server build; it is set by main from linker flags.
type BuildInfo struct {
	Version string
	Commit  string
	Built   string
	Started time.Time
}

type Server struct {
	Info BuildInfo
	DB   *pgxpool.Pool
	CA   *pki.CA
	Dir  Directory // optional
	mu   sync.Mutex
	re   map[string]*regexp.Regexp
	lim  *limiter
}

func New(db *pgxpool.Pool, ca *pki.CA) *Server {
	return &Server{DB: db, CA: ca, re: map[string]*regexp.Regexp{}, lim: newLimiter(5, 5*time.Minute)}
}

func hashTok(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

func randTok() string { b := make([]byte, 24); rand.Read(b); return hex.EncodeToString(b) }

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	jsonOut(w, code, map[string]string{"error": msg})
}

func (s *Server) audit(ctx context.Context, actor, host, action, detail string) {
	s.DB.Exec(ctx, `INSERT INTO audit(actor,host,action,detail) VALUES($1,$2,$3,$4)`, actor, host, action, detail)
}

// ---- password hashing (argon2id) ----

func HashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	k := argon2.IDKey([]byte(pw), salt, 2, 32*1024, 2, 32)
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(k)
}

func checkPassword(pw, stored string) bool {
	parts := strings.SplitN(stored, "$", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	return hex.EncodeToString(argon2.IDKey([]byte(pw), salt, 2, 32*1024, 2, 32)) == parts[1]
}

// ---- desired state ----

type Mount struct {
	Source     string `json:"source"`
	Mountpoint string `json:"mountpoint"`
	FSType     string `json:"fstype"`
	Options    string `json:"options"`
	Version    int    `json:"version"`
}

func (s *Server) regex(p string) *regexp.Regexp {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.re[p]; ok {
		return r
	}
	r, err := regexp.Compile(p)
	if err != nil {
		r = nil
	}
	s.re[p] = r
	return r
}

// DesiredMounts resolves a host's mounts: static members + regex groups; highest priority wins per mountpoint.
func (s *Server) DesiredMounts(ctx context.Context, hostID int64, hostname string) ([]Mount, error) {
	rows, err := s.DB.Query(ctx, `
	  SELECT g.id, g.priority, g.host_regex, (gm.host_id IS NOT NULL) AS member,
	         t.source, t.mountpoint, t.fstype, t.options, t.version
	  FROM groups g
	  JOIN group_templates gt ON gt.group_id=g.id JOIN templates t ON t.id=gt.template_id
	  LEFT JOIN group_members gm ON gm.group_id=g.id AND gm.host_id=$1
	  ORDER BY g.priority DESC, g.id`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []Mount
	for rows.Next() {
		var gid int64
		var prio int
		var rx string
		var member bool
		var m Mount
		if err := rows.Scan(&gid, &prio, &rx, &member, &m.Source, &m.Mountpoint, &m.FSType, &m.Options, &m.Version); err != nil {
			return nil, err
		}
		if !member && rx != "" {
			if r := s.regex(rx); r != nil && r.MatchString(hostname) {
				member = true
			}
		}
		if member && !seen[m.Mountpoint] {
			seen[m.Mountpoint] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mountpoint < out[j].Mountpoint })
	return out, rows.Err()
}

func mountsHash(m []Mount) string {
	b, _ := json.Marshal(m)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// ---- agent API ----

func (s *Server) AgentMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/enroll", s.enroll)
	mux.HandleFunc("GET /v1/ca", func(w http.ResponseWriter, r *http.Request) { w.Write(s.CA.CertPEM) })
	mux.HandleFunc("GET /v1/desired-state", s.agentAuth(s.desiredState))
	mux.HandleFunc("POST /v1/report", s.agentAuth(s.report))
	return mux
}

type agentHandler func(w http.ResponseWriter, r *http.Request, hostID int64, hostname string)

func (s *Server) agentAuth(h agentHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			fail(w, 401, "client certificate required")
			return
		}
		c := r.TLS.PeerCertificates[0]
		var id int64
		err := s.DB.QueryRow(r.Context(), `UPDATE hosts SET last_seen=now() WHERE hostname=$1 AND cert_serial=$2 RETURNING id`,
			c.Subject.CommonName, c.SerialNumber.Text(16)).Scan(&id)
		if err != nil {
			fail(w, 403, "unknown or superseded certificate")
			return
		}
		h(w, r, id, c.Subject.CommonName)
	}
}

func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct{ Token, Hostname, CSR, Version string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req) != nil || req.Hostname == "" {
		fail(w, 400, "bad request")
		return
	}
	// A host already holding a valid cert may re-enrol (renewal) via mTLS instead of a token.
	renew := false
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && r.TLS.PeerCertificates[0].Subject.CommonName == req.Hostname {
		renew = true
	}
	var tokID int64
	var tokNote string
	if !renew {
		// atomically spend one use of a token that is still active (not revoked, not expired, uses remaining)
		err := s.DB.QueryRow(r.Context(), `UPDATE enroll_tokens SET uses_left=uses_left-1
		  WHERE token_hash=$1 AND expires>now() AND uses_left>0 AND revoked_at IS NULL RETURNING id, note`, hashTok(req.Token)).Scan(&tokID, &tokNote)
		if err != nil {
			s.audit(r.Context(), "agent:"+req.Hostname, req.Hostname, "enrol-refused", "invalid, expired, revoked or used-up enrolment token")
			fail(w, 403, "invalid or expired enrolment token")
			return
		}
	}
	cert, serial, err := s.CA.SignCSR([]byte(req.CSR), req.Hostname, 30*24*time.Hour)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	_, err = s.DB.Exec(r.Context(), `INSERT INTO hosts(hostname,cert_serial,agent_version) VALUES($1,$2,$3)
	  ON CONFLICT (hostname) DO UPDATE SET cert_serial=$2, agent_version=$3`, req.Hostname, serial, req.Version)
	if err != nil {
		fail(w, 500, "db error")
		return
	}
	act := "enrol"
	if renew {
		act = "cert-renew"
	}
	detail := "serial " + serial
	if !renew {
		detail += fmt.Sprintf(" via token #%d %q", tokID, tokNote)
	}
	s.audit(r.Context(), "agent:"+req.Hostname, req.Hostname, act, detail)
	w.Header().Set("Content-Type", "text/plain")
	w.Write(cert)
	w.Write(s.CA.CertPEM)
}

func (s *Server) desiredState(w http.ResponseWriter, r *http.Request, id int64, host string) {
	m, err := s.DesiredMounts(r.Context(), id, host)
	if err != nil {
		fail(w, 500, "db error")
		return
	}
	h := mountsHash(m)
	s.DB.Exec(r.Context(), `UPDATE hosts SET desired_hash=$2 WHERE id=$1 AND desired_hash<>$2`, id, h)
	w.Header().Set("ETag", `"`+h+`"`)
	if r.Header.Get("If-None-Match") == `"`+h+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	// Plain TSV keeps the C agent free of a JSON dependency: "rev<TAB>hash" then one "mount" line per mount.
	w.Header().Set("Content-Type", "text/tab-separated-values")
	fmt.Fprintf(w, "rev\t%s\n", h)
	for _, x := range m {
		fmt.Fprintf(w, "mount\t%s\t%s\t%s\t%s\n", x.Source, x.Mountpoint, x.FSType, x.Options)
	}
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, id int64, host string) {
	var req struct {
		Version string `json:"version"`
		Rev     string `json:"rev"`
		Mounts  []struct {
			Mountpoint string `json:"mountpoint"`
			State      string `json:"state"` // ok | pending | failed
			Error      string `json:"error"`
		} `json:"mounts"`
		Events []struct {
			Action string `json:"action"`
			Detail string `json:"detail"`
		} `json:"events"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil {
		fail(w, 400, "bad request")
		return
	}
	ctx := r.Context()
	allOK := true
	keep := []string{}
	for _, m := range req.Mounts {
		if m.State != "ok" {
			allOK = false
		}
		keep = append(keep, m.Mountpoint)
		s.DB.Exec(ctx, `INSERT INTO host_mounts(host_id,mountpoint,state,error) VALUES($1,$2,$3,$4)
		  ON CONFLICT (host_id,mountpoint) DO UPDATE SET state=$3,error=$4,updated_at=now()`, id, m.Mountpoint, m.State, m.Error)
	}
	s.DB.Exec(ctx, `DELETE FROM host_mounts WHERE host_id=$1 AND NOT (mountpoint = ANY($2))`, id, keep)
	applied := ""
	if allOK {
		applied = req.Rev
	}
	s.DB.Exec(ctx, `UPDATE hosts SET applied_hash=$2, agent_version=$3 WHERE id=$1`, id, applied, req.Version)
	for _, e := range req.Events {
		s.audit(ctx, "agent:"+host, host, e.Action, e.Detail)
	}
	jsonOut(w, 200, map[string]string{"status": "ok"})
}
