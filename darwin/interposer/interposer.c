// libmproxy_interposer.dylib — DYLD interposer dylib that reroutes exec
// family calls through mproxy-shim on macOS.
//
// machineproxy injects this dylib into the target process via
// DYLD_INSERT_LIBRARIES (set by cmd/machineproxy/runchild_darwin.go).
// When the target calls execve, execv, execvp, posix_spawn or
// posix_spawnp, our replacement reads MPROXY_WHITELIST and MPROXY_SHIM_PATH
// from the environment, decides whether the target path should run
// locally (whitelisted) or remotely, and on the remote path rewrites
// argv to [shim, original_path, original_argv...] before re-invoking the
// real syscall with shim as the executable.
//
// Whitelist semantics implemented here mirror pkg/config.MatchLocalCommand
// for the two common cases:
//   - "/abs/path"   — exact absolute path match
//   - "name"        — basename match against the last path segment
// Regex entries ("/pattern/" with delimiters) are *not* honored by the
// dylib; they always fall through to remote exec. This is a deliberate
// scope reduction in the C implementation.
//
// When MPROXY_CWD_FROM / MPROXY_CWD_TO are set (container.cwd_remap=remote),
// the dylib also rewrites the leading CWD_FROM path prefix to CWD_TO in
// getcwd(3) results and in the $PWD env var handed to exec'd children,
// preserving any sub-path. This mirrors the Linux ptrace tracer's getcwd /
// $PWD remapping.
//
// Built per-arch (arm64 + amd64) by darwin/interposer/build.sh.

#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <spawn.h>
#include <sys/types.h>
#include <crt_externs.h>

// macOS DYLD interpose table entry layout. See `man 1 dyld` (search
// "interposing") and Apple's open-source dyld for the canonical pattern.
// The __DATA,__interpose section name is fixed by Apple and stays.
typedef struct interposer_entry {
    const void *replacement;
    const void *original;
} interposer_entry_t;

#define MPROXY_INTERPOSER(replacement, original)                               \
    __attribute__((used, section("__DATA,__interpose")))                       \
    static const interposer_entry_t interposer_##original = {                  \
        (const void *)(unsigned long)&replacement,                             \
        (const void *)(unsigned long)&original,                                \
    }

// is_whitelisted scans the colon-separated $MPROXY_WHITELIST list. Each
// entry is matched against path either as an exact absolute path (entry
// starts with '/') or as a basename (entry has no '/'). Entries with
// embedded slashes or regex-delimiter slashes are ignored — a regex
// entry like "/foo/" looks indistinguishable from an absolute path here
// and would mismatch surprisingly, so we skip both forms rather than
// guess. The matcher returns 1 on match, 0 otherwise.
static int is_whitelisted(const char *path) {
    if (!path || !*path) {
        return 0;
    }
    const char *list = getenv("MPROXY_WHITELIST");
    if (!list || !*list) {
        return 0;
    }

    // Compute basename of path: last segment after '/'.
    const char *base = strrchr(path, '/');
    base = base ? base + 1 : path;
    size_t path_len = strlen(path);
    size_t base_len = strlen(base);

    const char *p = list;
    while (*p) {
        const char *colon = strchr(p, ':');
        size_t len = colon ? (size_t)(colon - p) : strlen(p);

        if (len > 0) {
            int has_slash = 0;
            for (size_t i = 0; i < len; i++) {
                if (p[i] == '/') {
                    has_slash = 1;
                    break;
                }
            }

            if (has_slash) {
                // Treat as absolute-path entry only when it starts with
                // '/' AND does not also end with '/' (regex form). The
                // exact-match comparison is cheap and avoids confusion.
                if (p[0] == '/' && (len < 2 || p[len - 1] != '/')) {
                    if (path_len == len && strncmp(path, p, len) == 0) {
                        return 1;
                    }
                }
                // else: regex form, intentionally not honored here.
            } else {
                // Basename match.
                if (base_len == len && strncmp(base, p, len) == 0) {
                    return 1;
                }
            }
        }

        if (!colon) {
            break;
        }
        p = colon + 1;
    }
    return 0;
}

// argv_count returns the length of a null-terminated char* array.
static size_t argv_count(char *const argv[]) {
    size_t n = 0;
    if (argv) {
        while (argv[n]) {
            n++;
        }
    }
    return n;
}

