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
  char **new_env = calloc(envc + 2, sizeof(char *));
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
