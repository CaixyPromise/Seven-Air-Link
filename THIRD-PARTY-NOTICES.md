# Third-party notices

`internal/thirdparty/pbkdf2` contains the unmodified Go `golang.org/x/crypto`
PBKDF2 implementation, tag `v0.31.0`, under the Go Authors' BSD-3-Clause license.
Source blob: `28cd99c7f3fc50b03f3789b1013401db46d40bf9`.
License blob: `2a7cf70da6e498df9c11ab6a5eaa2ddd7af34da4`.
Binary ZIPs include `licenses/pbkdf2-LICENSE`. The wrapper uses random salts,
a versioned bounded format, PBKDF2-HMAC-SHA256/600000, and constant-time comparison.
This is not an invented password algorithm or a claim of FIPS validation.

SQLite remains the native system C engine. OS runtime components and bundled
Windows DLLs retain their upstream licenses. This RC is not independently
security-certified.
