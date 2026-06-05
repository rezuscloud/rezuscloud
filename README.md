# RezusCloud

Management plane and CLI for the RezusCloud Personal Cloud. Two binaries from
one repo, released together: `rezuscloud` (management plane server) and
`rezusctl` (CLI).

## Build

    go build -o rezuscloud .
    go build -o rezusctl ./cmd/rezusctl

## Run

    REZUSCLOUD_ADMIN_PASSWORD=changeme ./rezuscloud

## Test

    go test ./... -count=1 -race
    golangci-lint run ./...

## Deploy

Kubernetes deployment runs via the Helm chart — see
[chart/README.md](chart/README.md).

## License

MIT — see [LICENSE](LICENSE).
