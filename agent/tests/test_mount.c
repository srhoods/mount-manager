/*
 * Mount-logic tests for mmd. The agent source is compiled into this binary (main renamed) so the real
 * reconcile/adopt/retry code runs against fake mount/umount/sudo commands (tests/fakebin) and a fake
 * mountinfo file. No root, network or NFS required. Run: make check
 */
#define main mmd_main
#include "../mmd.c"
#undef main

static int checks, failures;
#define CHECK(cond) do { checks++; if (!(cond)) { failures++; printf("    FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond); } } while (0)
#define CHECK_STR(a, b) do { checks++; if (strcmp((a), (b))) { failures++; printf("    FAIL %s:%d: \"%s\" != \"%s\"\n", __FILE__, __LINE__, (a), (b)); } } while (0)

static char T[256], FAKE[300], MP[300];   /* temp root, fake state dir, mountpoint base */

static void sh(const char *fmt, ...)
{
	char cmd[1024]; va_list ap; va_start(ap, fmt); vsnprintf(cmd, sizeof cmd, fmt, ap); va_end(ap);
	if (system(cmd) != 0) { printf("    setup command failed: %s\n", cmd); failures++; }
}

static void setup(void)
{
	char fp[400];
	snprintf(T, sizeof T, "/tmp/mmtest.XXXXXX"); if (!mkdtemp(T)) { perror("mkdtemp"); exit(2); }
	snprintf(FAKE, sizeof FAKE, "%s/fake", T); snprintf(MP, sizeof MP, "%s/mnt", T);
	sh("mkdir -p %s/state %s && : > %s/mountinfo && : > %s/calls.log", T, FAKE, FAKE, FAKE);
	sh("mkdir -p %s %s/state", MP, T);
	snprintf(fp, sizeof fp, "%s/mountinfo", FAKE);
	setenv("MM_FAKE", FAKE, 1);
	{ char path[1024]; snprintf(path, sizeof path, "%s/tests/fakebin:%s", getenv("MM_AGENT_DIR") ? getenv("MM_AGENT_DIR") : ".", getenv("MM_ORIG_PATH")); setenv("PATH", path, 1); }
	mountinfo_path = strdup(fp);
	snprintf(C.state_dir, sizeof C.state_dir, "%s/state", T);
	snprintf(path_applied, sizeof path_applied, "%s/state/applied.tsv", T);
	snprintf(C.allowed, sizeof C.allowed, "%s", MP); C.allowed_exact[0] = 0;
	snprintf(C.allowed_fst, sizeof C.allowed_fst, "nfs,nfs4,wekafs");
	C.use_sudo = 0; C.retry = 900; C.mount_timeout = 5;
	nwant = napp = nev = 0; rev[0] = 0;
	memset(last_try, 0, sizeof last_try); memset(last_state, 0, sizeof last_state); memset(last_err, 0, sizeof last_err);
}

static void teardown(void) { sh("rm -rf %s", T); }

static void desire(const char *fmt, ...)   /* build a desired-state TSV and parse it */
{
	char body[4096], mnt[1024]; va_list ap; va_start(ap, fmt); vsnprintf(mnt, sizeof mnt, fmt, ap); va_end(ap);
	snprintf(body, sizeof body, "rev\tr%d\n%s", checks, mnt);
	CHECK(parse_desired(body) == 0);
}

static int calls(const char *prefix)   /* count logged fake-command calls starting with prefix */
{
	char p[400], line[1024]; int n = 0; FILE *f;
	snprintf(p, sizeof p, "%s/calls.log", FAKE); f = fopen(p, "r"); if (!f) return 0;
	while (fgets(line, sizeof line, f)) if (!strncmp(line, prefix, strlen(prefix))) n++;
	fclose(f); return n;
}

static int calls_total(void) { return calls(""); }
static void clear_calls(void) { char p[400]; snprintf(p, sizeof p, "%s/calls.log", FAKE); FILE *f = fopen(p, "w"); if (f) fclose(f); }

