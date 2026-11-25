# the e2e's fake home assistant (deploy/dev/compose.yml, server/tools/fakeha): a dev fake, never part of the server's
# image
FROM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS build
WORKDIR /src
COPY go.work go.work.su[m] ./
COPY pkg/ pkg/
COPY server/ server/
COPY device/ device/
RUN cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/fake-ha ./tools/fakeha

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:08476c244888bfb425bae1ae4dd7c17f093e972aab8426883d97f3c6f280f7c1
COPY --from=build /out/fake-ha /fake-ha
USER nonroot
ENTRYPOINT ["/fake-ha"]
