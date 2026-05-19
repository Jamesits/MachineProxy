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
// scope reduction documented in the README.
//
// Built per-arch (arm64 + amd64) by darwin/interposer/build.sh.

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

// filter_envp_strip_dyld returns a newly-allocated, NULL-terminated
// envp with any DYLD_INSERT_LIBRARIES=... entry removed. Caller frees
// with free(). Returns NULL on allocation failure; callers must fall
// back to the original envp in that case.
static char **filter_envp_strip_dyld(char *const envp[]) {
    if (!envp) {
        return NULL;
    }
    static const char prefix[] = "DYLD_INSERT_LIBRARIES=";
    static const size_t prefix_len = sizeof(prefix) - 1;

    size_t n = 0;
    while (envp[n]) {
        n++;
    }
    char **out = (char **)calloc(n + 1, sizeof(char *));
    if (!out) {
        return NULL;
    }
    size_t j = 0;
    for (size_t i = 0; i < n; i++) {
        if (strncmp(envp[i], prefix, prefix_len) == 0) {
            continue;
        }
        out[j++] = envp[i];
    }
    out[j] = NULL;
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

// ----- execve --------------------------------------------------------------

static int mproxy_execve(const char *path,
                         char *const argv[],
                         char *const envp[]) {
    if (is_whitelisted(path)) {
        if (strip_dyld_on_whitelist()) {
            char **filtered = filter_envp_strip_dyld(envp);
            if (filtered) {
                int ret = execve(path, argv, filtered);
                free(filtered);
                return ret;
            }
        }
        return execve(path, argv, envp);
    }
    const char *shim = getenv("MPROXY_SHIM_PATH");
    if (!shim || !*shim) {
        return execve(path, argv, envp);
    }
    char **new_argv = build_redirect_argv(shim, path, argv);
    if (!new_argv) {
        return execve(path, argv, envp);
    }
    int ret = execve(shim, new_argv, envp);
    // execve only returns on failure; free the allocation on the error
    // path. The replacement argv referenced original argv strings, but
    // we never own those.
    free(new_argv);
    return ret;
}
MPROXY_INTERPOSER(mproxy_execve, execve);

// ----- execv / execvp -----------------------------------------------------

static int mproxy_execv(const char *path, char *const argv[]) {
    if (is_whitelisted(path)) {
        if (strip_dyld_on_whitelist()) {
            unsetenv("DYLD_INSERT_LIBRARIES");
        }
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
    if (is_whitelisted(path)) {
        if (strip_dyld_on_whitelist()) {
            char **filtered = filter_envp_strip_dyld(envp);
            if (filtered) {
                int ret = posix_spawn(pid, path, file_actions, attrp, argv, filtered);
                free(filtered);
                return ret;
            }
        }
        return posix_spawn(pid, path, file_actions, attrp, argv, envp);
    }
    const char *shim = getenv("MPROXY_SHIM_PATH");
    if (!shim || !*shim) {
        return posix_spawn(pid, path, file_actions, attrp, argv, envp);
    }
    char **new_argv = build_redirect_argv(shim, path, argv);
    if (!new_argv) {
        return posix_spawn(pid, path, file_actions, attrp, argv, envp);
    }
    int ret = posix_spawn(pid, shim, file_actions, attrp, new_argv, envp);
    free(new_argv);
    return ret;
}
MPROXY_INTERPOSER(mproxy_posix_spawn, posix_spawn);

static int mproxy_posix_spawnp(pid_t *pid, const char *file,
                               const posix_spawn_file_actions_t *file_actions,
                               const posix_spawnattr_t *attrp,
                               char *const argv[],
                               char *const envp[]) {
    if (is_whitelisted(file)) {
        if (strip_dyld_on_whitelist()) {
            char **filtered = filter_envp_strip_dyld(envp);
            if (filtered) {
                int ret = posix_spawnp(pid, file, file_actions, attrp, argv, filtered);
                free(filtered);
                return ret;
            }
        }
        return posix_spawnp(pid, file, file_actions, attrp, argv, envp);
    }
    const char *shim = getenv("MPROXY_SHIM_PATH");
    if (!shim || !*shim) {
        return posix_spawnp(pid, file, file_actions, attrp, argv, envp);
    }
    char **new_argv = build_redirect_argv(shim, file, argv);
    if (!new_argv) {
        return posix_spawnp(pid, file, file_actions, attrp, argv, envp);
    }
    // Spawn the shim by absolute path (posix_spawn, not posix_spawnp);
    // PATH search on the shim itself is unwanted.
    int ret = posix_spawn(pid, shim, file_actions, attrp, new_argv, envp);
    free(new_argv);
    return ret;
}
MPROXY_INTERPOSER(mproxy_posix_spawnp, posix_spawnp);
