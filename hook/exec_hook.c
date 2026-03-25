#define _GNU_SOURCE

#include "exec_hook.h"

#include <dlfcn.h>
#include <errno.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

extern char **environ;

typedef int (*real_execve_fn)(const char *pathname, char *const argv[], char *const envp[]);

static real_execve_fn g_real_execve = NULL;
static pthread_once_t g_real_execve_once = PTHREAD_ONCE_INIT;

static void init_real_execve(void) {
  g_real_execve = (real_execve_fn)dlsym(RTLD_NEXT, "execve");
}

static real_execve_fn real_execve_ptr(void) {
  pthread_once(&g_real_execve_once, init_real_execve);
  return g_real_execve;
}

// --- Utility functions ---

static size_t count_argv(char *const argv[]) {
  size_t argc = 0;
  if (argv == NULL) {
    return 0;
  }
  while (argv[argc] != NULL) {
    argc++;
  }
  return argc;
}

static size_t count_env(char *const envp[]) {
  size_t n = 0;
  if (envp == NULL) {
    return 0;
  }
  while (envp[n] != NULL) {
    n++;
  }
  return n;
}

// Extract the variable name length (everything before '=').
static size_t env_name_len(const char *entry) {
  const char *eq = strchr(entry, '=');
  if (eq == NULL) {
    return strlen(entry);
  }
  return (size_t)(eq - entry);
}

// --- Environment snapshot (captured at library load time) ---

static char **g_env_snapshot = NULL;
static size_t g_env_snapshot_len = 0;

static void snapshot_env(void) __attribute__((constructor));

static void snapshot_env(void) {
  if (environ == NULL) {
    return;
  }
  size_t n = 0;
  while (environ[n] != NULL) {
    n++;
  }
  g_env_snapshot = calloc(n + 1, sizeof(char *));
  if (g_env_snapshot == NULL) {
    return;
  }
  for (size_t i = 0; i < n; i++) {
    g_env_snapshot[i] = strdup(environ[i]);
    if (g_env_snapshot[i] == NULL) {
      g_env_snapshot_len = i;
      g_env_snapshot[i] = NULL;
      return;
    }
  }
  g_env_snapshot_len = n;
  g_env_snapshot[n] = NULL;
}

// Find an entry in the snapshot by variable name. Returns the full
// "KEY=VALUE" string or NULL.
static const char *snapshot_find(const char *name, size_t nlen) {
  for (size_t i = 0; i < g_env_snapshot_len; i++) {
    size_t sn_len = env_name_len(g_env_snapshot[i]);
    if (sn_len == nlen && strncmp(g_env_snapshot[i], name, nlen) == 0) {
      return g_env_snapshot[i];
    }
  }
  return NULL;
}

// Build a colon-separated list of env var names that are new or changed
// relative to the snapshot. Returns a malloc'd string like
// "MPROXY_CHANGED_ENVS=FOO:BAR" or NULL on allocation failure / no changes.
static char *build_changed_envs(char *const envp[]) {
  static const char prefix[] = "MPROXY_CHANGED_ENVS=";
  static const size_t prefix_len = sizeof(prefix) - 1;

  if (g_env_snapshot == NULL || envp == NULL) {
    return NULL;
  }

  size_t envc = count_env(envp);

  // First pass: compute total length needed.
  size_t total_len = prefix_len;
  int count = 0;

  for (size_t i = 0; i < envc; i++) {
    size_t nlen = env_name_len(envp[i]);

    // Skip internal MPROXY vars.
    if ((nlen == sizeof("MPROXY_CHANGED_ENVS") - 1 &&
         strncmp(envp[i], "MPROXY_CHANGED_ENVS", nlen) == 0) ||
        (nlen == sizeof("MPROXY_HOOK_BYPASS") - 1 &&
         strncmp(envp[i], "MPROXY_HOOK_BYPASS", nlen) == 0)) {
      continue;
    }

    const char *snap = snapshot_find(envp[i], nlen);
    if (snap == NULL || strcmp(snap, envp[i]) != 0) {
      if (count > 0) {
        total_len++;
      }
      total_len += nlen;
      count++;
    }
  }

  if (count == 0) {
    return NULL;
  }

  char *buf = malloc(total_len + 1);
  if (buf == NULL) {
    return NULL;
  }

  memcpy(buf, prefix, prefix_len);
  size_t pos = prefix_len;
  int written = 0;

  for (size_t i = 0; i < envc; i++) {
    size_t nlen = env_name_len(envp[i]);

    if ((nlen == sizeof("MPROXY_CHANGED_ENVS") - 1 &&
         strncmp(envp[i], "MPROXY_CHANGED_ENVS", nlen) == 0) ||
        (nlen == sizeof("MPROXY_HOOK_BYPASS") - 1 &&
         strncmp(envp[i], "MPROXY_HOOK_BYPASS", nlen) == 0)) {
      continue;
    }

    const char *snap = snapshot_find(envp[i], nlen);
    if (snap == NULL || strcmp(snap, envp[i]) != 0) {
      if (written > 0) {
        buf[pos++] = ':';
      }
      memcpy(buf + pos, envp[i], nlen);
      pos += nlen;
      written++;
    }
  }

  buf[pos] = '\0';
  return buf;
}

// --- Test helpers ---

void hook_test_reset_snapshot(void) {
  if (g_env_snapshot != NULL) {
    for (size_t i = 0; i < g_env_snapshot_len; i++) {
      free(g_env_snapshot[i]);
    }
    free(g_env_snapshot);
    g_env_snapshot = NULL;
    g_env_snapshot_len = 0;
  }
}

void hook_test_set_snapshot(char *const envp[]) {
  hook_test_reset_snapshot();
  if (envp == NULL) {
    return;
  }
  size_t n = count_env(envp);
  g_env_snapshot = calloc(n + 1, sizeof(char *));
  if (g_env_snapshot == NULL) {
    return;
  }
  for (size_t i = 0; i < n; i++) {
    g_env_snapshot[i] = strdup(envp[i]);
  }
  g_env_snapshot_len = n;
  g_env_snapshot[n] = NULL;
}

// --- Hook decision logic ---

static int hook_bypass_enabled(void) {
  const char *v = getenv("MPROXY_HOOK_BYPASS");
  return v != NULL && strcmp(v, "1") == 0;
}

static int whitelist_allows(const char *pathname) {
  const char *raw = getenv("MPROXY_WHITELIST");
  if (raw == NULL || *raw == '\0') {
    return 0;
  }

  size_t plen = strlen(pathname);
  const char *cursor = raw;

  while (*cursor != '\0') {
    const char *sep = strchr(cursor, ':');
    size_t len = sep ? (size_t)(sep - cursor) : strlen(cursor);

    if (len == plen && strncmp(cursor, pathname, plen) == 0) {
      return 1;
    }

    if (sep == NULL) {
      break;
    }
    cursor = sep + 1;
  }

  return 0;
}

static char **build_shim_argv(const char *shim, const char *pathname, char *const argv[]) {
  size_t argc = count_argv(argv);
  char **new_argv = calloc(argc + 3, sizeof(char *));
  if (new_argv == NULL) {
    return NULL;
  }

  new_argv[0] = (char *)shim;
  new_argv[1] = (char *)pathname;
  for (size_t i = 0; i < argc; i++) {
    new_argv[i + 2] = argv[i];
  }
  new_argv[argc + 2] = NULL;
  return new_argv;
}

static char **build_shim_env(char *const envp[]) {
  static const char bypass_key[] = "MPROXY_HOOK_BYPASS=";
  static const char bypass_value[] = "MPROXY_HOOK_BYPASS=1";

  char *const *base = envp ? envp : environ;
  size_t envc = count_env(base);

  // +3: room for bypass, changed_envs, and NULL terminator.
  char **new_env = calloc(envc + 3, sizeof(char *));
  if (new_env == NULL) {
    return NULL;
  }

  int replaced = 0;
  for (size_t i = 0; i < envc; i++) {
    if (strncmp(base[i], bypass_key, sizeof(bypass_key) - 1) == 0) {
      new_env[i] = (char *)bypass_value;
      replaced = 1;
    } else {
      new_env[i] = base[i];
    }
  }
  if (!replaced) {
    new_env[envc++] = (char *)bypass_value;
  }

  // Append MPROXY_CHANGED_ENVS with var names that differ from the snapshot.
  char *changed = build_changed_envs(base);
  if (changed != NULL) {
    new_env[envc++] = changed;
  }

  new_env[envc] = NULL;

  return new_env;
}

struct hook_decision hook_decide_exec(const char *pathname, char *const argv[]) {
  (void)argv;

  struct hook_decision d;
  d.mode = HOOK_MODE_ALLOW_LOCAL;
  d.shim_path = getenv("MPROXY_SHIM_PATH");

  if (pathname == NULL || d.shim_path == NULL || d.shim_path[0] == '\0') {
    return d;
  }

  if (hook_bypass_enabled()) {
    return d;
  }

  if (strcmp(pathname, d.shim_path) == 0) {
    return d;
  }

  if (whitelist_allows(pathname)) {
    return d;
  }

  d.mode = HOOK_MODE_REWRITE_TO_SHIM;
  return d;
}

int execve(const char *pathname, char *const argv[], char *const envp[]) {
  real_execve_fn real_execve = real_execve_ptr();
  if (real_execve == NULL) {
    errno = ENOSYS;
    return -1;
  }

  struct hook_decision d = hook_decide_exec(pathname, argv);
  if (d.mode == HOOK_MODE_ALLOW_LOCAL) {
    return real_execve(pathname, argv, envp);
  }

  char **shim_argv = build_shim_argv(d.shim_path, pathname, argv);
  if (shim_argv == NULL) {
    errno = ENOMEM;
    return -1;
  }

  char **shim_env = build_shim_env(envp);
  if (shim_env == NULL) {
    free(shim_argv);
    errno = ENOMEM;
    return -1;
  }

  int rc = real_execve(d.shim_path, shim_argv, shim_env);

  free(shim_env);
  free(shim_argv);
  return rc;
}

int execv(const char *pathname, char *const argv[]) {
  return execve(pathname, argv, environ);
}

// TODO: explore execveat compatibility