// strip_dyld_on_whitelist returns non-zero when the operator has opted
// in to stripping DYLD_INSERT_LIBRARIES from the env passed to
// whitelisted commands. Useful when a whitelisted command (e.g. make,
// bash) spawns lots of local-only subcommands and the operator wants
// those subtrees not to inherit the interposer.
//
// Caveats:
//   - For execv / execvp the caller did not supply envp; we mutate the
//     process environ via unsetenv before invoking the real syscall.
//     If the real syscall fails, DYLD_INSERT_LIBRARIES stays unset for
//     the rest of this process — acceptable for an explicit opt-in.
//   - This does not affect the non-whitelist path; descendants of a
//     remote-routed exec still get the dylib via the unchanged envp.
static int strip_dyld_on_whitelist(void) {
    const char *v = getenv("MPROXY_INTERPOSER_STRIP_DYLD_ON_WHITELIST");
    if (!v || !*v) {
        return 0;
    }
    return v[0] == '1' || v[0] == 't' || v[0] == 'T' ||
           v[0] == 'y' || v[0] == 'Y';
}

// ----- cwd / $PWD remapping ------------------------------------------------

// cwd_from / cwd_to return the configured remap prefixes, or NULL when
// container.cwd_remap is not "remote" (the env vars are unset/empty).
static const char *cwd_from(void) {
    const char *v = getenv("MPROXY_CWD_FROM");
    return (v && *v) ? v : NULL;
}

static const char *cwd_to(void) {
    const char *v = getenv("MPROXY_CWD_TO");
    return (v && *v) ? v : NULL;
}

// rewrite_prefix returns a malloc'd copy of p with a leading `from` path
// component replaced by `to`, preserving the sub-path. It matches only on
// whole path components: p must equal `from` or start with `from + "/"`.
// Returns NULL when `from` does not prefix p (or on allocation failure).
// Mirrors pkg/config.RewritePathPrefix. Caller frees.
static char *rewrite_prefix(const char *p, const char *from, const char *to) {
    if (!p || !from || !*from || !to) {
        return NULL;
    }
    size_t flen = strlen(from);
    if (strcmp(p, from) == 0) {
        return strdup(to);
    }
    if (strncmp(p, from, flen) == 0 && p[flen] == '/') {
        size_t tlen = strlen(to);
        size_t suffix = strlen(p + flen); // sub-path including leading '/'
        char *out = (char *)malloc(tlen + suffix + 1);
        if (!out) {
            return NULL;
        }
        memcpy(out, to, tlen);
        memcpy(out + tlen, p + flen, suffix + 1); // copy sub-path incl. NUL
        return out;
    }
    return NULL;
}

// remap_pwd_value takes a "PWD=<value>" env entry and returns a malloc'd
// "PWD=<remapped>" string when <value> falls under CWD_FROM, else NULL.
static char *remap_pwd_value(const char *entry) {
    const char *from = cwd_from();
    const char *to = cwd_to();
    if (!from || !to) {
        return NULL;
    }
    char *mapped = rewrite_prefix(entry + 4 /* skip "PWD=" */, from, to);
    if (!mapped) {
        return NULL;
    }
    size_t mlen = strlen(mapped);
    char *out = (char *)malloc(4 + mlen + 1);
    if (out) {
        memcpy(out, "PWD=", 4);
        memcpy(out + 4, mapped, mlen + 1);
    }
    free(mapped);
    return out;
}

// remap_pwd_in_environ rewrites the process's own $PWD in place (via
// setenv) when it falls under CWD_FROM. Used on the whitelisted execv /
// execvp paths, which re-invoke the real call against the live environ.
static void remap_pwd_in_environ(void) {
    const char *pwd = getenv("PWD");
    const char *from = cwd_from();
    const char *to = cwd_to();
    if (!pwd || !from || !to) {
        return;
    }
    char *mapped = rewrite_prefix(pwd, from, to);
    if (!mapped) {
        return;
    }
    setenv("PWD", mapped, 1);
    free(mapped);
}