static int call_index(const char *needle)   /* 1-based line of first call containing needle, 0 if none */
{
	char p[400], line[1024]; int n = 0; FILE *f;
	snprintf(p, sizeof p, "%s/calls.log", FAKE); f = fopen(p, "r"); if (!f) return 0;
	while (fgets(line, sizeof line, f)) { n++; if (strstr(line, needle)) { fclose(f); return n; } }
	fclose(f); return 0;
}

static int mounted_at(const char *mp) { char s[512], f[32]; return is_mounted(mp, s, sizeof s, f, sizeof f); }
static void touch(const char *name) { sh("touch %s/%s", FAKE, name); }
static void untouch(const char *name) { sh("rm -f %s/%s", FAKE, name); }
static void set_busy(const char *mp) { sh("echo '%s' > %s/busy", mp, FAKE); }
static int has_event(const char *action) { int i; for (i = 0; i < nev; i++) if (!strcmp(evs[i].action, action)) return 1; return 0; }
static void expire_retry(void) { int i; for (i = 0; i < MAXM; i++) if (last_try[i]) last_try[i] = 1; }
static void preload_mount(const char *mp, const char *fst, const char *src)
{ sh("echo '77 1 0:9 / %s rw - %s %s rw' >> %s/mountinfo", mp, fst, src, FAKE); }

#define TEST(name) static void name(void)
#define RUN(name) do { printf("  %-46s", #name); int before = failures; setup(); name(); teardown(); printf("%s\n", failures == before ? "ok" : "FAILED"); } while (0)

/* ---------------------------------------------------------------- tests */

TEST(parses_desired_state)
{
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw,hard\nmount\tbackend/fs1\t%s/b\twekafs\trw\n", MP, MP);
	CHECK(nwant == 2);
	CHECK_STR(want[0].src, "lumpy:/a"); CHECK_STR(want[0].fst, "nfs"); CHECK_STR(want[0].opts, "rw,hard");
	CHECK_STR(want[1].fst, "wekafs"); CHECK_STR(want[1].src, "backend/fs1");
	CHECK(parse_desired("garbage\n") != 0);          /* no rev line -> rejected */
}

TEST(fresh_mount_then_idempotent)
{
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw,hard\n", MP);
	reconcile();
	CHECK(calls("mount -t nfs -o rw,hard lumpy:/a") == 1);
	CHECK(mounted_at(want[0].mp));
	CHECK_STR(last_state[0], "ok");
	CHECK(has_event("mount"));
	CHECK(find_app(want[0].mp) != NULL);
	clear_calls(); nev = 0;
	reconcile(); reconcile();
	CHECK(calls_total() == 0);                        /* steady state does nothing */
	CHECK(nev == 0);
}

TEST(option_change_unmounts_then_remounts)
{
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw,hard\n", MP); reconcile();
	clear_calls();
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw,soft\n", MP); reconcile();
	CHECK(calls("umount") == 1 && calls("mount -t nfs -o rw,soft") == 1);
	CHECK(call_index("umount") < call_index("mount -t"));   /* dismount first, then remount */
	CHECK_STR(last_state[0], "ok");
}

TEST(busy_umount_is_pending_and_retried)
{
	char a[400]; snprintf(a, sizeof a, "%s/a", MP);
	desire("mount\tlumpy:/a\t%s\tnfs\trw,hard\n", a); reconcile();
	set_busy(a); clear_calls();
	desire("mount\tlumpy:/a\t%s\tnfs\trw,soft\n", a); reconcile();
	CHECK_STR(last_state[0], "pending");
	CHECK(strstr(last_err[0], "busy") != NULL);
	CHECK(calls("mount -t") == 0);                    /* never remounts over a busy mount */
	CHECK(mounted_at(a));                             /* old mount left intact */
	CHECK(has_event("umount-failed"));
	clear_calls();
	reconcile();
	CHECK(calls_total() == 0);                        /* inside the retry interval: hands off */
	expire_retry(); reconcile();
	CHECK(calls("umount") == 1);                      /* retried after the interval */
	CHECK_STR(last_state[0], "pending");
	untouch("busy"); expire_retry(); reconcile();     /* file closed: converges */
	CHECK_STR(last_state[0], "ok");
	{ char o[100] = ""; char p[400]; FILE *f; snprintf(p, sizeof p, "%s/last_opts", FAKE); f = fopen(p, "r"); if (f) { if (fgets(o, sizeof o, f)) trim(o); fclose(f); } CHECK_STR(o, "rw,soft"); }
}

