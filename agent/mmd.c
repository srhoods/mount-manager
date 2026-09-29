/*
 * mmd - Mount Manager agent.
 * Polls the server over mTLS, reconciles NFS mounts, reports state.
 * Deps: libcurl, OpenSSL. Build: see Makefile.
 */
#define _GNU_SOURCE
#include <curl/curl.h>
#include <errno.h>
#include <fcntl.h>
#include <openssl/ec.h>
#include <openssl/evp.h>
#include <openssl/pem.h>
#include <openssl/sha.h>
#include <openssl/x509.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <syslog.h>
#include <time.h>
#include <unistd.h>

#ifndef VERSION
#define VERSION "dev"
#endif
#define MAXM 512
#define MAXEV 256

struct cfg {
	char server[256], state_dir[256], token_file[256], allowed[256], allowed_exact[256], allowed_fst[128], ca_sha256[80];
	char hostname[256];
	int interval, retry, use_sudo, mount_timeout;
} C = {"", "/var/lib/mountmgr", "/var/lib/mountmgr/enroll.token", "/mnt,/data,/sqpc", "/sqpc", "nfs,nfs4,wekafs", "", "",
       300, 900, 0, 60};

static const char *mountinfo_path = "/proc/self/mountinfo"; /* overridable for tests */

struct mnt { char src[512], mp[512], fst[16], opts[512]; };
struct applied { char mp[512], hash[24]; };
struct ev { char action[32], detail[768]; };

static struct mnt want[MAXM]; static int nwant;
static struct applied app[MAXM]; static int napp;
static struct ev evs[MAXEV]; static int nev;
static char rev[32];
static time_t last_try[MAXM];  /* parallel to want[] retry gating */
static char last_state[MAXM][16], last_err[MAXM][256];
static volatile sig_atomic_t stop;
static char path_key[300], path_crt[300], path_ca[300], path_desired[300], path_applied[300];

static void logf_(int pri, const char *action, const char *fmt, ...)
{
	char buf[700]; va_list ap;
	va_start(ap, fmt); vsnprintf(buf, sizeof buf, fmt, ap); va_end(ap);
	syslog(pri, "action=%s %s", action, buf);
}

static void add_event(const char *action, const char *fmt, ...)
{
	va_list ap;
	if (nev >= MAXEV) { memmove(evs, evs + 1, sizeof(evs[0]) * (MAXEV - 1)); nev--; }
	snprintf(evs[nev].action, sizeof evs[nev].action, "%s", action);
	va_start(ap, fmt); vsnprintf(evs[nev].detail, sizeof evs[nev].detail, fmt, ap); va_end(ap);
	nev++;
}

/* ---------- config ---------- */
static void trim(char *s) { size_t n = strlen(s); while (n && (s[n-1]=='\n'||s[n-1]==' '||s[n-1]=='\r')) s[--n]=0; }

static int load_cfg(const char *path)
{
	FILE *f = fopen(path, "r"); char line[512];
	if (!f) return -1;
	while (fgets(line, sizeof line, f)) {
		char *v = strchr(line, '='); if (!v || line[0]=='#') continue;
		*v++ = 0; trim(v); trim(line);
#define S(k,fld) if(!strcmp(line,k)) snprintf(C.fld,sizeof C.fld,"%s",v)
#define I(k,fld) if(!strcmp(line,k)) C.fld=atoi(v)
		S("server",server); S("state_dir",state_dir); S("enroll_token_file",token_file);
		S("allowed_mount_prefixes",allowed); S("allowed_mount_exact",allowed_exact); S("allowed_fstypes",allowed_fst); S("ca_sha256",ca_sha256); S("hostname",hostname);
		I("interval",interval); I("retry_interval",retry); I("use_sudo",use_sudo); I("mount_timeout",mount_timeout);
	}
	fclose(f); return 0;
}

static int in_list(const char *list, const char *v, int prefix)
{
	char tmp[256], *p, *save; size_t l;
	snprintf(tmp, sizeof tmp, "%s", list);
	for (p = strtok_r(tmp, ",", &save); p; p = strtok_r(NULL, ",", &save)) {
		l = strlen(p);
		if (!l) continue;
		if (prefix ? (!strncmp(v, p, l) && v[l] == '/' && v[l+1]) : !strcmp(v, p)) return 1;
	}
	return 0;
}

/* A mountpoint is permitted when it is below an allowed prefix, or is exactly an allowed path. */
static int allowed_path(const char *mp)
{
	if (mp[0] != '/' || strstr(mp, "..") || strstr(mp, "//")) return 0;
	return in_list(C.allowed, mp, 1) || in_list(C.allowed_exact, mp, 0);
}

static int allowed_fstype(const char *fst) { return fst[0] && in_list(C.allowed_fst, fst, 0); }

/* ---------- small helpers ---------- */
static void hash_mnt(const struct mnt *m, char out[24])
{
	unsigned char d[SHA256_DIGEST_LENGTH]; char in[2048]; int i;
	snprintf(in, sizeof in, "%s|%s|%s|%s", m->src, m->mp, m->fst, m->opts);
	SHA256((unsigned char *)in, strlen(in), d);
	for (i = 0; i < 8; i++) sprintf(out + 2*i, "%02x", d[i]);
}

static char *readfile(const char *p)
{
	FILE *f = fopen(p, "r"); long n; char *b;
	if (!f) return NULL;
	fseek(f, 0, SEEK_END); n = ftell(f); rewind(f);
	b = malloc(n + 1); if (fread(b, 1, n, f) != (size_t)n) { free(b); fclose(f); return NULL; }
	b[n] = 0; fclose(f); return b;
}

static int writefile(const char *p, const char *data, mode_t mode)
{
	char tmp[400]; int fd; snprintf(tmp, sizeof tmp, "%s.tmp", p);
	fd = open(tmp, O_WRONLY|O_CREAT|O_TRUNC, mode); if (fd < 0) return -1;
	if (write(fd, data, strlen(data)) < 0) { close(fd); return -1; }
	fsync(fd); close(fd); return rename(tmp, p);
}

/* ---------- HTTP ---------- */
struct buf { char *d; size_t n; };
static size_t wcb(char *p, size_t s, size_t n, void *u)
{ struct buf *b = u; b->d = realloc(b->d, b->n + s*n + 1); memcpy(b->d + b->n, p, s*n); b->n += s*n; b->d[b->n] = 0; return s*n; }

static char etag_hdr[64];
static size_t hcb(char *p, size_t s, size_t n, void *u)
{
	size_t l = s*n; (void)u;
	if (l > 6 && !strncasecmp(p, "etag:", 5)) { char *v = p + 5; while (*v==' ') v++; snprintf(etag_hdr, sizeof etag_hdr, "%.*s", (int)(l-(v-p)), v); trim(etag_hdr); }
	return l;
}

/* mode: 0 = mTLS with client cert; 1 = server-auth only (verify with CA); 2 = no verification (bootstrap CA fetch) */
static long http(const char *method, const char *path, const char *body, const char *inm, int mode, struct buf *out)
{
	CURL *c = curl_easy_init(); char url[512], hdr[128]; struct curl_slist *h = NULL; long code = 0;
	snprintf(url, sizeof url, "%s%s", C.server, path);
	curl_easy_setopt(c, CURLOPT_URL, url);
	curl_easy_setopt(c, CURLOPT_TIMEOUT, 60L);
	curl_easy_setopt(c, CURLOPT_WRITEFUNCTION, wcb); curl_easy_setopt(c, CURLOPT_WRITEDATA, out);
	curl_easy_setopt(c, CURLOPT_HEADERFUNCTION, hcb);
	if (mode == 2) { curl_easy_setopt(c, CURLOPT_SSL_VERIFYPEER, 0L); curl_easy_setopt(c, CURLOPT_SSL_VERIFYHOST, 0L); }
	else curl_easy_setopt(c, CURLOPT_CAINFO, path_ca);
	if (mode == 0) {
		curl_easy_setopt(c, CURLOPT_SSLCERT, path_crt); curl_easy_setopt(c, CURLOPT_SSLKEY, path_key);
	}
	if (!strcmp(method, "POST")) {
		curl_easy_setopt(c, CURLOPT_POSTFIELDS, body);
		h = curl_slist_append(h, "Content-Type: application/json");
	}
	if (inm) { snprintf(hdr, sizeof hdr, "If-None-Match: %s", inm); h = curl_slist_append(h, hdr); }
	if (h) curl_easy_setopt(c, CURLOPT_HTTPHEADER, h);
	etag_hdr[0] = 0;
	if (curl_easy_perform(c) != CURLE_OK) code = -1; else curl_easy_getinfo(c, CURLINFO_RESPONSE_CODE, &code);
	curl_slist_free_all(h); curl_easy_cleanup(c);
	return code;
}

