FROM golang:latest
MAINTAINER Jess Frazelle <jess@docker.com>

RUN go get github.com/bitly/go-nsq && \
    go get github.com/Sirupsen/logrus && \
    go get code.google.com/p/go.codereview/patch && \
    go get github.com/crosbymichael/octokat && \
    go get github.com/drone/go-github/github

ADD . /go/src/github.com/jfrazelle/gh-patch-parser
RUN cd /go/src/github.com/jfrazelle/gh-patch-parser && go install . ./...
ENV PATH $PATH:/go/bin

# make git happy
RUN git config --global user.name gh-patch-parser && \
    git config --global user.email gh-patch-parser@dockerproject.com

ENTRYPOINT ["gh-patch-parser"]
