#include "../exec_hook.h"

#include <assert.h>
#include <stdlib.h>

static void test_non_whitelisted_exec_rewrites_to_shim(void) {
  setenv("MPROXY_SHIM_PATH", "/opt/machineproxy/mproxy-shim", 1);
  setenv("MPROXY_WHITELIST", "/bin/echo:/usr/bin/env", 1);
  unsetenv("MPROXY_HOOK_BYPASS");

  struct hook_decision d = hook_decide_exec("/usr/bin/python3", (char *const[]){"python3", "-V", NULL});
  assert(d.mode == HOOK_MODE_REWRITE_TO_SHIM);
}

static void test_whitelisted_exec_runs_local(void) {
  setenv("MPROXY_SHIM_PATH", "/opt/machineproxy/mproxy-shim", 1);
  setenv("MPROXY_WHITELIST", "/bin/echo:/usr/bin/env", 1);
  unsetenv("MPROXY_HOOK_BYPASS");

  struct hook_decision d = hook_decide_exec("/usr/bin/env", (char *const[]){"env", NULL});
  assert(d.mode == HOOK_MODE_ALLOW_LOCAL);
}

int main(void) {
  test_non_whitelisted_exec_rewrites_to_shim();
  test_whitelisted_exec_runs_local();
  return 0;
}