/* ---------- enrolment / PKI ---------- */
static char newkey[320];

static int gen_key_csr(char **csr_pem)
{
	EVP_PKEY *pk = EVP_EC_gen("P-256"); X509_REQ *rq; X509_NAME *nm; BIO *b; char *p; long n; FILE *f;
	if (!pk) return -1;
	snprintf(newkey, sizeof newkey, "%s.new", path_key);
	f = fopen(newkey, "w"); if (!f) return -1; chmod(newkey, 0600);
	PEM_write_PrivateKey(f, pk, NULL, NULL, 0, NULL, NULL); fclose(f);
	rq = X509_REQ_new(); nm = X509_NAME_new();
	X509_NAME_add_entry_by_txt(nm, "CN", MBSTRING_ASC, (unsigned char *)C.hostname, -1, -1, 0);
	X509_REQ_set_subject_name(rq, nm); X509_REQ_set_pubkey(rq, pk); X509_REQ_sign(rq, pk, EVP_sha256());
	b = BIO_new(BIO_s_mem()); PEM_write_bio_X509_REQ(b, rq); n = BIO_get_mem_data(b, &p);
	*csr_pem = strndup(p, n); BIO_free(b); X509_REQ_free(rq); X509_NAME_free(nm); EVP_PKEY_free(pk);
	return 0;
}

static char *json_escape(const char *s)
{
	char *o = malloc(strlen(s) * 2 + 1), *q = o;
	for (; *s; s++) { if (*s == '\n') { *q++='\\'; *q++='n'; } else if (*s == '"' || *s == '\\') { *q++='\\'; *q++=*s; } else if ((unsigned char)*s >= 0x20) *q++ = *s; }
	*q = 0; return o;
}

static int enroll(int renew)
{
	struct buf b = {0}; char *csr, *esc, *body, *tok = NULL, *end; long code; size_t bl;
	if (!renew) {
		tok = readfile(C.token_file);
		if (!tok) { logf_(LOG_ERR, "enroll", "result=failed error=\"no enrol token at %s\"", C.token_file); return -1; }
		trim(tok);
	}
	if (gen_key_csr(&csr) < 0) { free(tok); return -1; }
	esc = json_escape(csr); bl = strlen(esc) + 512; body = malloc(bl);
	snprintf(body, bl, "{\"token\":\"%s\",\"hostname\":\"%s\",\"csr\":\"%s\",\"version\":\"%s\"}", tok ? tok : "", C.hostname, esc, VERSION);
	code = http("POST", "/v1/enroll", body, NULL, renew ? 0 : 1, &b);
	free(csr); free(esc); free(body); free(tok);
	if (code != 200 || !b.d || !(end = strstr(b.d, "-----END CERTIFICATE-----"))) {
		logf_(LOG_ERR, "enroll", "result=failed http=%ld", code); free(b.d); return -1;
	}
	end += strlen("-----END CERTIFICATE-----\n");
	{ char save = *end; *end = 0; writefile(path_crt, b.d, 0644); *end = save; }
	rename(newkey, path_key);
	free(b.d);
	logf_(LOG_NOTICE, "enroll", "result=ok host=%s", C.hostname);
	if (!renew) unlink(C.token_file);
	return 0;
}

static int fetch_ca(void)
{
	struct buf b = {0}; long code; unsigned char d[SHA256_DIGEST_LENGTH]; char hex[80]; int i;
	code = http("GET", "/v1/ca", NULL, NULL, 2, &b);
	if (code != 200 || !b.d) { free(b.d); logf_(LOG_ERR, "fetch-ca", "result=failed http=%ld", code); return -1; }
	SHA256((unsigned char *)b.d, b.n, d);
	for (i = 0; i < 32; i++) sprintf(hex + 2*i, "%02x", d[i]);
	if (C.ca_sha256[0] && strcasecmp(hex, C.ca_sha256)) {
		logf_(LOG_ERR, "fetch-ca", "result=failed error=\"CA fingerprint mismatch got=%s\"", hex); free(b.d); return -1;
	}
	if (!C.ca_sha256[0]) logf_(LOG_WARNING, "fetch-ca", "trust-on-first-use ca_sha256=%s (pin via ca_sha256 in config)", hex);
	writefile(path_ca, b.d, 0644); free(b.d); return 0;
}

