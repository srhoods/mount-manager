// mmctl: Mount Manager admin CLI.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

type config struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	CAFile   string `json:"ca_file,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

var cfgPath = filepath.Join(os.Getenv("HOME"), ".mmctl.json")
var cfg config

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

const usage = `usage: mmctl <command>
  version
  login <server-url> <username> [--ca file | --insecure]     (password from MM_PASSWORD or prompt-less stdin)
  mount ls [--name text] [--source text] [--mountpoint text] [--type nfs|nfs4|wekafs|fuse.vault-fs] [--limit N] [--offset N]
  mount set <name> <source> <mountpoint> [--type nfs|nfs4|wekafs|fuse.vault-fs] [--opts o1,o2] | clone <id|name> <new-name> | rm <id>
    (a mount was previously called a template; "mmctl template ..." still works)
  group ls | set <name> [--priority N] [--regex RE] | rm <id>
  group add-template <group-id> <mount-id> | rm-template <group-id> <mount-id>
  group add-host <group-id> <host-id> | rm-host <group-id> <host-id>
  host ls [--q text] [--state in-sync|pending|offline|unknown|attention] [--limit N] [--offset N] | show <id> | rm <id>
  token create [--note text] [--hours N] [--uses N] | ls [--all] | revoke <id>
  audit [--host name] [--limit N]`

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, "mmctl: "+f+"\n", a...); os.Exit(1) }

func flagVal(args []string, name string) (string, []string) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], append(append([]string{}, args[:i]...), args[i+2:]...)
		}
	}
	return "", args
}

func flagBool(args []string, name string) (bool, []string) {
	for i, a := range args {
		if a == name {
			return true, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return false, args
}

func client() *http.Client {
	t := &tls.Config{InsecureSkipVerify: cfg.Insecure}
	if cfg.CAFile != "" {
		b, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			die("%v", err)
		}
		p := x509.NewCertPool()
		p.AppendCertsFromPEM(b)
		t.RootCAs = p
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: t}}
}

func call(method, path string, body any) []byte {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, cfg.Server+path, rd)
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := client().Do(req)
	if err != nil {
		die("%v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		json.Unmarshal(out, &e)
		die("%s (HTTP %d)", e.Error, resp.StatusCode)
	}
	return out
}

func humanDuration(secs int64) string {
	switch {
	case secs <= 0:
		return "-"
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh %dm", secs/3600, secs%3600/60)
	}
	return fmt.Sprintf("%dd %dh", secs/86400, secs%86400/3600)
}

func table(data []byte, cols ...string) {
	var rows []map[string]any
	json.Unmarshal(data, &rows)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.ToUpper(strings.Join(cols, "\t")))
	for _, r := range rows {
		var f []string
		for _, c := range cols {
			v := r[c]
			switch x := v.(type) {
			case nil:
				v = "-"
			case float64:
				v = fmt.Sprintf("%.0f", x)
			case []any:
				s := make([]string, len(x))
				for i := range x {
					s[i] = fmt.Sprint(x[i])
				}
				v = strings.Join(s, ",")
			}
			f = append(f, fmt.Sprint(v))
		}
		fmt.Fprintln(w, strings.Join(f, "\t"))
	}
	w.Flush()
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		die("%s", usage)
	}
	if b, err := os.ReadFile(cfgPath); err == nil {
		json.Unmarshal(b, &cfg)
	}
	cmd, args := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		fmt.Println(version)
		return
	}
	need := func(n int) {
		if len(args) < n {
			die("%s", usage)
		}
	}
	sub := func() string {
		if len(args) == 0 {
			die("%s", usage)
		}
		s := args[0]
		args = args[1:]
		return s
	}
	switch cmd {
	case "login":
		ca, a := flagVal(args, "--ca")
		insec, a := flagBool(a, "--insecure")
		args = a
		need(2)
		cfg = config{Server: strings.TrimRight(args[0], "/"), CAFile: ca, Insecure: insec}
		pw := os.Getenv("MM_PASSWORD")
		if pw == "" {
			b, _ := io.ReadAll(os.Stdin)
			pw = strings.TrimSpace(string(b))
		}
		var r struct{ Token string }
		json.Unmarshal(call("POST", "/api/login", map[string]string{"username": args[1], "password": pw}), &r)
		cfg.Token = r.Token
		b, _ := json.Marshal(cfg)
		os.WriteFile(cfgPath, b, 0600)
		fmt.Println("logged in")
	case "mount", "template":
		switch sub() {
		case "ls":
			nm, a := flagVal(args, "--name")
			src, a := flagVal(a, "--source")
			mp, a := flagVal(a, "--mountpoint")
			ty, a := flagVal(a, "--type")
			lim, a := flagVal(a, "--limit")
			off, _ := flagVal(a, "--offset")
			path := "/api/templates?name=" + url.QueryEscape(nm) + "&source=" + url.QueryEscape(src) + "&mountpoint=" + url.QueryEscape(mp) +
				"&type=" + url.QueryEscape(ty) + "&limit=" + lim + "&offset=" + off
			table(call("GET", path, nil), "id", "name", "fstype", "source", "mountpoint", "options", "version")
		case "set":
			typ, a := flagVal(args, "--type")
			opts, a := flagVal(a, "--opts")
			args = a
			need(3)
			fmt.Printf("%s\n", call("POST", "/api/templates", map[string]string{"name": args[0], "fstype": typ, "source": args[1], "mountpoint": args[2], "options": opts}))
		case "clone":
			need(2)
			id := args[0]
			if _, err := strconv.Atoi(id); err != nil { // allow the mount's name instead of its id
				var rows []struct {
					ID   int64
					Name string
				}
				json.Unmarshal(call("GET", "/api/templates", nil), &rows)
				id = ""
				for _, r := range rows {
					if r.Name == args[0] {
						id = strconv.FormatInt(r.ID, 10)
					}
				}
				if id == "" {
					die("no mount named %q", args[0])
				}
			}
			fmt.Printf("%s\n", call("POST", "/api/templates/"+id+"/clone", map[string]string{"name": args[1]}))
		case "rm":
			need(1)
			call("DELETE", "/api/templates/"+args[0], nil)
		default:
			die("%s", usage)
		}
	case "group":
		switch sub() {
		case "ls":
			table(call("GET", "/api/groups", nil), "id", "name", "priority", "host_regex", "templates", "members")
		case "set":
			prio, a := flagVal(args, "--priority")
			rx, a := flagVal(a, "--regex")
			args = a
			need(1)
			p := 0
			fmt.Sscan(prio, &p)
			fmt.Printf("%s\n", call("POST", "/api/groups", map[string]any{"name": args[0], "priority": p, "host_regex": rx}))
		case "rm":
			need(1)
			call("DELETE", "/api/groups/"+args[0], nil)
		case "add-template":
			need(2)
			call("POST", "/api/groups/"+args[0]+"/templates/"+args[1], nil)
		case "rm-template":
			need(2)
			call("DELETE", "/api/groups/"+args[0]+"/templates/"+args[1], nil)
		case "add-host":
			need(2)
			call("POST", "/api/groups/"+args[0]+"/members/"+args[1], nil)
		case "rm-host":
			need(2)
			call("DELETE", "/api/groups/"+args[0]+"/members/"+args[1], nil)
		default:
			die("%s", usage)
		}
	case "host":
		switch sub() {
		case "ls":
			q, a := flagVal(args, "--q")
			st, a := flagVal(a, "--state")
			lim, a := flagVal(a, "--limit")
			off, _ := flagVal(a, "--offset")
			path := "/api/hosts?q=" + url.QueryEscape(q) + "&state=" + url.QueryEscape(st) + "&limit=" + lim + "&offset=" + off
			table(call("GET", path, nil), "id", "hostname", "state", "last_seen", "agent_version", "problems")
		case "show":
			need(1)
			var b bytes.Buffer
			json.Indent(&b, call("GET", "/api/hosts/"+args[0]+"/mounts", nil), "", "  ")
			fmt.Println(b.String())
		case "rm":
			need(1)
			call("DELETE", "/api/hosts/"+args[0], nil)
		default:
			die("%s", usage)
		}
	case "token":
		switch sub() {
		case "create":
			note, a := flagVal(args, "--note")
			hrs, a := flagVal(a, "--hours")
			uses, _ := flagVal(a, "--uses")
			h, u := 24, 1
			fmt.Sscan(hrs, &h)
			fmt.Sscan(uses, &u)
			var r struct{ Token string }
			json.Unmarshal(call("POST", "/api/tokens", map[string]any{"note": note, "hours": h, "uses": u}), &r)
			fmt.Println(r.Token)
		case "ls":
			all, _ := flagBool(args, "--all")
			path := "/api/tokens"
			if all {
				path += "?all=1"
			}
			var rows []map[string]any
			json.Unmarshal(call("GET", path, nil), &rows)
			for _, r := range rows {
				r["uses"] = fmt.Sprintf("%.0f/%.0f", r["uses_left"], r["uses_total"])
				r["left"] = humanDuration(int64(r["seconds_left"].(float64)))
			}
			b, _ := json.Marshal(rows)
			table(b, "id", "status", "note", "uses", "left", "created_by", "expires")
		case "revoke":
			need(1)
			call("DELETE", "/api/tokens/"+args[0], nil)
			fmt.Println("revoked")
		default:
			die("%s", usage)
		}
	case "audit":
		host, a := flagVal(args, "--host")
		lim, _ := flagVal(a, "--limit")
		table(call("GET", "/api/audit?host="+host+"&limit="+lim, nil), "ts", "actor", "host", "action", "detail")
	default:
		die("%s", usage)
	}
}
