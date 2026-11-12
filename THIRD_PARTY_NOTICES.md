# Third-party notices

MikroDash vendors the assets below so the dashboard works on an isolated network
with no CDN and no outbound requests. Every one is redistributed under the terms
reproduced here.

**Verified 2026-08-27 against each project's own LICENSE file**, not from memory
- the copyright years and licence types below were fetched from upstream rather
than recalled, because a notice that is approximately right is not a notice.

Two things about how this file is meant to be maintained:

- **A licence must be here BEFORE the asset it covers is committed.** Adding one
  afterwards does not cover the commits in between, and at cutover these files
  transplant into the public MikroDash repository as they stand.
- **The copyright line quoted is the one shipped with the VENDORED VERSION**,
  which is not always what upstream's current LICENSE says. Tabler is the worked
  example below.

---

## Fonts - SIL Open Font License 1.1

`web/public/fonts/` (25 families, 98 files) and `web/public/vendor/fonts/`
(JetBrains Mono, Syne, Inter).

**The full notice, including the per-family copyright lines the OFL requires to
accompany the files, is in `web/public/fonts/OFL.txt`** - copied verbatim from
the live repository, where it already states that it covers both directories.
It is not duplicated here; that file is the notice.

---

## Tabler - MIT

`web/public/vendor/tabler.min.css`, Tabler v1.7.4, https://tabler.io

The vendored file is the npm package `@tabler/core@1.7.4`'s own
`dist/css/tabler.min.css` (integrity checked by npm on 2026-11-11), and its
header carries:

    Tabler v1.7.4 (https://tabler.io)
    Copyright 2018-2026 The Tabler Authors
    Copyright 2018-2026 codecalm.net Pawel Kuna

The npm package ships no licence file (its package.json declares MIT), so the
text is upstream's LICENSE at the `@tabler/core@1.7.4` tag, compared word for
word on 2026-11-11:

```
The MIT License (MIT)

Copyright (c) 2018-2026 The Tabler Authors

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

## Chart.js - MIT

`web/public/vendor/chart.umd.min.js`, Chart.js v4.6.1, https://www.chartjs.org

The vendored file is the npm package's own `dist/chart.umd.min.js` (integrity
checked by npm on 2026-11-11), and its header carries:

    Chart.js v4.6.1
    https://www.chartjs.org
    (c) 2026 Chart.js Contributors
    Released under the MIT License

The licence text is the package's LICENSE.md:

```
The MIT License (MIT)

Copyright (c) 2014-2024 Chart.js Contributors

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

---

## Apps Usage logos and the brands they name

The logos under `web/public/vendor/app-icons/` name the apps on the Apps Usage
page. **Each is a trademark of its owner**, shown only to identify that owner's
service; none implies endorsement. Their artwork comes from the five sets below,
each under its own licence, chosen per app in `testdata/apps/catalogue.json` and
written by `cmd/appgen` at the pinned versions recorded there. A Simple Icons
logo that carries a licence of its own (rather than the project's CC0) is
refused by the generator.

---

## domain-list-community - MIT

`internal/apps/catalogue_gen.json`: which domains belong to which app, expanded
from v2fly/domain-list-community at commit
`2b7ae3222f79e58e323a6b1c30c6618821068963`,
https://github.com/v2fly/domain-list-community

```
MIT License

Copyright (c) 2018-2019 V2Ray

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

## Simple Icons - CC0-1.0

Most of the Apps Usage logos, and the brand colours, from the npm package
`simple-icons` 16.37.0 (integrity checked by npm), https://simpleicons.org.
Released under CC0 1.0 Universal: no rights reserved. Simple Icons' own
disclaimer applies: the icons depict trademarks, see the section above.

---

## SVG Logos - CC0-1.0

The full-colour logos (Microsoft Teams, Windows, OneDrive, LinkedIn, Slack,
OpenAI, Adobe, AWS, Azure, Bing, Microsoft, Copilot), from gilbarbara/logos at
commit `b9edc65f8946027cb5e2fe6c772e914cf3649fba`,
https://github.com/gilbarbara/logos. Released under CC0 1.0 Universal.

---

## CoreUI Brands - CC0-1.0

Xbox, Amazon, Hulu, Nintendo, Canva, Yahoo, GOG and Tencent, from the npm
package `@iconify-json/cib` 1.2.3 (CoreUI Brands by creativeLabs Lukasz
Holeczek, https://github.com/coreui/coreui-icons). Released under CC0 1.0
Universal.

---

## Arcticons - CC BY-SA 4.0

Prime Video, Disney+, Showmax, DStv, Outlook, Microsoft 365, Temu, Shein,
Takealot, Speedtest and Webex, from the npm package `@iconify-json/arcticons`
1.2.60. **Arcticons by Donnnno and the Arcticons contributors**,
https://github.com/Arcticons-Team/Arcticons, licensed under Creative Commons
Attribution-ShareAlike 4.0 International,
https://creativecommons.org/licenses/by-sa/4.0/. Changes: each icon is taken
from Iconify's packaging and written as a standalone SVG; MikroDash fills it with
the app's brand colour when drawing it. These eleven files remain under CC BY-SA
4.0, whatever licence the rest of MikroDash is under.

---

## Lucide - ISC

The generic icons for protocols (SSH, DNS, NTP, Email, IPsec, Remote Desktop),
from the npm package `@iconify-json/lucide` 1.2.154,
https://github.com/lucide-icons/lucide:

```
ISC License

Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

---

The following Lucide icons are derived from the Feather project:

airplay, alert-circle, alert-octagon, alert-triangle, aperture, arrow-down-circle, arrow-down-left, arrow-down-right, arrow-down, arrow-left-circle, arrow-left, arrow-right-circle, arrow-right, arrow-up-circle, arrow-up-left, arrow-up-right, arrow-up, at-sign, calendar, cast, check, chevron-down, chevron-left, chevron-right, chevron-up, chevrons-down, chevrons-left, chevrons-right, chevrons-up, circle, clipboard, clock, code, columns, command, compass, corner-down-left, corner-down-right, corner-left-down, corner-left-up, corner-right-down, corner-right-up, corner-up-left, corner-up-right, crosshair, database, divide-circle, divide-square, dollar-sign, download, external-link, feather, frown, hash, headphones, help-circle, info, italic, key, layout, life-buoy, link-2, link, loader, lock, log-in, log-out, maximize, meh, minimize, minimize-2, minus-circle, minus-square, minus, monitor, moon, more-horizontal, more-vertical, move, music, navigation-2, navigation, octagon, pause-circle, percent, plus-circle, plus-square, plus, power, radio, rss, search, server, share, shopping-bag, sidebar, smartphone, smile, square, table-2, tablet, target, terminal, trash-2, trash, triangle, tv, type, upload, x-circle, x-octagon, x-square, x, zoom-in, zoom-out

The MIT License (MIT) (for the icons listed above)

Copyright (c) 2013-present Cole Bemis

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

---

## topojson-client - NOT VENDORED

`/vendor/topojson-client.min.js` is loaded by the live app and **is deliberately
not shipped here.** `web/src/pages/connections-worldmap.ts` reimplements the one
function the map needs - a thirty-line arc-delta decode - and is verified against
the live output rather than trusted.

It was copied in during the vendoring pass on 2026-08-27 and removed again the
same hour, once checking showed nothing loads it. Recorded rather than silently
dropped, for two reasons: **a licence notice for something not redistributed is
as wrong as a missing one**, and the next person to compare the two vendor trees
will find a file here that is absent and should know it is a decision.

The DATA it decodes - `world-atlas` below - is still vendored and still needs its
notice.

---

## world-atlas - ISC

`web/public/vendor/world-atlas/countries-110m.json`,
https://github.com/topojson/world-atlas

Same licence and holder as topojson-client above:

    Copyright 2013-2019 Michael Bostock

    Permission to use, copy, modify, and/or distribute this software for any
    purpose with or without fee is hereby granted, provided that the above
    copyright notice and this permission notice appear in all copies.

    THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
    WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
    MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY
    SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
    WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION
    OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN
    CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

**The underlying geography is Natural Earth data, which is in the public
domain** (https://www.naturalearthdata.com/about/terms-of-use/). The ISC licence
above covers the TopoJSON packaging of it.

---

## DB-IP City Lite - CC BY 4.0

**IP Geolocation by [DB-IP](https://db-ip.com)**, used under the
[Creative Commons Attribution 4.0 International Licence](https://creativecommons.org/licenses/by/4.0/).

Ships as `dbip-city-lite.mmdb` and answers country, region, city and
coordinates. Read by `internal/geo`.

## DB-IP ASN Lite - CC BY 4.0

**IP Geolocation by [DB-IP](https://db-ip.com)**, used under the
[Creative Commons Attribution 4.0 International Licence](https://creativecommons.org/licenses/by/4.0/).

Ships as `dbip-asn-lite.mmdb` and answers which organisation owns an address.
Read by `internal/asn`.

### Why the credit is repeated

The two databases come from one vendor under one licence and were once one
section here. They are separate now because the attribution is a LICENCE
CONDITION rather than a courtesy: CC BY 4.0 requires crediting the source, and
the credit has to travel with anything that ships the data. A section that
covers two files credits neither of them on its own, and either file can be
shipped without the other - the Dockerfile fetches them independently, and they
can be from different months.

`internal/verify/geodata_test.go` pins the credit, the link and BOTH database
filenames - crediting one while quietly adding a second beside it is how the ASN
file arrived uncredited.

Both databases are fetched fresh by the `geodata` stage of the `Dockerfile` - no
account, no licence key. MaxMind's GeoLite2 is better data and updates more
often, but it needs an account, a key and a signed EULA, which would make a
build nobody who clones this repo can run; IPinfo and IP2Location were checked
in September 2026 and both require an account too.

City Lite replaced geoip-lite's bundled `.dat` files on
2026-08-30, because those shipped whatever geoip-lite 2.0.3 published and nothing
in that project's build ever refreshed them: the addresses moved and the file did
not.

`cities.json`, the gazetteer behind the city picker, is DERIVED from the same
database by `cmd/geogen` and is therefore covered by the same licence and the
same attribution.

MaxMind GeoLite2 was considered and rejected. It is more accurate and updates
twice weekly, but requires an account, a signed EULA and a licence key that
expires every 90 days - which would make this a build that nobody without
credentials could run.

---

## go-routeros - MIT

`third_party/go-routeros/`, github.com/go-routeros/routeros v3.0.1,
https://github.com/go-routeros/routeros. Its non-test sources, used through a
`replace` in `go.mod`, with one fix described in `third_party/go-routeros/PATCHES.md`.

The licence shipped with v3.0.1 is kept beside the sources as
`third_party/go-routeros/LICENSE`, and carries:

    The MIT License (MIT)

    Copyright (c) 2016 André Luiz dos Santos

---

## Go modules linked into the binary

Every module the linker puts into `cmd/mikrodash`, with the licence read from
that module's **own LICENSE file in the module cache on 2026-09-25**, not from
memory. Each BSD entry was checked for the third clause ("may be used to endorse
or promote"): BSD-2-Clause and BSD-3-Clause are different licences and naming
the wrong one is a false statement about somebody else's terms.

The sections above describe what is vendored into the repository and shipped to
the browser. This table is the other half, and it was missing until now: the
eight direct dependencies were documented and the modules they pull in were not.

| Module | Licence |
|---|---|
| github.com/coder/websocket | ISC |
| github.com/dustin/go-humanize | MIT |
| github.com/go-pdf/fpdf | MIT |
| github.com/go-routeros/routeros/v3 | MIT |
| github.com/google/btree | Apache-2.0 |
| github.com/google/uuid | BSD-3-Clause |
| github.com/oschwald/maxminddb-golang/v2 | ISC |
| github.com/remyoudompheng/bigfft | BSD-3-Clause |
| golang.org/x/crypto | BSD-3-Clause |
| golang.org/x/net | BSD-3-Clause |
| golang.org/x/sys | BSD-3-Clause |
| golang.org/x/time | BSD-3-Clause |
| golang.zx2c4.com/wireguard | MIT |
| gvisor.dev/gvisor | Apache-2.0 |
| modernc.org/libc | BSD-3-Clause |
| modernc.org/mathutil | BSD-3-Clause |
| modernc.org/memory | BSD-3-Clause |
| modernc.org/sqlite | BSD-3-Clause |

**This list is not maintained by hand.** `internal/server/deps.go` holds the same
table, joined to the module list the binary reports about itself, and
`TestEveryLinkedModuleHasARecordedLicence` fails in BOTH directions: a module
with no licence recorded, and a licence recorded for a module no longer linked.
Add a dependency and the check names it; remove one and the check names the
entry left behind.

---

## Not third-party

For the avoidance of doubt, these are MikroDash's own and are not covered above:
`web/public/css/*`, `web/public/logo.png`, `preflight.js`, and the login page.
