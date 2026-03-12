CC      ?= gcc
CFLAGS  ?= -shared -fPIC -O2 -Wall

.PHONY: all install clean

all: inject.so

inject.so: inject.c
	$(CC) $(CFLAGS) -o $@ $<

# Install to the path ContainerManager expects on the host.
install: inject.so
	install -m 0755 inject.so /opt/boxnex/inject.so

clean:
	rm -f inject.so
