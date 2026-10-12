/* Setuid helper: open utun, ifconfig, split default, pass fd via SCM_RIGHTS.
   fork/exec /sbin/ifconfig and /sbin/route. system() from setuid drops euid. */
#include "utun.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stddef.h>
#include <unistd.h>
#include <errno.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/sysctl.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <sys/stat.h>
#include <dirent.h>
#include <fcntl.h>
#include <libproc.h>

/* runv: fork/execv `path`; keep euid 0. ignore non-zero if `ignore`. */
static int runv(int ignore, const char *path, char *const argv[]) {
    pid_t pid;
    int st;
    pid = fork();
    if (pid < 0) {
        perror("fork");
        return -1;
    }
    if (pid == 0) {
        if (geteuid() != 0) (void)setuid(0);
        execv(path, argv);
        _exit(127);
    }
    if (waitpid(pid, &st, 0) < 0) return -1;
    if (WIFEXITED(st) && WEXITSTATUS(st) == 0) return 0;
    if (!ignore) {
        fprintf(stderr, "loft-priv: %s -> %d\n", path, WIFEXITED(st) ? WEXITSTATUS(st) : -1);
        return -1;
    }
    return 0;
}

#define TUN_LOCAL "169.254.8.1"

/* ifconfig_tun: p2p IPv4 on utun. NEPacketTunnel / SSTP Connect style is
   overlay IP as both ends (A --> A) so the kernel sources from that address. */
static int ifconfig_tun(const char *name, const char *local, const char *peer, const char *mtu) {
    char *argv[] = {
        "ifconfig", (char *)name, "inet", (char *)local, (char *)peer,
        "netmask", "255.255.255.255", "mtu", (char *)mtu, "up", NULL
    };
    return runv(0, "/sbin/ifconfig", argv);
}

/* IPv4-only until overlay IPv6 is assigned. Do not install ::/0. */
static int ifconfig_no_inet6(const char *name) {
    char *argv[] = { "ifconfig", (char *)name, "inet6", "ifdisabled", NULL };
    return runv(1, "/sbin/ifconfig", argv);
}

/* lo_alias: drop a leftover overlay IP on lo0 (old dummy-local layout). */
static int lo_alias(int add, const char *ip) {
    if (add) {
        char *argv[] = {
            "ifconfig", "lo0", "alias", (char *)ip, "netmask", "255.255.255.255", NULL
        };
        return runv(0, "/sbin/ifconfig", argv);
    } else {
        char *argv[] = { "ifconfig", "lo0", "inet", (char *)ip, "-alias", NULL };
        return runv(1, "/sbin/ifconfig", argv);
    }
}

/* route_del_net: `route -n delete -inet net`. */
static int route_del_net(const char *net) {
    char *argv[] = { "route", "-n", "delete", "-inet", (char *)net, NULL };
    return runv(1, "/sbin/route", argv);
}

/* drop_split_default: remove 0/1 and 128/1 installed by loft. */
static void drop_split_default(void) {
    route_del_net("0.0.0.0/1");
    route_del_net("0/1");
    route_del_net("128.0.0.0/1");
    route_del_net("128.0/1");
}

/* route_del_host: `route -n delete -host ip`. */
static int route_del_host(const char *ip) {
    char *argv[] = { "route", "-n", "delete", "-host", (char *)ip, NULL };
    return runv(1, "/sbin/route", argv);
}

/* route_add_host: pin `ip` via pre-loft `gw` (front/DNS exclude). */
static int route_add_host(const char *ip, const char *gw) {
    char *argv[] = { "route", "-n", "add", "-host", (char *)ip, (char *)gw, NULL };
    return runv(1, "/sbin/route", argv);
}



/* cidr24: `a.b.c.0/24` of `ip`. */
static void cidr24(const char *ip, char *out, size_t n) {
    unsigned a, b, c, d;
    if (sscanf(ip, "%u.%u.%u.%u", &a, &b, &c, &d) == 4)
        snprintf(out, n, "%u.%u.%u.0/24", a, b, c);
    else
        snprintf(out, n, "%s", ip);
}