TEST(mount_failure_reports_error_and_honours_retry_interval)
{
	touch("fail_mount");
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\n", MP); reconcile();
	CHECK_STR(last_state[0], "failed");
	CHECK(strstr(last_err[0], "Connection timed out") != NULL);
	CHECK(find_app(want[0].mp) == NULL);              /* failed mounts are not recorded as applied */
	clear_calls(); reconcile();
	CHECK(calls_total() == 0);
	untouch("fail_mount"); expire_retry(); reconcile();
	CHECK_STR(last_state[0], "ok");
}

TEST(mount_timeout_kills_hung_mount)
{
	time_t t0 = time(NULL);
	touch("hang_mount"); C.mount_timeout = 1;
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\n", MP); reconcile();
	CHECK(time(NULL) - t0 < 10);
	CHECK_STR(last_state[0], "failed");
	CHECK(strstr(last_err[0], "timed out") != NULL);
}

TEST(mount_removed_from_config_is_unmounted)
{
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\nmount\tlumpy:/b\t%s/b\tnfs\trw\n", MP, MP); reconcile();
	clear_calls();
	desire("mount\tlumpy:/b\t%s/b\tnfs\trw\n", MP); reconcile();
	CHECK(calls("umount") == 1);
	{ char a[400]; snprintf(a, sizeof a, "%s/a", MP); CHECK(!mounted_at(a)); CHECK(find_app(a) == NULL); }
	{ char b[400]; snprintf(b, sizeof b, "%s/b", MP); CHECK(mounted_at(b)); CHECK(find_app(b) != NULL); }
}

TEST(busy_removal_keeps_tracking_for_next_cycle)
{
	char a[400]; snprintf(a, sizeof a, "%s/a", MP);
	desire("mount\tlumpy:/a\t%s\tnfs\trw\n", a); reconcile();
	set_busy(a);
	desire("rev\tx\n"); parse_desired("rev\tr-empty\n");   /* mount removed */
	reconcile();
	CHECK(mounted_at(a)); CHECK(find_app(a) != NULL);       /* still ours, will retry */
	untouch("busy"); reconcile();
	CHECK(!mounted_at(a)); CHECK(find_app(a) == NULL);
}

TEST(removals_happen_before_new_mounts)
{
	char child[400]; snprintf(child, sizeof child, "%s/p/child", MP);
	desire("mount\tlumpy:/c\t%s\tnfs\trw\n", child); reconcile();
	clear_calls();
	/* next config: child gone, new parent mount over its path. The child must be unmounted first,
	 * otherwise the parent mount would hide it and the umount would fail. */
	desire("mount\tlumpy:/p\t%s/p\tnfs\trw\n", MP); reconcile();
	CHECK(call_index("umount") > 0 && call_index("mount -t") > 0);
	CHECK(call_index("umount") < call_index("mount -t"));
	CHECK(!mounted_at(child)); CHECK(find_app(child) == NULL);
	{ char p[400]; snprintf(p, sizeof p, "%s/p", MP); CHECK(mounted_at(p)); CHECK_STR(last_state[0], "ok"); }
}

TEST(nested_removals_unmount_deepest_first)
{
	char p[400], c[400]; snprintf(p, sizeof p, "%s/p", MP); snprintf(c, sizeof c, "%s/p/c", MP);
	desire("mount\tlumpy:/p\t%s\tnfs\trw\nmount\tlumpy:/c\t%s\tnfs\trw\n", p, c); reconcile();
	clear_calls();
	parse_desired("rev\tempty\n"); reconcile();
	CHECK(calls("umount") == 2);
	{ char uc[450], up[450]; snprintf(uc, sizeof uc, "umount %s\n", c); snprintf(up, sizeof up, "umount %s\n", p);  /* exact lines: p is a prefix of c */
	  CHECK(call_index(uc) > 0 && call_index(up) > 0 && call_index(uc) < call_index(up)); }
	CHECK(napp == 0);
}

