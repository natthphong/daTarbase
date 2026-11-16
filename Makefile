.PHONY: test phase p progress readme check race

# make test                — run every test
test:
	go test ./...

# make phase P=02          — run one phase
phase:
	go test -count=1 ./$$(ls -d phase$(P)_*)/

# make p P=2.3             — run the tests of one problem
p:
	go test -count=1 -v -run '^TestP$(subst .,_,$(P))_' ./phase$$(printf %02d $(firstword $(subst ., ,$(P))))_*/

# make progress            — what have I solved?
progress:
	go run ./tools/dbtool progress

race:
	go test -race -count=1 ./...

readme:
	go run ./tools/dbtool readme

check:
	go run ./tools/dbtool check