static int cert_days_left(void)
{
	FILE *f = fopen(path_crt, "r"); X509 *x; int days = 0, sec;
	if (!f) return -1;
	x = PEM_read_X509(f, NULL, NULL, NULL); fclose(f); if (!x) return -1;
	ASN1_TIME_diff(&days, &sec, NULL, X509_get0_notAfter(x)); X509_free(x);
	return days;
}

/* ---------- state files ---------- */
static void load_applied(void)
{
	FILE *f = fopen(path_applied, "r"); char l[1100]; napp = 0;
	if (!f) return;
	while (napp < MAXM && fgets(l, sizeof l, f)) {
		char *t = strchr(l, '\t'); if (!t) continue; *t++ = 0; trim(t);
		snprintf(app[napp].mp, sizeof app[napp].mp, "%s", l); snprintf(app[napp].hash, sizeof app[napp].hash, "%s", t); napp++;
	}
	fclose(f);
}

static void save_applied(void)
{
	char *b = calloc(1, napp * 560 + 1); int i;
	for (i = 0; i < napp; i++) sprintf(b + strlen(b), "%s\t%s\n", app[i].mp, app[i].hash);
	writefile(path_applied, b, 0600); free(b);
}

static struct applied *find_app(const char *mp)
{ int i; for (i = 0; i < napp; i++) if (!strcmp(app[i].mp, mp)) return &app[i]; return NULL; }

static void set_app(const char *mp, const char *hash)
{
	struct applied *a = find_app(mp);
	if (!a && napp < MAXM) { a = &app[napp++]; snprintf(a->mp, sizeof a->mp, "%s", mp); }
	if (a) snprintf(a->hash, sizeof a->hash, "%s", hash);
	save_applied();
}

static void del_app(const char *mp)
{
	int i; for (i = 0; i < napp; i++) if (!strcmp(app[i].mp, mp)) { app[i] = app[--napp]; break; }
	save_applied();
}

static int parse_desired(const char *txt)
{
	char *cp = strdup(txt), *save, *line;
	struct mnt old[MAXM]; time_t otry[MAXM]; char ost[MAXM][16], oerr[MAXM][256]; int nold = nwant, i, j;
	/* keep retry/status runtime state attached to the mountpoint, not the array slot */
	memcpy(old, want, sizeof(struct mnt) * nold); memcpy(otry, last_try, sizeof otry);
	memcpy(ost, last_state, sizeof ost); memcpy(oerr, last_err, sizeof oerr);
	nwant = 0; rev[0] = 0;
	for (line = strtok_r(cp, "\n", &save); line; line = strtok_r(NULL, "\n", &save)) {
		char *f[6], *s2 = line; int n = 0;
		while (n < 6 && (f[n] = strsep(&s2, "\t"))) n++;
		if (n >= 2 && !strcmp(f[0], "rev")) snprintf(rev, sizeof rev, "%s", f[1]);
		else if (n >= 5 && !strcmp(f[0], "mount") && nwant < MAXM) {
			snprintf(want[nwant].src, sizeof want[nwant].src, "%s", f[1]);
			snprintf(want[nwant].mp, sizeof want[nwant].mp, "%s", f[2]);
			snprintf(want[nwant].fst, sizeof want[nwant].fst, "%s", f[3]);
			snprintf(want[nwant].opts, sizeof want[nwant].opts, "%s", f[4]);
			nwant++;
		}
	}
	free(cp);
	for (i = 0; i < nwant; i++) {
		last_try[i] = 0; last_state[i][0] = 0; last_err[i][0] = 0;
		for (j = 0; j < nold; j++) if (!strcmp(old[j].mp, want[i].mp)) {
			last_try[i] = otry[j]; memcpy(last_state[i], ost[j], sizeof last_state[i]); memcpy(last_err[i], oerr[j], sizeof last_err[i]);
		}
	}
	return rev[0] ? 0 : -1;
}

