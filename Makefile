.SILENT :
.PHONY : lgtm clean fmt

TAG:=`git describe --abbrev=0 --tags`
LDFLAGS:=-X main.buildVersion=$(TAG)

all: lgtm

lgtm:
	echo "Building lgtm $(LDFLAGS)"
	go install -ldflags "$(LDFLAGS)"

dist-clean:
	rm -rf dist
	rm -f lgtm-*.tar.gz

dist: dist-clean
	mkdir -p dist/alpine-linux/amd64 && cp reviewers.yaml dist/alpine-linux/amd64 && GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -a -tags netgo -installsuffix netgo -o dist/alpine-linux/amd64/lgtm 
	mkdir -p dist/linux/amd64 && cp reviewers.yaml dist/linux/amd64 && GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/linux/amd64/lgtm
	mkdir -p dist/linux/armel && cp reviewers.yaml dist/linux/armel && GOOS=linux GOARCH=arm GOARM=5 go build -ldflags "$(LDFLAGS)" -o dist/linux/armel/lgtm
	mkdir -p dist/linux/armhf && cp reviewers.yaml dist/linux/armhf && GOOS=linux GOARCH=arm GOARM=6 go build -ldflags "$(LDFLAGS)" -o dist/linux/armhf/lgtm
	mkdir -p dist/darwin/amd64 && cp reviewers.yaml dist/darwin/amd64 && GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/darwin/amd64/lgtm

release: dist
	tar -cvzf lgtm-alpine-linux-amd64-$(TAG).tar.gz -C dist/alpine-linux/amd64 lgtm reviewers.yaml
	tar -cvzf lgtm-linux-amd64-$(TAG).tar.gz -C dist/linux/amd64 lgtm reviewers.yaml
	tar -cvzf lgtm-linux-armel-$(TAG).tar.gz -C dist/linux/armel lgtm reviewers.yaml
	tar -cvzf lgtm-linux-armhf-$(TAG).tar.gz -C dist/linux/armhf lgtm reviewers.yaml
	tar -cvzf lgtm-darwin-amd64-$(TAG).tar.gz -C dist/darwin/amd64 lgtm reviewers.yaml