TEST(never_unmounts_mounts_it_did_not_create)
{
	char x[400]; snprintf(x, sizeof x, "%s/manual", MP);
	preload_mount(x, "nfs4", "other:/x");
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\n", MP); reconcile();
	CHECK(mounted_at(x));
	CHECK(calls("umount") == 0);
}

TEST(adopts_existing_nfs_mount_with_same_source)
{
	char a[400]; snprintf(a, sizeof a, "%s/a", MP);
	preload_mount(a, "nfs4", "lumpy:/a");
	desire("mount\tlumpy:/a\t%s\tnfs\trw,hard\n", a); reconcile();
	CHECK(calls_total() == 0);                        /* not disturbed */
	CHECK_STR(last_state[0], "ok"); CHECK(find_app(a) != NULL); CHECK(has_event("adopt"));
}

TEST(remounts_existing_nfs_mount_with_different_source)
{
	char a[400]; snprintf(a, sizeof a, "%s/a", MP);
	preload_mount(a, "nfs4", "elsewhere:/old");
	desire("mount\tlumpy:/a\t%s\tnfs\trw\n", a); reconcile();
	CHECK(calls("umount") == 1 && calls("mount -t nfs -o rw lumpy:/a") == 1);
	CHECK_STR(last_state[0], "ok");
}

TEST(adopts_existing_wekafs_mount_whatever_the_source_form)
{
	char w[400]; snprintf(w, sizeof w, "%s/w", MP);
	preload_mount(w, "wekafs", "10.0.0.1/fs1");        /* kernel-reported form differs from template */
	desire("mount\tbackend0/fs1\t%s\twekafs\trw\n", w); reconcile();
	CHECK(calls_total() == 0); CHECK_STR(last_state[0], "ok"); CHECK(has_event("adopt"));
}

TEST(wekafs_mount_uses_wekafs_type)
{
	desire("mount\tbackend0/fs1\t%s/w\twekafs\trw,readcache\n", MP); reconcile();
	CHECK(calls("mount -t wekafs -o rw,readcache backend0/fs1") == 1);
	CHECK_STR(last_state[0], "ok");
}

TEST(disallowed_fstype_is_refused)
{
	desire("mount\t/dev/sda1\t%s/l\text4\trw\nmount\tx:/y\t%s/m\tcifs\trw\n", MP, MP); reconcile();
	CHECK(calls_total() == 0);
	CHECK_STR(last_state[0], "failed"); CHECK(strstr(last_err[0], "not permitted") != NULL);
	CHECK_STR(last_state[1], "failed");
}

TEST(mountpoint_allow_list)
{
	char ok[400], out[400], dots[400];
	snprintf(ok, sizeof ok, "%s/deep/er", MP); snprintf(out, sizeof out, "/etc");
	snprintf(dots, sizeof dots, "%s/../etc", MP);
	CHECK(allowed_path(ok));
	CHECK(!allowed_path(out));
	CHECK(!allowed_path(dots));                       /* traversal */
	CHECK(!allowed_path(MP));                         /* the prefix itself is not a valid child */
	CHECK(!allowed_path("relative/path"));
	{ char sib[400]; snprintf(sib, sizeof sib, "%sx/a", MP); CHECK(!allowed_path(sib)); }   /* /mntx is not under /mnt */
	desire("mount\tlumpy:/a\t/etc/passwd.d\tnfs\trw\n"); reconcile();
	CHECK(calls_total() == 0); CHECK_STR(last_state[0], "failed");
}

TEST(sqpc_default_paths)
{
	struct cfg saved = C;
	struct cfg def = { "", "", "", "/mnt,/data,/sqpc", "/sqpc", "nfs,nfs4,wekafs", "", "", "", 300, 900, 0, 60 };
	C = def;
	CHECK(allowed_path("/sqpc"));                     /* exact */
	CHECK(allowed_path("/sqpc/projects"));            /* below */
	CHECK(allowed_path("/sqpc/a/b"));
	CHECK(!allowed_path("/sqpcx"));
	CHECK(!allowed_path("/sqpc/"));
	CHECK(!allowed_path("/sqpc/../etc"));
	CHECK(allowed_path("/mnt/data1"));
	CHECK(!allowed_path("/mnt"));                     /* pre-existing behaviour unchanged */
	C = saved;
}

