FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/momentarr ./cmd/momentarr \
 && mkdir -m 1777 /out/tmp

# Static stdlib-only binary: CA certs for the cached-clearance HTTPS fetch and
# a writable /tmp for the cookie cache are all it needs.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/tmp /tmp
COPY --from=build /out/momentarr /momentarr
USER 1000:1000
EXPOSE 8191
ENTRYPOINT ["/momentarr"]