/* route_add_split: add `net` via utun `-interface`, else via `peer`. */
static int route_add_split(const char *net, const char *name, const char *peer) {
    char *via_if[] = {
        "route", "-n", "add", "-inet", (char *)net, "-interface", (char *)name, NULL
    };
    if (runv(1, "/sbin/route", via_if) == 0) return 0;
    char *via_gw[] = {
        "route", "-n", "add", "-inet", (char *)net, (char *)peer, NULL
    };
    return runv(0, "/sbin/route", via_gw);
}

/* route_blackhole: hold default via 127.0.0.1 -blackhole. */
static int route_blackhole(void) {
    char *argv[] = {
        "route", "-n", "add", "-inet", "0.0.0.0/0", "127.0.0.1", "-blackhole", NULL
    };
    return runv(1, "/sbin/route", argv);
}

/* route_unhold: delete only the 127.0.0.1 blackhole, not a real 0/0. */
static int route_unhold(void) {
    /* Only the blackhole we installed. A bare delete of 0/0 can drop the real default. */
    char *argv[] = {
        "route", "-n", "delete", "-inet", "0.0.0.0/0", "127.0.0.1", NULL
    };
    return runv(1, "/sbin/route", argv);
}

static char hold_stamp[64];

/* stamp_hold: /tmp/loft-hold.pid so --reap can find this holder. */
static void stamp_hold(void) {
    snprintf(hold_stamp, sizeof hold_stamp, "/tmp/loft-hold.%d", (int)getpid());
    int fd = open(hold_stamp, O_CREAT | O_WRONLY | O_TRUNC, 0644);
    if (fd >= 0) close(fd);
}

/* unstamp_hold: unlink this process's hold stamp. */
static void unstamp_hold(void) {
    if (hold_stamp[0]) unlink(hold_stamp);
    hold_stamp[0] = 0;
}

/* comm_is_priv: true if pid's executable basename is loft-priv. */
static int comm_is_priv(pid_t pid) {
    char path[PROC_PIDPATHINFO_MAXSIZE];
    const char *base;
    if (proc_pidpath(pid, path, sizeof path) <= 0) return 0;
    base = strrchr(path, '/');
    base = base ? base + 1 : path;
    return strcmp(base, "loft-priv") == 0;
}

/* Holders are started with --sock. --down/--reap/--kill are not. */
/* argv_has_sock: true if pid was started with --sock (a holder). */
static int argv_has_sock(pid_t pid) {
    int mib[3] = { CTL_KERN, KERN_PROCARGS2, (int)pid };
    char buf[8192];
    size_t len = sizeof buf;
    char *p, *end;
    if (sysctl(mib, 3, buf, &len, NULL, 0) < 0 || len < sizeof(int) + 4) return 0;
    p = buf + sizeof(int);
    end = buf + len;
    while (p < end && *p) p++;
    if (p < end) p++;
    while (p < end) {
        if (!*p) { p++; continue; }
        if (strcmp(p, "--sock") == 0) return 1;
        p += strlen(p) + 1;
    }
    return 0;
}

/* kill_holder: signal another loft-priv; skip pid 1 and self. */
static void kill_holder(pid_t pid, int sig) {
    if (pid <= 1 || pid == getpid()) return;
    if (!comm_is_priv(pid)) return;
    if (kill(pid, sig) == 0)
        fprintf(stderr, "loft-priv: reap %d\n", (int)pid);
}