/* ---------- mounts ---------- */
static int is_mounted(const char *mp, char *src, size_t sl, char *fst, size_t fl)
{
	FILE *f = fopen(mountinfo_path, "r"); char l[4096]; int found = 0;
	if (!f) return 0;
	if (src) src[0] = 0;
	if (fst) fst[0] = 0;
	while (fgets(l, sizeof l, f)) {
		/* id parent maj:min root mountpoint opts [optional...] - fstype source superopts */
		char *p = l, *tok, *dash; int i; char *mpt = NULL;
		for (i = 0; i < 5 && (tok = strsep(&p, " ")); i++) if (i == 4) mpt = tok;
		if (!mpt || strcmp(mpt, mp)) continue;
		found = 1; /* last match wins (stacked mounts) */
		if ((dash = strstr(p ? p : "", " - "))) {
			char *q = dash + 3, *t1, *t2; t1 = strsep(&q, " "); t2 = strsep(&q, " ");
			if (t1 && fst) snprintf(fst, fl, "%s", t1);
			if (t2 && src) snprintf(src, sl, "%s", t2);
		}
	}
	fclose(f); return found;
}

/* run argv (optionally via sudo), capture stderr (trimmed) into err. returns exit status (0 = ok) */
static int run(char *const argv[], char *err, size_t el)
{
	int pfd[2], st = 0, i, n = 0; pid_t pid; char *av[16]; time_t t0 = time(NULL); ssize_t r; size_t got = 0;
	if (C.use_sudo) { av[n++] = "sudo"; av[n++] = "-n"; }
	for (i = 0; argv[i] && n < 15; i++) av[n++] = argv[i];
	av[n] = NULL; err[0] = 0;
	if (pipe(pfd) < 0) return -1;
	pid = fork();
	if (pid == 0) { dup2(pfd[1], 2); dup2(pfd[1], 1); close(pfd[0]); close(pfd[1]); execvp(av[0], av); _exit(127); }
	close(pfd[1]); fcntl(pfd[0], F_SETFL, O_NONBLOCK);
	for (;;) {
		pid_t w = waitpid(pid, &st, WNOHANG);
		while ((r = read(pfd[0], err + got, el - 1 - got)) > 0) { got += r; if (got >= el - 1) break; }
		if (w == pid) break;
		if (time(NULL) - t0 > C.mount_timeout) { kill(pid, SIGKILL); waitpid(pid, &st, 0); snprintf(err, el, "timed out after %ds", C.mount_timeout); close(pfd[0]); return -2; }
		usleep(100000);
	}
	while ((r = read(pfd[0], err + got, el - 1 - got)) > 0) got += r;
	err[got] = 0; trim(err); for (i = 0; err[i]; i++) if (err[i] == '\n' || err[i] == '"') err[i] = ' ';
	close(pfd[0]); return WIFEXITED(st) ? WEXITSTATUS(st) : -1;
}

static int do_umount(const char *mp, char *err, size_t el)
{ char *av[] = {"umount", (char *)mp, NULL}; return run(av, err, el); }

static int do_mount(const struct mnt *m, char *err, size_t el)
{
	char *av[10]; int n = 0; char mk[600];
	snprintf(mk, sizeof mk, "%s", m->mp);
	{ char *av2[] = {"mkdir", "-p", mk, NULL}; char e2[128]; run(av2, e2, sizeof e2); }
	av[n++] = "mount"; av[n++] = "-t"; av[n++] = (char *)m->fst;
	if (m->opts[0]) { av[n++] = "-o"; av[n++] = (char *)m->opts; }
	av[n++] = (char *)m->src; av[n++] = (char *)m->mp; av[n] = NULL;
	return run(av, err, el);
}

static void setst(int i, const char *st, const char *e)
{ snprintf(last_state[i], sizeof last_state[i], "%s", st); snprintf(last_err[i], sizeof last_err[i], "%s", e); }

/* Unmount mounts we created that are no longer desired. Deepest paths first, and before any new mounts are
 * made, so a new parent mount can never hide a nested child we still have to unmount. */
static void remove_unwanted(void)
{
	char todo[MAXM][512], src[512], mfst[32], err[512]; int n = 0, i, j;
	for (j = 0; j < napp && n < MAXM; j++) {
		int keep = 0; for (i = 0; i < nwant; i++) if (!strcmp(want[i].mp, app[j].mp)) keep = 1;
		if (!keep) snprintf(todo[n++], sizeof todo[0], "%s", app[j].mp);
	}
	for (i = 0; i < n; i++) for (j = i + 1; j < n; j++)      /* longest (deepest) first */
		if (strlen(todo[j]) > strlen(todo[i])) { char t[512]; memcpy(t, todo[i], sizeof t); memcpy(todo[i], todo[j], sizeof t); memcpy(todo[j], t, sizeof t); }
	for (i = 0; i < n; i++) {
		if (!is_mounted(todo[i], src, sizeof src, mfst, sizeof mfst) || do_umount(todo[i], err, sizeof err) == 0) {
			logf_(LOG_NOTICE, "umount", "mountpoint=%s result=ok reason=removed-from-config", todo[i]);
			add_event("umount", "%s (removed from config)", todo[i]); del_app(todo[i]);
		} else {
			logf_(LOG_WARNING, "umount", "mountpoint=%s result=busy error=\"%s\" will_retry=next-cycle", todo[i], err);
		}
	}
}

