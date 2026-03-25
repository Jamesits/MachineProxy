#ifndef MPROXY_EXEC_HOOK_H
#define MPROXY_EXEC_HOOK_H

typedef enum {
  HOOK_MODE_ALLOW_LOCAL = 0,
  HOOK_MODE_REWRITE_TO_SHIM = 1,
} hook_mode;

struct hook_decision {
  hook_mode mode;
  const char *shim_path;
};

struct hook_decision hook_decide_exec(const char *pathname, char *const argv[]);

// Testing helpers. These are not part of the public API.
void hook_test_reset_snapshot(void);
void hook_test_set_snapshot(char *const envp[]);

#endif
