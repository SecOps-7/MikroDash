# SwOS `.b` captures

What a MikroTik switch running SwOS answered on its web API, replayed by
`internal/swos` tests. Nobody on this project owns a SwOS switch; every file
here was captured by somebody else and is used under its MIT licence (see
THIRD_PARTY_NOTICES.md).

| Folder | Device and firmware | Source | Real capture? |
|---|---|---|---|
| `lite-css610-2.21/` | CSS610-8P-2S+, SwOS Lite 2.21 | AliKarami/mikromcp `test/fixtures/swos/` at commit `153c32a520ebd4f67772d6e791a558b401938643` | Yes, byte-identical apart from the sanitising below. `!stats.b.sample2` is an EARLIER capture of `!stats.b`, so every counter in `!stats.b` is greater than or equal to it. `vlan.b.sample2` is from another switch, and one of its three rows was written by hand in the wire format. |
| `swos-css318-2.19/` | CSS318-16G-2S+, SwOS 2.19 | AliKarami/mikromcp `test/fixtures/swos-full-real/` at the same commit | Yes, sanitised the same way. No statistics capture exists for full SwOS; that is the one gap. |
| `pyswitchos-css610-2.21/` | CSS610-8G-2S+, SwOS Lite 2.21 | probert94/python-switchos `tests/fixtures/` at commit `7101c0a65258932f7150bcc5ef8ea11d5e2f32ff` | Partly. `link_b_response_1.txt` is a compact capture with zero-padded name strings. `link_b_response_2_pretty.txt` is the same endpoint PRETTY-PRINTED (newlines and spaces), so it cannot be compared byte for byte. `sys_b_mock_response_1.txt` is a MOCK ("MockSwitch", version "2.16"), useful only for its padded strings. |

## Sanitising (done upstream, before the files reached this repository)

mikromcp's own README, condensed: MAC addresses became `02:00:00:00:00:NN`
(locally administered) consistently across files, serials became `SWTEST...`
and `SFPTESTSN...`, port names became role names, the CSS318's IPs became
`192.168.88.x`, its identity `MikroTik`, its SNMP community `public`, and
VLAN IDs in `vlan.b.sample2` became 10, 20 and 30. Everything else, key order
and value widths included, is as the switch sent it.

## Why the bytes matter

The codec is tested by decoding each file and encoding it again, byte for
byte. SwOS writes are whole-blob POSTs, so a codec that changed a value's
width could not be trusted to write later, even though this version never
writes.