static void reconcile(void)
{
	remove_unwanted();
	int i, j; char src[512], mfst[32], err[512], h[24];
	for (i = 0; i < nwant; i++) {
		struct mnt *m = &want[i]; struct applied *a; int mounted;
		hash_mnt(m, h);
		if (!allowed_path(m->mp)) {
			setst(i, "failed", "mountpoint not permitted by agent allowed_mount_prefixes/allowed_mount_exact");
			logf_(LOG_WARNING, "mount", "mountpoint=%s result=refused reason=\"not an allowed mountpoint\"", m->mp);
			continue;
		}
		if (!allowed_fstype(m->fst)) {
			setst(i, "failed", "filesystem type not permitted by agent allowed_fstypes");
			logf_(LOG_WARNING, "mount", "mountpoint=%s type=%s result=refused reason=\"fstype not allowed\"", m->mp, m->fst);
			continue;
		}
		mounted = is_mounted(m->mp, src, sizeof src, mfst, sizeof mfst); a = find_app(m->mp);
		if (mounted && a && !strcmp(a->hash, h)) { setst(i, "ok", ""); continue; }
		/* Adopt a pre-existing mount (e.g. deployed by Ansible) instead of disrupting it. NFS is matched on
		 * source; wekafs sources are reported in varying forms, so the filesystem type is enough there. */
		if (mounted && !a && (!strcmp(src, m->src) || (!strncmp(m->fst, "nfs", 3) ? 0 : !strcmp(mfst, m->fst)))) {
			set_app(m->mp, h); setst(i, "ok", "");
			logf_(LOG_NOTICE, "adopt", "mountpoint=%s source=%s", m->mp, m->src);
			add_event("adopt", "%s already mounted from %s", m->mp, m->src); continue;
		}
		if (last_try[i] && strcmp(last_state[i], "ok") && time(NULL) - last_try[i] < C.retry) continue; /* honour retry interval */
		last_try[i] = time(NULL);
		if (mounted) {
			int rc = do_umount(m->mp, err, sizeof err);
			if (rc != 0) {
				char msg[300]; snprintf(msg, sizeof msg, "umount failed: %s", err);
				setst(i, "pending", msg);
				logf_(LOG_WARNING, "umount", "mountpoint=%s result=busy error=\"%s\" will_retry=%ds", m->mp, err, C.retry);
				add_event("umount-failed", "%s: %s", m->mp, err); continue;
			}
			logf_(LOG_NOTICE, "umount", "mountpoint=%s result=ok reason=config-change", m->mp);
			add_event("umount", "%s (config change)", m->mp);
		}
		if (do_mount(m, err, sizeof err) == 0) {
			set_app(m->mp, h); setst(i, "ok", ""); last_try[i] = 0;
			logf_(LOG_NOTICE, "mount", "mountpoint=%s source=%s type=%s options=%s result=ok", m->mp, m->src, m->fst, m->opts);
			add_event("mount", "%s from %s type=%s opts=%s", m->mp, m->src, m->fst, m->opts);
		} else {
			setst(i, "failed", err);
			logf_(LOG_ERR, "mount", "mountpoint=%s source=%s result=failed error=\"%s\"", m->mp, m->src, err);
			add_event("mount-failed", "%s: %s", m->mp, err);
		}
	}
}

