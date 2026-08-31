# Third-party notices

MikroDash vendors the assets below so the dashboard works on an isolated network
with no CDN and no outbound requests. Every one is redistributed under the terms
reproduced here.

**Verified 2026-08-27 against each project's own LICENSE file**, not from memory
— the copyright years and licence types below were fetched from upstream rather
than recalled, because a notice that is approximately right is not a notice.

Two things about how this file is meant to be maintained:

- **A licence must be here BEFORE the asset it covers is committed.** Adding one
  afterwards does not cover the commits in between, and at cutover these files
  transplant into the public MikroDash repository as they stand.
- **The copyright line quoted is the one shipped with the VENDORED VERSION**,
  which is not always what upstream's current LICENSE says. Tabler is the worked
  example below.

---

## Fonts — SIL Open Font License 1.1

`web/public/fonts/` (25 families, 98 files) and `web/public/vendor/fonts/`
(JetBrains Mono, Syne, Inter).

**The full notice, including the per-family copyright lines the OFL requires to
accompany the files, is in `web/public/fonts/OFL.txt`** — copied verbatim from
the live repository, where it already states that it covers both directories.
It is not duplicated here; that file is the notice.

---

## Tabler — MIT

`web/public/vendor/tabler.min.css`, Tabler v1.4.0, https://tabler.io

The vendored file's own header carries:

    Tabler v1.4.0 (https://tabler.io)
    Copyright 2018-2025 The Tabler Authors
    Copyright 2018-2025 codecalm.net Pawel Kuna

**Upstream's current LICENSE says "2018-2026"**, because it has moved on since
v1.4.0 was vendored. The range above is the one shipped with the file actually
redistributed here, which is the one that applies to it. The licence text is
otherwise unchanged:

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

## Chart.js — MIT

`web/public/vendor/chart.umd.min.js`, Chart.js v4.4.2, https://www.chartjs.org

The vendored file carries only a jsDelivr build banner and no copyright line, so
this was taken from the project's LICENSE.md:

```
The MIT License (MIT)

Copyright (c) 2014-2024 Chart.js Contributors

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

---

## topojson-client — NOT VENDORED

`/vendor/topojson-client.min.js` is loaded by the live app and **is deliberately
not shipped here.** `web/src/pages/connections-worldmap.ts` reimplements the one
function the map needs — a thirty-line arc-delta decode — and is verified against
the live output rather than trusted.

It was copied in during the vendoring pass on 2026-08-27 and removed again the
same hour, once checking showed nothing loads it. Recorded rather than silently
dropped, for two reasons: **a licence notice for something not redistributed is
as wrong as a missing one**, and the next person to compare the two vendor trees
will find a file here that is absent and should know it is a decision.

The DATA it decodes — `world-atlas` below — is still vendored and still needs its
notice.

---

## world-atlas — ISC

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

## IP geolocation data — DB-IP City Lite

**IP Geolocation by [DB-IP](https://db-ip.com)**, used under the
[Creative Commons Attribution 4.0 International Licence](https://creativecommons.org/licenses/by/4.0/).

The attribution above is a LICENCE CONDITION, not a courtesy: CC BY 4.0 requires
crediting the source, and the credit has to travel with anything that ships the
data. `dbip-city-lite.mmdb` is baked into the image, so this file is where that
credit lives.

The database is fetched fresh by the `geodata` stage of the `Dockerfile` — no
account, no licence key. It replaced geoip-lite's bundled `.dat` files on
2026-08-30, because those shipped whatever geoip-lite 2.0.3 published and nothing
in that project's build ever refreshed them: the addresses moved and the file did
not.

`cities.json`, the gazetteer behind the city picker, is DERIVED from the same
database by `cmd/geogen` and is therefore covered by the same licence and the
same attribution.

MaxMind GeoLite2 was considered and rejected. It is more accurate and updates
twice weekly, but requires an account, a signed EULA and a licence key that
expires every 90 days — which would make this a build that nobody without
credentials could run.

---

## Not third-party

For the avoidance of doubt, these are MikroDash's own and are not covered above:
`web/public/css/*`, `web/public/logo.png`, `preflight.js`, and the login page.
