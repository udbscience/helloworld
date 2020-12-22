FROM golang:1.14 as build

ENV GOOS linux
ENV GOARCH 386

WORKDIR /go/src/app
COPY main.go .

RUN go get -d -v ./...
RUN go install -v ./...


FROM gcr.io/google.com/cloudsdktool/cloud-sdk:266.0.0
COPY --from=build ./go/bin/linux_386/app /bin