TEST(exact_mountpoint_can_be_mounted)
{
	snprintf(C.allowed_exact, sizeof C.allowed_exact, "%s/exact", MP);
	desire("mount\tbackend0/fs1\t%s/exact\twekafs\trw\n", MP); reconcile();
	CHECK_STR(last_state[0], "ok");
}

TEST(runtime_state_follows_the_mountpoint_not_the_slot)
{
	touch("fail_mount");
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\nmount\tlumpy:/b\t%s/b\tnfs\trw\n", MP, MP);
	reconcile();
	CHECK_STR(last_state[0], "failed"); CHECK_STR(last_state[1], "failed");
	untouch("fail_mount"); expire_retry();
	desire("mount\tlumpy:/b\t%s/b\tnfs\trw\n", MP);     /* /a removed: /b shifts to slot 0 */
	CHECK_STR(last_state[0], "failed");                  /* carries /b's own state */
	CHECK(last_try[0] != 0);
	reconcile();
	CHECK_STR(last_state[0], "ok");
	desire("mount\tlumpy:/c\t%s/c\tnfs\trw\nmount\tlumpy:/b\t%s/b\tnfs\trw\n", MP, MP);  /* new mount inserted before /b */
	CHECK(last_state[0][0] == 0);                        /* the new mount starts clean */
	CHECK_STR(last_state[1], "ok");
}

TEST(applied_state_survives_restart)
{
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\n", MP); reconcile();
	napp = 0; load_applied();                          /* simulate agent restart */
	CHECK(napp == 1); CHECK(find_app(want[0].mp) != NULL);
	clear_calls(); reconcile();
	CHECK(calls_total() == 0);                         /* no remount after restart */
}

TEST(unmounted_but_recorded_is_remounted)
{
	char a[400]; snprintf(a, sizeof a, "%s/a", MP);
	desire("mount\tlumpy:/a\t%s\tnfs\trw\n", a); reconcile();
	sh(": > %s/mountinfo", FAKE);                      /* host rebooted / someone unmounted it */
	clear_calls(); reconcile();
	CHECK(calls("umount") == 0 && calls("mount -t nfs") == 1);
	CHECK(mounted_at(a));
}

TEST(commands_run_through_sudo_when_configured)
{
	C.use_sudo = 1;
	desire("mount\tlumpy:/a\t%s/a\tnfs\trw\n", MP); reconcile();
	CHECK(calls("sudo -n mount -t nfs") == 1);
	CHECK(calls("sudo -n mkdir -p") == 1);
	CHECK_STR(last_state[0], "ok");
}

TEST(stacked_mounts_last_wins)
{
	char a[400], s[512], f[32]; snprintf(a, sizeof a, "%s/a", MP);
	preload_mount(a, "nfs4", "first:/x"); preload_mount(a, "wekafs", "second/y");
	CHECK(is_mounted(a, s, sizeof s, f, sizeof f));
	CHECK_STR(s, "second/y"); CHECK_STR(f, "wekafs");
}

TEST(json_escape_handles_quotes_and_newlines)
{
	char *e = json_escape("a\"b\\c\nd");
	CHECK_STR(e, "a\\\"b\\\\c\\nd"); free(e);
}