static int report(void)
{
	size_t cap = 4096 + nwant * 700 + nev * 900, len = 0; char *b = malloc(cap), *e1, *e2; int i; struct buf out = {0}; long code;
	len += snprintf(b + len, cap - len, "{\"version\":\"%s\",\"rev\":\"%s\",\"mounts\":[", VERSION, rev);
	for (i = 0; i < nwant; i++) {
		e1 = json_escape(want[i].mp); e2 = json_escape(last_err[i]);
		len += snprintf(b + len, cap - len, "%s{\"mountpoint\":\"%s\",\"state\":\"%s\",\"error\":\"%s\"}", i ? "," : "", e1, last_state[i][0] ? last_state[i] : "pending", e2);
		free(e1); free(e2);
	}
	len += snprintf(b + len, cap - len, "],\"events\":[");
	for (i = 0; i < nev; i++) {
		e2 = json_escape(evs[i].detail);
		len += snprintf(b + len, cap - len, "%s{\"action\":\"%s\",\"detail\":\"%s\"}", i ? "," : "", evs[i].action, e2); free(e2);
	}
	snprintf(b + len, cap - len, "]}");
	code = http("POST", "/v1/report", b, NULL, 0, &out);
	free(b); free(out.d);
	if (code == 200) { nev = 0; return 0; }
	logf_(LOG_WARNING, "report", "result=failed http=%ld (events retained)", code);
	return -1;
}

static void cycle(void)
{
	struct buf b = {0}; long code; char inm[64] = "";
	int d = cert_days_left();
	if (d >= 0 && d < 7) enroll(1);
	if (rev[0]) snprintf(inm, sizeof inm, "\"%s\"", rev);
	code = http("GET", "/v1/desired-state", NULL, inm[0] ? inm : NULL, 0, &b);
	if (code == 200 && b.d) {
		if (parse_desired(b.d) == 0) writefile(path_desired, b.d, 0600);
		else logf_(LOG_ERR, "poll", "result=failed error=\"unparseable desired state\"");
	} else if (code == 304) {
		/* unchanged; still reconcile below to detect drift and retry pending work */
	} else {
		logf_(LOG_WARNING, "poll", "result=failed http=%ld (server unreachable, using cached state)", code);
	}
	free(b.d);
	if (rev[0]) { reconcile(); report(); }
}

static void onsig(int s) { (void)s; stop = 1; }

int main(int argc, char **argv)
{
	const char *cfgp = "/etc/mountmgr/agent.conf"; int once = 0, fg = 0, i; char *cached;
	for (i = 1; i < argc; i++) {
		if (!strcmp(argv[i], "-c") && i + 1 < argc) cfgp = argv[++i];
		else if (!strcmp(argv[i], "-1")) once = 1;
		else if (!strcmp(argv[i], "-f")) fg = 1;
		else if (!strcmp(argv[i], "-V")) { puts(VERSION); return 0; }
		else { fprintf(stderr, "usage: mmd [-c config] [-f foreground] [-1 run once]\n"); return 2; }
	}
	openlog("mountmgr", LOG_PID | ((fg && isatty(2)) ? LOG_PERROR : 0), LOG_DAEMON);
	if (load_cfg(cfgp) < 0) { fprintf(stderr, "mmd: cannot read %s\n", cfgp); return 1; }
	if (!C.server[0]) { fprintf(stderr, "mmd: server not configured\n"); return 1; }
	if (!C.hostname[0]) gethostname(C.hostname, sizeof C.hostname);
	mkdir(C.state_dir, 0700);
	snprintf(path_key, sizeof path_key, "%s/client.key", C.state_dir);
	snprintf(path_crt, sizeof path_crt, "%s/client.crt", C.state_dir);
	snprintf(path_ca, sizeof path_ca, "%s/ca.crt", C.state_dir);
	snprintf(path_desired, sizeof path_desired, "%s/desired.tsv", C.state_dir);
	snprintf(path_applied, sizeof path_applied, "%s/applied.tsv", C.state_dir);
	curl_global_init(CURL_GLOBAL_DEFAULT); srand(getpid() ^ time(NULL));
	signal(SIGTERM, onsig); signal(SIGINT, onsig);
	if (!fg && !once && daemon(0, 0) < 0) return 1;
	logf_(LOG_NOTICE, "start", "version=%s server=%s interval=%d", VERSION, C.server, C.interval);
	load_applied();
	if ((cached = readfile(path_desired))) { parse_desired(cached); free(cached); /* keep cached state so we can reconcile while offline */ }
	while (!stop) {
		if (access(path_ca, R_OK) != 0 && fetch_ca() < 0) goto sleep;
		if (access(path_crt, R_OK) != 0 && enroll(0) < 0) goto sleep;
		cycle();
sleep:
		if (once) break;
		for (i = C.interval + rand() % 30; i > 0 && !stop; i--) sleep(1);
	}
	logf_(LOG_NOTICE, "stop", "version=%s", VERSION);
	return 0;
}