/* Holders run as ruid 0; the GUI cannot signal them. */
/* reap_others: SIGTERM leftover holders (hold stamps and --sock argv). */
static void reap_others(int sig) {
    DIR *d;
    struct dirent *e;
    pid_t me = getpid();
    int bytes, n, i;
    pid_t *pids;
    d = opendir("/tmp");
    if (d) {
        while ((e = readdir(d))) {
            int pid = 0;
            if (sscanf(e->d_name, "loft-hold.%d", &pid) != 1) continue;
            if ((pid_t)pid == me) continue;
            kill_holder((pid_t)pid, sig);
            {
                char path[96];
                snprintf(path, sizeof path, "/tmp/%s", e->d_name);
                unlink(path);
            }
        }
        closedir(d);
    }
    bytes = proc_listallpids(NULL, 0);
    if (bytes <= 0) return;
    n = bytes / (int)sizeof(pid_t) + 64;
    pids = calloc((size_t)n, sizeof(pid_t));
    if (!pids) return;
    bytes = proc_listallpids(pids, n * (int)sizeof(pid_t));
    if (bytes <= 0) {
        free(pids);
        return;
    }
    n = bytes / (int)sizeof(pid_t);
    for (i = 0; i < n; i++) {
        pid_t pid = pids[i];
        if (pid <= 1 || pid == me) continue;
        if (!argv_has_sock(pid)) continue;
        kill_holder(pid, sig);
    }
    free(pids);
}

/* After dropping 0/1, Darwin may still have default via utun (or none).
   Put 0/0 back on the pre-loft gateway. */
#define DNS_SAVE "/var/run/loft-dns.prev"

/* dns_capture: run `path` and copy stdout into `out`. */
static int dns_capture(const char *path, char *const argv[], char *out, size_t n) {
    int p[2];
    pid_t pid;
    size_t off = 0;
    int st;
    if (pipe(p) < 0) return -1;
    pid = fork();
    if (pid < 0) {
        close(p[0]);
        close(p[1]);
        return -1;
    }
    if (pid == 0) {
        if (geteuid() != 0) (void)setuid(0);
        dup2(p[1], 1);
        close(p[0]);
        close(p[1]);
        execv(path, argv);
        _exit(127);
    }
    close(p[1]);
    while (off + 1 < n) {
        ssize_t k = read(p[0], out + off, n - 1 - off);
        if (k <= 0) break;
        off += (size_t)k;
    }
    out[off] = 0;
    close(p[0]);
    if (waitpid(pid, &st, 0) < 0) return -1;
    return (WIFEXITED(st) && WEXITSTATUS(st) == 0) ? 0 : -1;
}

/* dns_set_svc: networksetup -setdnsservers for one service. */
static int dns_set_svc(const char *svc, char *const ips[]) {
    char *argv[16];
    int n = 0;
    argv[n++] = "networksetup";
    argv[n++] = "-setdnsservers";
    argv[n++] = (char *)svc;
    if (ips && ips[0]) {
        int i;
        for (i = 0; ips[i] && n < 15; i++) argv[n++] = ips[i];
    } else {
        argv[n++] = "Empty";
    }
    argv[n] = NULL;
    return runv(1, "/usr/sbin/networksetup", argv);
}

/* dns_restore: replay DNS_SAVE and unlink it. Unused on the live overlay-DNS-off path. */
static void dns_restore(void) {
    FILE *fp;
    char line[512];
    fp = fopen(DNS_SAVE, "r");
    if (!fp) return;
    while (fgets(line, sizeof line, fp)) {
        char *bar, *svc, *rest;
        char *ips[9];
        int n = 0;
        line[strcspn(line, "\r\n")] = 0;
        bar = strchr(line, '|');
        if (!bar) continue;
        *bar = 0;
        svc = line;
        rest = bar + 1;
        if (!svc[0]) continue;
        if (!rest[0] || !strcmp(rest, "Empty")) {
            dns_set_svc(svc, NULL);
            continue;
        }
        ips[n] = strtok(rest, " ");
        while (ips[n] && n < 8) {
            n++;
            ips[n] = strtok(NULL, " ");
        }
        dns_set_svc(svc, ips);
    }
    fclose(fp);
    unlink(DNS_SAVE);
}

