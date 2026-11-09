# Third-party notices

This project contains adapted portions of:

- `byJoey/fanout`, copyright 2026 Joey (byJoey), licensed under the MIT
  License. The original license text is preserved in this repository's
  `LICENSE`.

At installation time, the project downloads a pinned revision of:

- `Aki1106-0116/Three-Protocol-Script`,
  `jb-combo-installer.sh` at commit
  `68d15b2397bb8df8f058c004c29ac6872fded09d`.

The fixed version incorporates and maintains the adapted installer in
`node-installer.sh` and `lib/node-*.sh`. It no longer downloads or text-patches
the upstream installer at runtime. The upstream commit and original checksum
are retained in the audit record as provenance.

The Go module also depends on `golang.org/x/sys` (BSD-3-Clause) and
`gopkg.in/yaml.v3` (MIT/Apache-2.0). Their source distributions retain their
license notices. Protocol binaries are obtained from their respective official
projects and remain subject to those projects' licenses.