// build_exec_envp derives a new NULL-terminated envp from envp, applying
// the $PWD cwd-remap (whenever configured) and, when strip_dyld is set,
// dropping any DYLD_INSERT_LIBRARIES entry. Returns NULL when neither
// transformation changes anything — callers then use the original envp.
//
// On success the returned array is malloc'd and borrows envp's strings,
// except a rewritten PWD entry: that malloc'd string is returned via
// *owned_pwd so the caller can free it alongside the array. *owned_pwd is
// NULL when PWD was not rewritten.
static char **build_exec_envp(char *const envp[], int strip_dyld,
                              char **owned_pwd) {
    *owned_pwd = NULL;
    if (!envp) {
        return NULL;
    }
    static const char dyld_prefix[] = "DYLD_INSERT_LIBRARIES=";
    static const size_t dyld_len = sizeof(dyld_prefix) - 1;

    char *new_pwd = NULL;
    long pwd_idx = -1;
    size_t n = 0;
    for (; envp[n]; n++) {
        if (pwd_idx < 0 && strncmp(envp[n], "PWD=", 4) == 0) {
            new_pwd = remap_pwd_value(envp[n]);
            if (new_pwd) {
                pwd_idx = (long)n;
            }
        }
    }
    if (!new_pwd && !strip_dyld) {
        return NULL;
    }

    char **out = (char **)calloc(n + 1, sizeof(char *));
    if (!out) {
        free(new_pwd);
        return NULL;
    }
    size_t j = 0;
    for (size_t i = 0; i < n; i++) {
        if (strip_dyld && strncmp(envp[i], dyld_prefix, dyld_len) == 0) {
            continue;
        }
        out[j++] = ((long)i == pwd_idx) ? new_pwd : envp[i];
    }
    out[j] = NULL;
    *owned_pwd = new_pwd;
    return out;
}

// build_redirect_argv allocates a new argv of the form
//   [shim_path, original_path, argv[0], argv[1], ..., NULL]
// Caller frees with free(). Returns NULL on allocation failure.
static char **build_redirect_argv(const char *shim_path,
                                  const char *original_path,
                                  char *const argv[]) {
    size_t n = argv_count(argv);
    char **out = (char **)calloc(n + 3, sizeof(char *));
    if (!out) {
        return NULL;
    }
    out[0] = (char *)shim_path;
    out[1] = (char *)original_path;
    for (size_t i = 0; i < n; i++) {
        out[i + 2] = argv[i];
    }
    out[n + 2] = NULL;
    return out;
}

// current_environ returns the process environment. On macOS a global
// `environ` is unreliable in dylibs; _NSGetEnviron() is the supported
// way.
static char **current_environ(void) {
    char ***envp = _NSGetEnviron();
    return envp ? *envp : NULL;
}

// ----- getcwd --------------------------------------------------------------

// mproxy_getcwd calls the real getcwd, then rewrites the CWD_FROM prefix of
// the result to CWD_TO (preserving the sub-path), so the process observes
// the remote view of its working directory. A no-op unless container.cwd_remap
// is "remote" and the real cwd falls under CWD_FROM.
static char *mproxy_getcwd(char *buf, size_t size) {
    char *res = getcwd(buf, size);
    if (!res) {
        return res; // failure; errno already set by getcwd
    }
    const char *from = cwd_from();
    const char *to = cwd_to();
    if (!from || !to) {
        return res;
    }
    char *mapped = rewrite_prefix(res, from, to);
    if (!mapped) {
        return res; // cwd not under CWD_FROM
    }
    size_t need = strlen(mapped) + 1;

    if (buf == NULL) {
        // getcwd allocated `res` itself (BSD extension); grow it to hold the
        // possibly-longer remapped path and hand the new buffer back for the
        // caller to free.
        char *grown = (char *)realloc(res, need);
        if (!grown) {
            free(mapped);
            return res; // keep the original buffer on allocation failure
        }
        memcpy(grown, mapped, need);
        free(mapped);
        return grown;
    }

    if (need > size) {
        // The remapped path no longer fits the caller's buffer.
        free(mapped);
        errno = ERANGE;
        return NULL;
    }
    memcpy(buf, mapped, need);
    free(mapped);
    return buf;
}
MPROXY_INTERPOSER(mproxy_getcwd, getcwd);

// ----- execve --------------------------------------------------------------

static int mproxy_execve(const char *path,
                         char *const argv[],
                         char *const envp[]) {
    int wl = is_whitelisted(path);
    // Apply $PWD remapping (always, when configured) and DYLD stripping
    // (whitelist opt-in only) in one pass. eff_envp falls back to envp.
    char *owned_pwd = NULL;
    char **xenvp = build_exec_envp(envp, wl && strip_dyld_on_whitelist(), &owned_pwd);
    char *const *eff_envp = xenvp ? xenvp : envp;

    int ret;
    if (wl) {
        ret = execve(path, argv, eff_envp);
    } else {
        const char *shim = getenv("MPROXY_SHIM_PATH");
        char **new_argv =
            (shim && *shim) ? build_redirect_argv(shim, path, argv) : NULL;
        if (new_argv) {
            ret = execve(shim, new_argv, eff_envp);
            free(new_argv);
        } else {
            ret = execve(path, argv, eff_envp);
        }
    }
    // exec only returns on failure; free our scratch allocations.
    free(xenvp);
    free(owned_pwd);
    return ret;
}
MPROXY_INTERPOSER(mproxy_execve, execve);

