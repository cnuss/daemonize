.PHONY: test e2e run

# Library package tests only (excludes examples).
test:
	go test .

# End-to-end: the harness builds and drives every example binary. -count=1 disables
# go test caching, since the harness builds the example binaries at runtime and the
# cache key wouldn't otherwise pick up example source changes.
e2e:
	go test -count=1 -v ./e2e

# Run an example by name, forwarding any trailing words as args:
#   make run hello start
#   make run with-args start --port 9000
run:
	cd examples/$(word 2,$(MAKECMDGOALS)) && go run . $(wordlist 3,$(words $(MAKECMDGOALS)),$(MAKECMDGOALS))

# Swallow the example name and forwarded args (extra goals) so make doesn't error.
%:
	@:
