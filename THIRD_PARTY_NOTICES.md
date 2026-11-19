# Third-party notices

This file records third-party provenance and required notices for
`script-agent`. It does **not** license original `script-agent` code.

There is no root `LICENSE` file in this repository. No license is granted for
the original `script-agent` code, and it is not licensed for external
redistribution beyond the owner's authorization. The owner must choose a
license before publishing or redistributing this repository.

The vendored `third_party/pi-go` source and the
`github.com/keejkrej/quickpy` dependency also have no license file at their
pinned revisions. No license is asserted for their original code. Their
upstream projects must make their own licensing decisions.

The embedded WASM binaries do not carry SPDX headers. The notices below are
based on the pinned upstream sources, package metadata, and the Go module cache
used to build this repository. Versions and hashes are recorded in
[internal/artifacts/PROVENANCE.md](internal/artifacts/PROVENANCE.md) and
[third_party/pi-go/PROVENANCE.md](third_party/pi-go/PROVENANCE.md).

This file is not legal advice.

## Contents

1. [Original script-agent code](#1-original-script-agent-code)
2. [Vendored pi-go runtime](#2-vendored-pi-go-runtime)
3. [QuickPy](#3-quickpy)
4. [MicroPython](#4-micropython)
5. [wasi-libc and musl](#5-wasi-libc-and-musl)
6. [QuickJS-WASI](#6-quickjs-wasi)
7. [QuickJS-NG](#7-quickjs-ng)
8. [wazero](#8-wazero)
9. [Go Authors BSD components](#9-go-authors-bsd-components)
10. [Build-time tools](#10-build-time-tools)
11. [Notes on Prime/RLM and design inspiration](#11-notes-on-primerm-and-design-inspiration)

---

## 1. Original script-agent code

- **Components:** the root Go package, `adapter/quickjs`, `process`, `cmd/*`,
  `scripts/*`, and the documentation in this repository.
- **License:** none granted. There is no root `LICENSE` file.
- **Status:** the owner must choose a license before external redistribution.
  Do not describe this code as MIT, Apache-2.0, BSD, or any other license
  without an explicit owner decision and a root license file.

This notice does not grant permission to copy, modify, or redistribute the
original code beyond what the owner authorizes.

---

## 2. Vendored pi-go runtime

- **Component:** `third_party/pi-go`, a minimal source-vendored copy of the
  generic codemode runtime from `github.com/keejkrej/pi-go`.
- **Upstream commit:** `69d43f6d9a4b83333d4582ca8c755457702d6576`
  (`Merge pull request #5 from keejkrej/chore/repo-cleanup-and-docs`).
- **Vendored on:** 2026-11-19.
- **License:** no root `LICENSE` file exists at the pinned commit. The raw
  `LICENSE` URL returned 404 when checked on 2026-11-19. No license is
  asserted here for the vendored source.
- **Status:** the owner must decide the upstream project's license and wrap
  this notice later. Do not assume the upstream repository is MIT or any other
  license.

This directory is not a fork of the whole upstream repository. It is a minimal
production closure plus the tests that pin the archived generic runtime
behavior. The module path is kept as `github.com/keejkrej/pi-go` so the
vendored `internal/...` imports stay legal. The only expected differences from
upstream are documented in
[third_party/pi-go/PROVENANCE.md](third_party/pi-go/PROVENANCE.md):

1. `TrustedPreludeSource`.
2. `CodemodeErrorCoder` / `CodemodeErrorCode`.
3. `CodemodeSandboxOptions.MaxOutputBytes`.

The embedded `third_party/pi-go/internal/quickjs/quickjs.wasm` is the upstream
QuickJS-WASI build and is covered by the QuickJS-WASI and QuickJS-NG notices
below.

No Prime/RLM code is copied into this repository. The Prime agent's MIT license
is not reproduced here because it does not cover copied code. See
[§11](#11-notes-on-primerm-and-design-inspiration).

---

## 3. QuickPy

- **Component:** `github.com/keejkrej/quickpy`, a direct Go module dependency.
- **Version:** `v0.0.0-20261118233109-3897a879f468`.
- **Commit:** `3897a879f468c47cb5c70235e3cf9872ab7fc8a8`
  (`Merge pull request #4 from keejkrej/m2/scripting-foundation`).
- **License:** no `LICENSE` file exists at the pinned commit. The raw
  `LICENSE` URL returned 404 when checked on 2026-11-19. The upstream README
  and `THIRD_PARTY_NOTICES.md` state that no project license has been chosen.
  No license is asserted here for QuickPy's original code.
- **Status:** the owner must decide the QuickPy project's license before
  external redistribution.

The compiled artifact `internal/artifacts/quickpy.wasm` embeds MicroPython and
wasi-libc code. Those components are covered by the notices below. The QuickPy
guest adapter and frozen Python modules are original QuickPy code and are not
licensed by this file.

See [internal/artifacts/PROVENANCE.md](internal/artifacts/PROVENANCE.md) for
the artifact size, SHA-256, ABI, engine, and build toolchain.

---

## 4. MicroPython

- **Component:** MicroPython v1.29.0, used as the QuickPy guest parser, bytecode
  VM, GC, builtins, and several frozen Python modules.
- **Commit:** `0fd6c573ea815774668bbb16b8e197c8822368b2`.
- **How included:** the MicroPython source is not vendored. The QuickPy build
  script clones the pinned checkout at build time. The compiled artifact
  embeds MicroPython code.
- **License:** MIT.
- **Copyright:** Copyright (c) 2013-2026 Damien P. George. Individual files may
  include additional copyright holders; the asyncio files carry
  `Copyright (c) 2019-2022 Damien P. George`.
- **Upstream:** <https://github.com/micropython/micropython>
- **License text:** <https://github.com/micropython/micropython/blob/v1.29.0/LICENSE>

```text
The MIT License (MIT)

Copyright (c) 2013-2026 Damien P. George

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

MicroPython's own `LICENSE` notes that some ports and bundled libraries carry
different licenses. The QuickPy build uses the core interpreter and a narrow
set of extmod/frozen modules; it does not vendor the port-specific third-party
trees listed in MicroPython's license summary.

---

## 5. wasi-libc and musl

### wasi-libc

- **Component:** wasi-libc, used for the WebAssembly setjmp/longjmp runtime and
  the C library surface compiled into the QuickPy guest.
- **How included:** compiled into `internal/artifacts/quickpy.wasm` through Zig
  0.17.1's wasi-libc and a local `setjmp_rt.c` adapted from wasi-libc's
  `libc-top-half/musl/src/setjmp/wasm32/rt.c`.
- **License:** wasi-libc as a whole is multi-licensed under the Apache License
  v2.0 with LLVM Exceptions, the Apache License v2.0, and the MIT License. The
  `libc-top-half/musl/` portion is derived from musl and is MIT licensed;
  wasi-libc's changes to those files are multi-licensed under the same set plus
  the original licenses.
- **Upstream:** <https://github.com/WebAssembly/wasi-libc>
- **License files:**
  - <https://github.com/WebAssembly/wasi-libc/blob/main/LICENSE>
  - <https://github.com/WebAssembly/wasi-libc/blob/main/LICENSE-MIT>
  - <https://github.com/WebAssembly/wasi-libc/blob/main/LICENSE-APACHE>
  - <https://github.com/WebAssembly/wasi-libc/blob/main/LICENSE-APACHE-LLVM>
  - musl copyright: <https://github.com/WebAssembly/wasi-libc/blob/main/libc-top-half/musl/COPYRIGHT>

The MIT text below is reproduced from wasi-libc's `LICENSE-MIT`. The Apache
License 2.0 text is reproduced in [§8](#8-wazero) and also covers wasi-libc's
Apache-2.0 licensing option. The Apache-2.0-with-LLVM-Exceptions variant is
available at the upstream URL above.

```text
Permission is hereby granted, free of charge, to any
person obtaining a copy of this software and associated
documentation files (the "Software"), to deal in the
Software without restriction, including without
limitation the rights to use, copy, modify, merge,
publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software
is furnished to do so, subject to the following
conditions:

The above copyright notice and this permission notice
shall be included in all copies or substantial portions
of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF
ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED
TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A
PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT
SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR
IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
DEALINGS IN THE SOFTWARE.
```

### musl COPYRIGHT notice

The `libc-top-half/musl/` portion of wasi-libc is derived from musl. The
following is the musl `COPYRIGHT` notice, retrieved from the authoritative
upstream wasi-libc repository at
<https://raw.githubusercontent.com/WebAssembly/wasi-libc/main/libc-top-half/musl/COPYRIGHT>
on 2026-11-17. The copy bundled with Zig 0.17.1 is identical apart from one
contributor-name spelling (`Valentin Ochs` vs. `Lynn Ochs`).

```text
musl as a whole is licensed under the following standard MIT license:

----------------------------------------------------------------------
Copyright © 2005-2020 Rich Felker, et al.

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
----------------------------------------------------------------------

Authors/contributors include:

A. Wilcox
Ada Worcester
Alex Dowad
Alex Suykov
Alexander Monakov
Andre McCurdy
Andrew Kelley
Anthony G. Basile
Aric Belsito
Arvid Picciani
Bartosz Brachaczek
Benjamin Peterson
Bobby Bingham
Boris Brezillon
Brent Cook
Chris Spiegel
Clément Vasseur
Daniel Micay
Daniel Sabogal
Daurnimator
David Carlier
David Edelsohn
Denys Vlasenko
Dmitry Ivanov
Dmitry V. Levin
Drew DeVault
Emil Renner Berthing
Fangrui Song
Felix Fietkau
Felix Janda
Gianluca Anzolin
Hauke Mehrtens
He X
Hiltjo Posthuma
Isaac Dunham
Jaydeep Patil
Jens Gustedt
Jeremy Huntwork
Jo-Philipp Wich
Joakim Sindholt
John Spencer
Julien Ramseier
Justin Cormack
Kaarle Ritvanen
Khem Raj
Kylie McClain
Leah Neukirchen
Luca Barbato
Luka Perkov
Lynn Ochs
M Farkas-Dyck (Strake)
Mahesh Bodapati
Markus Wichmann
Masanori Ogino
Michael Clark
Michael Forney
Mikhail Kremnyov
Natanael Copa
Nicholas J. Kain
orc
Pascal Cuoq
Patrick Oppenlander
Petr Hosek
Petr Skocik
Pierre Carrier
Reini Urban
Rich Felker
Richard Pennington
Ryan Fairfax
Samuel Holland
Segev Finer
Shiz
sin
Solar Designer
Stefan Kristiansson
Stefan O'Rear
Szabolcs Nagy
Timo Teräs
Trutz Behn
Will Dietz
William Haddon
William Pitcock

Portions of this software are derived from third-party works licensed
under terms compatible with the above MIT license:

The TRE regular expression implementation (src/regex/reg* and
src/regex/tre*) is Copyright © 2001-2008 Ville Laurikari and licensed
under a 2-clause BSD license (license text in the source files). The
included version has been heavily modified by Rich Felker in 2012, in
the interests of size, simplicity, and namespace cleanliness.

Much of the math library code (src/math/* and src/complex/*) is
Copyright © 1993,2004 Sun Microsystems or
Copyright © 2003-2011 David Schultz or
Copyright © 2003-2009 Steven G. Kargl or
Copyright © 2003-2009 Bruce D. Evans or
Copyright © 2008 Stephen L. Moshier or
Copyright © 2017-2018 Arm Limited
and labelled as such in comments in the individual source files. All
have been licensed under extremely permissive terms.

The ARM memcpy code (src/string/arm/memcpy.S) is Copyright © 2008
The Android Open Source Project and is licensed under a two-clause BSD
license. It was taken from Bionic libc, used on Android.

The AArch64 memcpy and memset code (src/string/aarch64/*) are
Copyright © 1999-2019, Arm Limited.

The implementation of DES for crypt (src/crypt/crypt_des.c) is
Copyright © 1994 David Burren. It is licensed under a BSD license.

The implementation of blowfish crypt (src/crypt/crypt_blowfish.c) was
originally written by Solar Designer and placed into the public
domain. The code also comes with a fallback permissive license for use
in jurisdictions that may not recognize the public domain.

The smoothsort implementation (src/stdlib/qsort.c) is Copyright © 2011
Lynn Ochs and is licensed under an MIT-style license.

The x86_64 port was written by Nicholas J. Kain and is licensed under
the standard MIT terms.

The mips and microblaze ports were originally written by Richard
Pennington for use in the ellcc project. The original code was adapted
by Rich Felker for build system and code conventions during upstream
integration. It is licensed under the standard MIT terms.

The mips64 port was contributed by Imagination Technologies and is
licensed under the standard MIT terms.

The powerpc port was also originally written by Richard Pennington,
and later supplemented and integrated by John Spencer. It is licensed
under the standard MIT terms.

All other files which have no copyright comments are original works
produced specifically for use as part of this library, written either
by Rich Felker, the main author of the library, or by one or more
contibutors listed above. Details on authorship of individual files
can be found in the git version control history of the project. The
omission of copyright and license comments in each file is in the
interest of source tree size.

In addition, permission is hereby granted for all public header files
(include/* and arch/*/bits/*) and crt files intended to be linked into
applications (crt/*, ldso/dlstart.c, and arch/*/crt_arch.h) to omit
the copyright notice and permission notice otherwise required by the
license, and to use these files without any requirement of
attribution. These files include substantial contributions from:

Bobby Bingham
John Spencer
Nicholas J. Kain
Rich Felker
Richard Pennington
Stefan Kristiansson
Szabolcs Nagy

all of whom have explicitly granted such permission.

This file previously contained text expressing a belief that most of
the files covered by the above exception were sufficiently trivial not
to be subject to copyright, resulting in confusion over whether it
negated the permissions granted in the license. In the spirit of
permissive licensing, and of not having licensing issues being an
obstacle to adoption, that text has been removed.
```

---

## 6. QuickJS-WASI

- **Component:** `quickjs-wasi` 3.6.2, the npm package that provides the
  QuickJS-NG WebAssembly build and the JavaScript glue.
- **How included:** `third_party/pi-go/internal/quickjs/quickjs.wasm` is the
  upstream QuickJS-WASI build, copied byte-for-byte from pi-go at the pinned
  commit. The Go glue under `third_party/pi-go/internal/quickjs/` is a port of
  `quickjs-wasi` 3.6.2 `dist/index.js` and `dist/wasi-shim.js`.
- **License:** MIT.
- **Copyright:** Copyright (c) 2026 Vercel, Inc.
- **Upstream:** <https://github.com/vercel-labs/quickjs-wasi>
- **Package:** <https://www.npmjs.com/package/quickjs-wasi/v/3.6.2>
- **License text:** <https://github.com/vercel-labs/quickjs-wasi/blob/main/LICENSE>

The npm registry metadata for `quickjs-wasi@3.6.2` declares `"license": "MIT"`.
The npm tarball for 3.6.2 contains the same `LICENSE` text, with Copyright
(c) 2026 Vercel, Inc. The upstream `LICENSE` fetched on 2026-11-19 matches.

```text
MIT License

Copyright (c) 2026 Vercel, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

---

## 7. QuickJS-NG

- **Component:** QuickJS-NG 0.17.0, the JavaScript engine compiled into
  `quickjs.wasm` by QuickJS-WASI.
- **License:** MIT.
- **Copyright:**
  - Copyright (c) 2017-2026 Fabrice Bellard
  - Copyright (c) 2017-2024 Charlie Gordon
  - Copyright (c) 2023-2026 Ben Noordhuis
  - Copyright (c) 2023-2026 Saúl Ibarra Corretgé
- **Upstream:** <https://github.com/quickjs-ng/quickjs>
- **License text:** <https://github.com/quickjs-ng/quickjs/blob/v0.17.0/LICENSE>

```text
The MIT License (MIT)

Copyright (c) 2017-2026 Fabrice Bellard
Copyright (c) 2017-2024 Charlie Gordon
Copyright (c) 2023-2026 Ben Noordhuis
Copyright (c) 2023-2026 Saúl Ibarra Corretgé

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

---

## 8. wazero

- **Component:** `github.com/tetratelabs/wazero` v1.12.0, the Go WebAssembly
  runtime used by QuickPy and the vendored pi-go QuickJS runtime.
- **How included:** Go module dependency, not vendored.
- **License:** Apache License 2.0.
- **Copyright:** Copyright 2020-2023 wazero authors.
- **Upstream:** <https://github.com/tetratelabs/wazero>
- **License text:** <https://github.com/tetratelabs/wazero/blob/v1.12.0/LICENSE>
- **NOTICE:** <https://github.com/tetratelabs/wazero/blob/v1.12.0/NOTICE>

```text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright 2020-2023 wazero authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

The wazero `NOTICE` file contains:

```text
wazero
Copyright 2020-2023 wazero authors
```

---

## 9. Go Authors BSD components

- **Components:**
  - `golang.org/x/text` v0.43.0
  - `golang.org/x/sys` v0.48.0
  - the Go standard library and runtime included in the native binary
- **License:** BSD 3-Clause.
- **Copyright:** Copyright 2009 The Go Authors.
- **Upstream:**
  - <https://cs.opensource.google/go/x/text>
  - <https://cs.opensource.google/go/x/sys>
  - <https://go.dev/LICENSE>

`golang.org/x/sys` v0.48.0 is the root module graph selection. QuickPy's own
`go.mod` lists v0.44.0 indirect, but the build uses the root's selected
v0.48.0. This is a module graph note, not a separate license.

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

---

## 10. Build-time tools

The following tools are used to build the committed artifacts but are not
distributed in this repository:

- **Zig 0.17.1** — C toolchain and wasi-libc for the QuickPy guest.
- **binaryen / wasm-opt 133** — WebAssembly optimization.
- **git**, **make**, **python3**, **shasum** — build and verification helpers.

Their own licenses apply to the tools themselves. They are not vendored and
are not part of the native binary. `scripts/rebuild-quickpy.sh` enforces the
exact Zig and wasm-opt versions.

---

## 11. Notes on Prime/RLM and design inspiration

No Prime/RLM code is copied into this repository. The Prime agent's MIT license
is not reproduced here because it does not cover copied code. The pi-go/Prime
design inspired parts of the codemode runtime, but design inspiration alone
does not create a notice requirement.

The vendored `third_party/pi-go` source is a separate upstream project with no
root license at the pinned commit. Do not assign it the Prime agent's license
or any other license without an explicit owner decision.

If you redistribute this repository or a binary built from it:

- add a root `LICENSE` for the original `script-agent` code;
- confirm the licensing decision for the vendored pi-go source and QuickPy;
- re-check the pinned MicroPython, wasi-libc, QuickJS-WASI, QuickJS-NG, wazero,
  x/text, and x/sys revisions;
- keep this notice file and the upstream license texts with the distribution.