// ----- execv / execvp -----------------------------------------------------

static int mproxy_execv(const char *path, char *const argv[]) {
    if (is_whitelisted(path)) {
        if (strip_dyld_on_whitelist()) {
            unsetenv("DYLD_INSERT_LIBRARIES");
        }
        // execv runs against the live environ; remap $PWD in place.
        remap_pwd_in_environ();
        return execv(path, argv);
    }
    // Reuse the execve path so the rewrite logic stays in one place.
    return mproxy_execve(path, argv, current_environ());
}
MPROXY_INTERPOSER(mproxy_execv, execv);

static int mproxy_execvp(const char *file, char *const argv[]) {
    if (is_whitelisted(file)) {
        if (strip_dyld_on_whitelist()) {
            unsetenv("DYLD_INSERT_LIBRARIES");
        }
        remap_pwd_in_environ();
        return execvp(file, argv);
    }
    // execvp normally resolves `file` via $PATH locally. We deliberately
    // route the unresolved name through the shim so the remote can do
    // its own PATH lookup against the remote environment — that matches
    // the semantics we want (run on the remote, not lookup-then-route).
    return mproxy_execve(file, argv, current_environ());
}
MPROXY_INTERPOSER(mproxy_execvp, execvp);

// ----- posix_spawn / posix_spawnp -----------------------------------------

static int mproxy_posix_spawn(pid_t *pid, const char *path,
                              const posix_spawn_file_actions_t *file_actions,
                              const posix_spawnattr_t *attrp,
                              char *const argv[],
                              char *const envp[]) {
    int wl = is_whitelisted(path);
    char *owned_pwd = NULL;
    char **xenvp = build_exec_envp(envp, wl && strip_dyld_on_whitelist(), &owned_pwd);
    char *const *eff_envp = xenvp ? xenvp : envp;

    int ret;
    if (wl) {
        ret = posix_spawn(pid, path, file_actions, attrp, argv, eff_envp);
    } else {
        const char *shim = getenv("MPROXY_SHIM_PATH");
        char **new_argv =
            (shim && *shim) ? build_redirect_argv(shim, path, argv) : NULL;
        if (new_argv) {
            ret = posix_spawn(pid, shim, file_actions, attrp, new_argv, eff_envp);
            free(new_argv);
        } else {
            ret = posix_spawn(pid, path, file_actions, attrp, argv, eff_envp);
        }
    }
    // posix_spawn returns to the caller; free our scratch allocations.
    free(xenvp);
    free(owned_pwd);
    return ret;
}
MPROXY_INTERPOSER(mproxy_posix_spawn, posix_spawn);

static int mproxy_posix_spawnp(pid_t *pid, const char *file,
                               const posix_spawn_file_actions_t *file_actions,
                               const posix_spawnattr_t *attrp,
                               char *const argv[],
                               char *const envp[]) {
    int wl = is_whitelisted(file);
    char *owned_pwd = NULL;
    char **xenvp = build_exec_envp(envp, wl && strip_dyld_on_whitelist(), &owned_pwd);
    char *const *eff_envp = xenvp ? xenvp : envp;

    int ret;
    if (wl) {
        ret = posix_spawnp(pid, file, file_actions, attrp, argv, eff_envp);
    } else {
        const char *shim = getenv("MPROXY_SHIM_PATH");
        char **new_argv =
            (shim && *shim) ? build_redirect_argv(shim, file, argv) : NULL;
        if (new_argv) {
            // Spawn the shim by absolute path (posix_spawn, not posix_spawnp);
            // PATH search on the shim itself is unwanted.
            ret = posix_spawn(pid, shim, file_actions, attrp, new_argv, eff_envp);
            free(new_argv);
        } else {
            ret = posix_spawnp(pid, file, file_actions, attrp, argv, eff_envp);
        }
    }
    free(xenvp);
    free(owned_pwd);
    return ret;
}
MPROXY_INTERPOSER(mproxy_posix_spawnp, posix_spawnp);