/* dns_apply: push csv nameservers onto every Network Service; save previous. */
static int dns_apply(const char *csv) {
    char list[4096];
    char *argv[] = {"networksetup", "-listallnetworkservices", NULL};
    char *ips[9];
    char *dup, *tok;
    int nips = 0;
    char *line, *nl;
    FILE *fp;
    if (!csv || !csv[0]) return 0;
    dup = strdup(csv);
    if (!dup) return -1;
    tok = strtok(dup, ",");
    while (tok && nips < 8) {
        ips[nips++] = tok;
        tok = strtok(NULL, ",");
    }
    ips[nips] = NULL;
    if (nips == 0) {
        free(dup);
        return 0;
    }
    if (dns_capture("/usr/sbin/networksetup", argv, list, sizeof list) != 0) {
        free(dup);
        return -1;
    }
    fp = fopen(DNS_SAVE, "w");
    line = list;
    /* skip header */
    nl = strchr(line, '\n');
    if (nl) line = nl + 1;
    while (line && line[0]) {
        char svc[256];
        char getbuf[1024];
        char *getargv[4];
        size_t sl;
        nl = strchr(line, '\n');
        if (nl) *nl = 0;
        if (line[0] != '*' && line[0] != 0 && strncmp(line, "An asterisk", 11) != 0) {
            snprintf(svc, sizeof svc, "%s", line);
            sl = strlen(svc);
            if (sl && svc[sl - 1] == '\r') svc[sl - 1] = 0;
            getargv[0] = "networksetup";
            getargv[1] = "-getdnsservers";
            getargv[2] = svc;
            getargv[3] = NULL;
            if (fp) {
                if (dns_capture("/usr/sbin/networksetup", getargv, getbuf, sizeof getbuf) != 0 || strstr(getbuf, "aren't any"))
                    fprintf(fp, "%s|Empty\n", svc);
                else {
                    char *p;
                    for (p = getbuf; *p; p++) if (*p == '\n' || *p == '\r') *p = ' ';
                    fprintf(fp, "%s|%s\n", svc, getbuf);
                }
            }
            dns_set_svc(svc, ips);
            fprintf(stderr, "loft-priv: dns %s -> %s\n", svc, csv);
        }
        line = nl ? nl + 1 : NULL;
    }
    if (fp) fclose(fp);
    free(dup);
    return 0;
}

#define DID_DEFAULT_STAMP "/tmp/loft-did-default"

/* stamp_default: /tmp/loft-did-default so --down knows to restore 0/0. */
static void stamp_default(void) {
    int fd = open(DID_DEFAULT_STAMP, O_CREAT | O_WRONLY | O_TRUNC, 0644);
    if (fd >= 0) close(fd);
}

/* unstamp_default: unlink the split-default stamp. */
static void unstamp_default(void) {
    unlink(DID_DEFAULT_STAMP);
}

/* stamped_default: true if loft installed the 0/1 split. */
static int stamped_default(void) {
    return access(DID_DEFAULT_STAMP, F_OK) == 0;
}

/* route_restore_default: change+add 0/0 via pre-loft gw. Skip dummy/link# gw. */
static int route_restore_default(const char *gw) {
    char *chg[] = {
        "route", "-n", "change", "-inet", "default", (char *)gw, NULL
    };
    char *add[] = {
        "route", "-n", "add", "-inet", "default", (char *)gw, NULL
    };
    if (!gw || !gw[0]) return 0;
    if (!strcmp(gw, TUN_LOCAL) || !strncmp(gw, "link#", 5)) return 0;
    /* Darwin `route change` often prints "not in table" and still exits 0.
       Always try add: File exists is fine, missing default is not. */
    runv(1, "/sbin/route", chg);
    if (runv(1, "/sbin/route", add) == 0)
        fprintf(stderr, "loft-priv: default add %s\n", gw);
    else
        fprintf(stderr, "loft-priv: default via %s\n", gw);
    return 0;
}

