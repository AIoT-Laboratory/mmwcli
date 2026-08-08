# Release guide

Release assets use the exact Git tag in their names. Tag `0.2` embeds semantic application version
`0.2.0` and retains the public `0.1` asset naming contract:

- `mmwcli-0.2-linux-amd64`
- `mmwcli-0.2-linux-arm64`
- `mmwcli-0.2-windows-amd64.exe`
- `mmwcli-0.2-windows-arm64.exe`
- `mmwcli-0.2-windows-amd64-ftd2xx.exe`
- `SHA256SUMS`, `LICENSE`, and `THIRD_PARTY_NOTICES.md`
- `download-mmwcli.sh` and `download-mmwcli.ps1`

Standard binaries use `CGO_ENABLED=0`. The additional Windows amd64 binary uses the `ftd2xx`
build tag and loads the separately installed FTDI DLL. No TI or FTDI binary is bundled.

Before creating tag `0.2`, start from a clean commit that has passed the repository checks and
verify the embedded version and all release targets:

```sh
make check
CGO_ENABLED=0 go build -trimpath -ldflags "-X=mmwcli/internal/app.Version=0.2.0" ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ./cmd/mmwcli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath ./cmd/mmwcli
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath ./cmd/mmwcli
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath ./cmd/mmwcli
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -tags ftd2xx ./cmd/mmwcli
```

Every executable must have exactly one entry in `SHA256SUMS`. The downloaders resolve `latest` or
an exact requested tag through the GitHub Release API, require the exact platform asset and
`SHA256SUMS`, and fail before installation if the digest differs. Release notes must accurately
separate source validation from hardware validation and identify the user-installed TI/FTDI assets.

Tagging, uploading, and publishing remain deliberate maintainer operations. Do not publish until
the tag commit, asset list, checksums, embedded versions, release notes, and hardware-evidence
claims have all been checked.
