CC ?= cc
CFLAGS ?= -O2 -Wall -Wextra -fPIC
LDFLAGS_SHARED ?= -shared

LIB = libmproxyhook.so
TEST_BIN = exec_hook_test.out

.PHONY: all test clean

all: $(LIB)

$(LIB): exec_hook.c exec_hook.h
	$(CC) $(CFLAGS) $(LDFLAGS_SHARED) -o $@ exec_hook.c -ldl -lpthread

$(TEST_BIN): exec_hook.c exec_hook.h tests/exec_hook_test.c
	$(CC) $(CFLAGS) -o $@ exec_hook.c tests/exec_hook_test.c -ldl -lpthread

test: $(TEST_BIN)
	./$(TEST_BIN)

clean:
	rm -f $(LIB) $(TEST_BIN)