/* send_fd: connect to unix sock and pass the utun fd via SCM_RIGHTS. */
static int send_fd(const char *path, int tun, const char *name) {
    int fd;
    struct sockaddr_un un;
    struct msghdr msg;
    struct iovec iov;
    char cbuf[CMSG_SPACE(sizeof(int))];
    struct cmsghdr *c;
    char nbuf[16];
    socklen_t slen;
    fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) return -1;
    memset(&un, 0, sizeof un);
    un.sun_family = AF_UNIX;
    strncpy(un.sun_path, path, sizeof(un.sun_path) - 1);
    un.sun_len = (unsigned char)(offsetof(struct sockaddr_un, sun_path) + strlen(un.sun_path) + 1);
    slen = un.sun_len;
    if (connect(fd, (struct sockaddr *)&un, slen) < 0) {
        fprintf(stderr, "loft-priv: connect %s: %s\n", path, strerror(errno));
        close(fd);
        return -1;
    }
    memset(&msg, 0, sizeof msg);
    memset(nbuf, 0, sizeof nbuf);
    strncpy(nbuf, name, 15);
    iov.iov_base = nbuf;
    iov.iov_len = sizeof nbuf;
    msg.msg_iov = &iov;
    msg.msg_iovlen = 1;
    msg.msg_control = cbuf;
    msg.msg_controllen = sizeof cbuf;
    c = CMSG_FIRSTHDR(&msg);
    c->cmsg_level = SOL_SOCKET;
    c->cmsg_type = SCM_RIGHTS;
    c->cmsg_len = CMSG_LEN(sizeof(int));
    memcpy(CMSG_DATA(c), &tun, sizeof tun);
    if (sendmsg(fd, &msg, 0) < 0) {
        fprintf(stderr, "loft-priv: sendmsg: %s\n", strerror(errno));
        close(fd);
        return -1;
    }
    close(fd);
    return 0;
}

/* each_ip: call `fn` for each comma-separated exclude IP. */
static void each_ip(const char *exclude, void (*fn)(const char *)) {
    char *dup, *tok;
    if (!exclude || !exclude[0]) return;
    dup = strdup(exclude);
    if (!dup) return;
    tok = strtok(dup, ",");
    while (tok) {
        fn(tok);
        tok = strtok(NULL, ",");
    }
    free(dup);
}

static const char *g_gw;

static char g_ip[64], g_peer[64], g_gwbuf[64];
static int g_tun = -1, g_did_default;
static volatile sig_atomic_t stop_hold;

/* on_hold_sig: SIGTERM/INT/HUP while holding utun. */
static void on_hold_sig(int sig) {
    (void)sig;
    stop_hold = 1;
}

/* hold_cleanup: drop overlay routes, restore 0/0 if we installed it, close utun. */
static void hold_cleanup(void) {
    char net[32];
    drop_split_default();
    if (g_ip[0]) {
        cidr24(g_ip, net, sizeof net);
        route_del_net(net);
        route_del_host(g_ip);
        lo_alias(0, g_ip);
    }
    if (g_peer[0]) route_del_host(g_peer);
    route_del_host(TUN_LOCAL);
    /* --set-default is a one-shot and does not set g_did_default.
       Only rewrite 0/0 if loft actually installed the split default. */
    if (g_did_default || stamped_default()) {
        if (g_gwbuf[0]) route_restore_default(g_gwbuf);
        unstamp_default();
    }
    dns_restore();
    if (g_tun >= 0) {
        loft_utun_close(g_tun);
        g_tun = -1;
    }
    unstamp_hold();
}

/* add_excl: pin `ip` on the pre-loft gateway. */
static void add_excl(const char *ip) {
    if (g_gw && g_gw[0]) route_add_host(ip, g_gw);
}

/* del_excl: drop a host exclude route. */
static void del_excl(const char *ip) { route_del_host(ip); }