TEST(server_trust_selection)
{
	char ext[400], builtin[400], out[400], *pem;
	snprintf(ext, sizeof ext, "%s/corp-ca.pem", T); snprintf(builtin, sizeof builtin, "%s/state/ca.crt", T);
	snprintf(path_ca, sizeof path_ca, "%s", builtin); snprintf(path_trust, sizeof path_trust, "%s/state/trust.pem", T);
	sh("printf 'CORP-CA\\n' > %s; printf 'BUILTIN-CA\\n' > %s", ext, builtin);
	C.server_ca[0] = 0;
	CHECK_STR(cainfo(), builtin);                               /* default: only the built-in CA */
	CHECK(build_trust() == 0);                                  /* and nothing is generated */
	snprintf(C.server_ca, sizeof C.server_ca, "%s", ext);
	sh("rm -f %s", path_trust);
	CHECK_STR(cainfo(), path_trust);                            /* external CA configured: use the combined file ... */
	CHECK(access(path_trust, R_OK) == 0);                       /* ... built on demand, so a brand-new host can enrol */
	CHECK(build_trust() == 0);
	snprintf(out, sizeof out, "%s", path_trust); pem = readfile(out);
	CHECK(pem && strstr(pem, "CORP-CA") && strstr(pem, "BUILTIN-CA"));   /* trusts both: safe in either migration order */
	free(pem);
	sh("rm -f %s", builtin);                                    /* a new host that never fetched the built-in CA */
	CHECK(build_trust() == 0);
	pem = readfile(out); CHECK(pem && strstr(pem, "CORP-CA") && !strstr(pem, "BUILTIN-CA")); free(pem);
	snprintf(C.server_ca, sizeof C.server_ca, "%s/missing.pem", T);
	CHECK(build_trust() < 0);                                   /* unreadable bundle is reported, not ignored */
}

TEST(config_parsing)
{
	char p[400]; FILE *f;
	snprintf(p, sizeof p, "%s/agent.conf", T); f = fopen(p, "w");
	fputs("# comment\nserver=https://x:8443\ninterval=60\nuse_sudo=1\nallowed_mount_prefixes=/a,/b\nallowed_mount_exact=/c\nallowed_fstypes=nfs,wekafs\nserver_ca_file=/etc/pki/corp-ca.pem\n", f); fclose(f);
	CHECK(load_cfg(p) == 0);
	CHECK_STR(C.server, "https://x:8443"); CHECK(C.interval == 60); CHECK(C.use_sudo == 1);
	CHECK(allowed_path("/a/x") && allowed_path("/c") && !allowed_path("/d"));
	CHECK(allowed_fstype("wekafs") && !allowed_fstype("nfs4"));
	CHECK_STR(C.server_ca, "/etc/pki/corp-ca.pem");
	CHECK(load_cfg("/nonexistent/agent.conf") < 0);
}

int main(void)
{
	setenv("MM_ORIG_PATH", getenv("PATH") ? getenv("PATH") : "/usr/bin:/bin", 1);
	printf("mmd mount logic tests\n");
	RUN(parses_desired_state);
	RUN(fresh_mount_then_idempotent);
	RUN(option_change_unmounts_then_remounts);
	RUN(busy_umount_is_pending_and_retried);
	RUN(mount_failure_reports_error_and_honours_retry_interval);
	RUN(mount_timeout_kills_hung_mount);
	RUN(mount_removed_from_config_is_unmounted);
	RUN(busy_removal_keeps_tracking_for_next_cycle);
	RUN(removals_happen_before_new_mounts);
	RUN(nested_removals_unmount_deepest_first);
	RUN(never_unmounts_mounts_it_did_not_create);
	RUN(adopts_existing_nfs_mount_with_same_source);
	RUN(remounts_existing_nfs_mount_with_different_source);
	RUN(adopts_existing_wekafs_mount_whatever_the_source_form);
	RUN(wekafs_mount_uses_wekafs_type);
	RUN(disallowed_fstype_is_refused);
	RUN(mountpoint_allow_list);
	RUN(sqpc_default_paths);
	RUN(exact_mountpoint_can_be_mounted);
	RUN(runtime_state_follows_the_mountpoint_not_the_slot);
	RUN(applied_state_survives_restart);
	RUN(unmounted_but_recorded_is_remounted);
	RUN(commands_run_through_sudo_when_configured);
	RUN(stacked_mounts_last_wins);
	RUN(json_escape_handles_quotes_and_newlines);
	RUN(server_trust_selection);
	RUN(config_parsing);
	printf("%d checks, %d failures\n", checks, failures);
	return failures ? 1 : 0;
}
