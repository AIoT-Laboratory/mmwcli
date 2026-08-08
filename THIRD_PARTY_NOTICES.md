# Third-party notices

mmwcli is licensed under the MIT License; see [LICENSE](LICENSE). `go.mod` contains no third-party
modules. Core builds use only the Go standard library. The optional native backend loads the
user-installed FTDI D2XX library at runtime on Windows and uses the user-installed D2XX library at
build, link, and runtime on Linux.

The implementation refers to protocol material supplied by Texas Instruments for xWR68xx, Radar
Toolbox Studio CLI, and DCA1000. TI firmware, host tools, reference source, profiles, manifests, and
documentation are outside this project's MIT license and are not distributed in the repository or
release archives. Users must obtain these assets from their own licensed TI installation and follow
the terms attached to each file and its manifest.

`studio_cli` and `mmwave_Studio_cli_xwr68xx.bin` are original TI Radar Toolbox asset names. The
functional route only requires the user to flash this device firmware; the complete Toolbox,
reference source, profiles, manifests, and host tools are not build or runtime dependencies. The
repository's `hardware/studio-cli-xwr6843-raw.cfg` is this project's raw-capture validation
configuration, not an official TI monitor profile. The MSS/BSS firmware checked by `debug-cli`
must also be supplied by the user.

## FTDI D2XX

The optional `ftd2xx` build tag for `debug-cli` binds only the minimum D2XX ABI needed to open
the A/B interfaces explicitly, initialize MPSSE, and carry the SPI/IRQ transport. The repository and
release archives contain no FTDI library, header, driver, or installer. The official `ftd2xx.h` and
`libftd2xx.so` used by Linux builds must also be supplied by the user. These files are outside this
project's MIT license.

Obtain the version for the target operating system and architecture from the
[official FTDI D2XX download page](https://ftdichip.com/drivers/d2xx-drivers/) and follow its license
terms. API definitions are documented in the
[FTDI D2XX Programmer's Guide](https://ftdichip.com/wp-content/uploads/2023/09/D2XX_Programmers_Guide.pdf).

Texas Instruments, TI, mmWave, and related product names and trademarks belong to their respective
owners. This project is not affiliated with or endorsed by Texas Instruments.
