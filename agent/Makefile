VERSION ?= dev
CFLAGS ?= -O2 -g -Wall -Wextra -Wno-deprecated-declarations
override CFLAGS += -DVERSION='"$(VERSION)"'
LIBS = -lcurl -lssl -lcrypto

mmd: mmd.c
	$(CC) $(CFLAGS) -o $@ mmd.c $(LIBS)

tests/test_mount: tests/test_mount.c mmd.c
	$(CC) $(CFLAGS) -Wno-unused-function -o $@ tests/test_mount.c $(LIBS)

# Mount-logic tests: real reconcile code vs fake mount/umount/sudo (no root or NFS needed).
check: tests/test_mount
	MM_AGENT_DIR=$(CURDIR) tests/test_mount

clean:
	rm -f mmd tests/test_mount
.PHONY: check clean