/* main: --sock hold, --down/--restore/--hold, --set-default/--clear-default, --kill/--reap. */
int main(int argc, char **argv) {
    const char *sock = NULL, *ip = NULL, *peer = NULL, *mtu = "1280", *gw = NULL, *exclude = NULL, *ifname = NULL, *dns = NULL;
    int i, tun, drop_bh = 0, down = 0, hold = 0, want_default = 0, set_default = 0, clear_default = 0;
    int do_kill = 0, force = 0, reap = 0, restore = 0, overlay_only = 0;
    pid_t kill_pid = 0;
    char name[16];
    for (i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--sock") && i + 1 < argc) sock = argv[++i];
        else if (!strcmp(argv[i], "--ip") && i + 1 < argc) ip = argv[++i];
        else if (!strcmp(argv[i], "--peer") && i + 1 < argc) peer = argv[++i];
        else if (!strcmp(argv[i], "--mtu") && i + 1 < argc) mtu = argv[++i];
        else if (!strcmp(argv[i], "--gw") && i + 1 < argc) gw = argv[++i];
        else if (!strcmp(argv[i], "--exclude") && i + 1 < argc) exclude = argv[++i];
        else if (!strcmp(argv[i], "--dns") && i + 1 < argc) dns = argv[++i];
        else if (!strcmp(argv[i], "--name") && i + 1 < argc) ifname = argv[++i];
        else if (!strcmp(argv[i], "--clear-blackhole")) drop_bh = 1;
        else if (!strcmp(argv[i], "--down")) down = 1;
        else if (!strcmp(argv[i], "--overlay")) overlay_only = 1;
        else if (!strcmp(argv[i], "--restore")) restore = 1;
        else if (!strcmp(argv[i], "--reap")) reap = 1;
        else if (!strcmp(argv[i], "--kill") && i + 1 < argc) {
            do_kill = 1;
            kill_pid = (pid_t)atoi(argv[++i]);
        }
        else if (!strcmp(argv[i], "--force")) force = 1;
        else if (!strcmp(argv[i], "--hold")) hold = 1;
        else if (!strcmp(argv[i], "--default")) want_default = 1;
        else if (!strcmp(argv[i], "--set-default")) set_default = 1;
        else if (!strcmp(argv[i], "--clear-default")) clear_default = 1;
    }
    if (geteuid() != 0) {
        fprintf(stderr, "loft-priv: need root\n");
        return 1;
    }
    if (do_kill) {
        if (kill_pid > 1) {
            kill(kill_pid, force ? SIGKILL : SIGTERM);
            fprintf(stderr, "loft-priv: kill %d %s\n", (int)kill_pid, force ? "KILL" : "TERM");
        }
        return 0;
    }
    if (reap && !down && !restore && !clear_default && !drop_bh) {
        reap_others(SIGTERM);
        return 0;
    }
    if (down || restore) {
        int touch_default = !overlay_only && (restore || hold || stamped_default());
        if (touch_default) {
            drop_split_default();
            route_unhold();
        }
        if (ip) {
            char net[32];
            cidr24(ip, net, sizeof net);
            route_del_net(net);
            route_del_host(ip);
            lo_alias(0, ip);
        }
        if (peer) route_del_host(peer);
        route_del_host(TUN_LOCAL);
        each_ip(exclude, del_excl);
        dns_restore();
        if (hold) route_blackhole();
        else if (touch_default) {
            route_restore_default(gw);
            unstamp_default();
        }
        fprintf(stderr, "loft-priv: %s gw=%s overlay=%d\n",
                hold ? "hold" : (touch_default ? "restore" : "overlay"),
                gw ? gw : "", overlay_only);
        fflush(stderr);
        /* Reap in a child so this --down can exit even if a holder is slow. */
        {
            pid_t k = fork();
            if (k == 0) {
                usleep(150000);
                reap_others(SIGTERM);
                _exit(0);
            }
        }
        return 0;
    }
    if (drop_bh && !sock) {
        route_unhold();
        route_restore_default(gw);
        drop_split_default();
        fprintf(stderr, "loft-priv: unhold gw=%s\n", gw ? gw : "");
        return 0;
    }
    if (clear_default) {
        drop_split_default();
        route_restore_default(gw);
        each_ip(exclude, del_excl);
        unstamp_default();
        fprintf(stderr, "loft-priv: default off\n");
        return 0;
    }
    if (set_default) {
        if (!ifname) {
            fprintf(stderr, "loft-priv: --set-default needs --name\n");
            return 2;
        }
        g_gw = gw;
        each_ip(exclude, add_excl);
        drop_split_default();
        if (route_add_split("0.0.0.0/1", ifname, ifname) != 0) return 1;
        if (route_add_split("128.0.0.0/1", ifname, ifname) != 0) return 1;
        stamp_default();
        fprintf(stderr, "loft-priv: default via %s\n", ifname);
        return 0;
    }
    if (!sock || !ip || !peer) {
        fprintf(stderr, "usage: loft-priv --sock PATH --ip A --peer B [--mtu N] [--gw G] [--exclude ip,ip] [--default]\n");
        return 2;
    }
    tun = loft_utun_open(name);
    if (tun < 0) {
        perror("utun");
        return 1;
    }
    fprintf(stderr, "loft-priv: open %s overlay=%s peer=%s local=%s\n", name, ip, peer, ip);
    if (drop_bh) route_unhold();
    /* A --> A like SSTP Connect's utun. Kernel sources ICMP from the overlay
       IP, so the PPP/LFTP gateway can reply. Drop a leftover lo0 alias from
       the old dummy-local layout; do not add a new one (duplicate of utun). */
    (void)lo_alias(0, ip);
    if (ifconfig_tun(name, ip, ip, mtu) != 0) {
        loft_utun_close(tun);
        return 1;
    }
    (void)ifconfig_no_inet6(name);
    {
        char net[32];
        cidr24(ip, net, sizeof net);
        route_del_net(net);
        if (route_add_split(net, name, peer) == 0)
            fprintf(stderr, "loft-priv: overlay %s via %s\n", net, name);
        else
            fprintf(stderr, "loft-priv: overlay %s failed\n", net);
    }
    if (want_default) {
        /* IPv4 0/1 split only. Overlay IPv6 later may add inet6 default. */
        g_gw = gw;
        each_ip(exclude, add_excl);
        drop_split_default();
        if (route_add_split("0.0.0.0/1", name, peer) != 0) {
            loft_utun_close(tun);
            return 1;
        }
        if (route_add_split("128.0.0.0/1", name, peer) != 0) {
            loft_utun_close(tun);
            return 1;
        }
        stamp_default();
        fprintf(stderr, "loft-priv: default via %s (%s)\n", name, peer);
    } else {
        fprintf(stderr, "loft-priv: overlay only\n");
    }
    if (dns && dns[0]) dns_apply(dns);
    if (send_fd(sock, tun, name) != 0) {
        if (want_default) drop_split_default();
        loft_utun_close(tun);
        return 1;
    }
    /* Keep the original utun fd until SIGTERM. Darwin utun stays up
       while any fd remains (loft-front's SCM_RIGHTS copy). On signal,
       drop lo0 alias and overlay routes — SIGTERM otherwise never
       reaches the lines after pause(). */
    snprintf(g_ip, sizeof g_ip, "%s", ip);
    snprintf(g_peer, sizeof g_peer, "%s", peer);
    if (gw && gw[0]) snprintf(g_gwbuf, sizeof g_gwbuf, "%s", gw);
    g_did_default = want_default;
    g_tun = tun;
    {
        struct sigaction sa;
        memset(&sa, 0, sizeof sa);
        sa.sa_handler = on_hold_sig;
        sigemptyset(&sa.sa_mask);
        sigaction(SIGTERM, &sa, NULL);
        sigaction(SIGINT, &sa, NULL);
        sigaction(SIGHUP, &sa, NULL);
    }
    fprintf(stderr, "loft-priv: hold %s fd=%d\n", name, tun);
    fflush(stderr);
    stamp_hold();
    while (!stop_hold) pause();
    hold_cleanup();
    fprintf(stderr, "loft-priv: down %s\n", name);
    return 0;
}
