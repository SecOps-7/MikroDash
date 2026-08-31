// The Connections page's reference tables and map geometry.
//
// ── THE TABLES ARE LIFTED, NOT RETYPED ──────────────────────────────────────
//
// `PORT_NAMES`, `CC_NAMES` and `CC_CENTROIDS` are 75 lines of pure data — every
// country's name and a hand-picked centroid for each. They were copied out of
// public/app.js by a script rather than by hand, because a mistyped centroid
// draws an arc to the wrong country and nothing would ever fail to tell you.
//
// The projection is equirectangular and deliberately so: the map is a 1000x500
// picture used to show WHERE, not to measure anything, and a projection with a
// name would add a dependency to draw the same rectangle.

  export const PORT_NAMES: Record<string, string> = {'80':'HTTP','443':'HTTPS','53':'DNS','22':'SSH','21':'FTP',
    '25':'SMTP','587':'SMTP','993':'IMAP','995':'POP3','3389':'RDP','1194':'OpenVPN',
    '51820':'WireGuard','8080':'HTTP-alt','8443':'HTTPS-alt','123':'NTP','67':'DHCP',
    '110':'POP3','143':'IMAP','5353':'mDNS','1900':'UPnP'};

  export const CC_NAMES: Record<string, string> = {
    AF:'Afghanistan',AL:'Albania',DZ:'Algeria',AO:'Angola',AR:'Argentina',AU:'Australia',
    AT:'Austria',BD:'Bangladesh',BE:'Belgium',BO:'Bolivia',BR:'Brazil',BG:'Bulgaria',
    MM:'Myanmar',KH:'Cambodia',CM:'Cameroon',CA:'Canada',LK:'Sri Lanka',CL:'Chile',
    CN:'China',CO:'Colombia',CD:'DR Congo',CR:'Costa Rica',HR:'Croatia',CU:'Cuba',
    CY:'Cyprus',CZ:'Czechia',DK:'Denmark',DO:'Dominican Rep.',EC:'Ecuador',EG:'Egypt',
    SV:'El Salvador',ET:'Ethiopia',FI:'Finland',FR:'France',GA:'Gabon',DE:'Germany',
    GH:'Ghana',GR:'Greece',GT:'Guatemala',HT:'Haiti',HN:'Honduras',HU:'Hungary',
    IN:'India',ID:'Indonesia',IR:'Iran',IQ:'Iraq',IE:'Ireland',IL:'Israel',IT:'Italy',
    JM:'Jamaica',JP:'Japan',JO:'Jordan',KE:'Kenya',KP:'North Korea',KR:'South Korea',
    KW:'Kuwait',LA:'Laos',LB:'Lebanon',LR:'Liberia',LY:'Libya',LU:'Luxembourg',
    MX:'Mexico',MA:'Morocco',MZ:'Mozambique',NA:'Namibia',NP:'Nepal',NL:'Netherlands',
    NZ:'New Zealand',NI:'Nicaragua',NG:'Nigeria',NO:'Norway',PK:'Pakistan',PA:'Panama',
    PG:'Papua New Guinea',PE:'Peru',PH:'Philippines',PL:'Poland',PT:'Portugal',
    QA:'Qatar',RO:'Romania',RU:'Russia',SA:'Saudi Arabia',SN:'Senegal',SO:'Somalia',
    ZA:'South Africa',ES:'Spain',SD:'Sudan',SE:'Sweden',CH:'Switzerland',SY:'Syria',
    TH:'Thailand',TR:'Turkey',UG:'Uganda',UA:'Ukraine',AE:'UAE',GB:'United Kingdom',
    US:'United States',UY:'Uruguay',VE:'Venezuela',VN:'Vietnam',YE:'Yemen',
    ZM:'Zambia',ZW:'Zimbabwe',BA:'Bosnia',RS:'Serbia',BY:'Belarus',GE:'Georgia',
    KZ:'Kazakhstan',MN:'Mongolia',TJ:'Tajikistan',TM:'Turkmenistan',UZ:'Uzbekistan',
    AZ:'Azerbaijan',AM:'Armenia',MD:'Moldova',KG:'Kyrgyzstan',MK:'N. Macedonia',
    ME:'Montenegro',NC:'New Caledonia',PR:'Puerto Rico',TZ:'Tanzania',MG:'Madagascar',
    CI:'Ivory Coast',ML:'Mali',BF:'Burkina Faso',NE:'Niger',TD:'Chad',
    SS:'South Sudan',CF:'Central African Rep.',GN:'Guinea',ZR:'DR Congo',
    RW:'Rwanda',BI:'Burundi',MW:'Malawi',ZI:'Zimbabwe',MR:'Mauritania',
    GM:'Gambia',GW:'Guinea-Bissau',SL:'Sierra Leone',GQ:'Eq. Guinea',
    TG:'Togo',BJ:'Benin',DJ:'Djibouti',ER:'Eritrea',KM:'Comoros',
    SC:'Seychelles',MU:'Mauritius',SZ:'Eswatini',LS:'Lesotho',BW:'Botswana',
    ZB:'Zambia',TN:'Tunisia',PS:'Palestine',OM:'Oman',
    YU:'Yugoslavia',SK:'Slovakia',SI:'Slovenia',EE:'Estonia',LV:'Latvia',
    LT:'Lithuania',FO:'Faroe Islands',IS:'Iceland',MT:'Malta',
    XK:'Kosovo',LI:'Liechtenstein',MC:'Monaco',SM:'San Marino',
    VA:'Vatican',AD:'Andorra',GI:'Gibraltar',JE:'Jersey',GG:'Guernsey',IM:'Isle of Man',
    HK:'Hong Kong',MO:'Macau',TW:'Taiwan',SG:'Singapore',BN:'Brunei',
    TL:'Timor-Leste',MV:'Maldives',PW:'Palau',
    FM:'Micronesia',MH:'Marshall Islands',NR:'Nauru',TV:'Tuvalu',TO:'Tonga',
    WS:'Samoa',FJ:'Fiji',VU:'Vanuatu',SB:'Solomon Islands',KI:'Kiribati',
    PF:'French Polynesia',GU:'Guam',AS:'American Samoa',CK:'Cook Islands',
    NF:'Norfolk Island',CC:'Cocos Islands',CX:'Christmas Island',
    BB:'Barbados',LC:'St. Lucia',VC:'St. Vincent',GD:'Grenada',
    AG:'Antigua',KN:'St. Kitts',DM:'Dominica',TT:'Trinidad',
    BS:'Bahamas',TC:'Turks & Caicos',KY:'Cayman Islands',VG:'British Virgin Islands',
    VI:'US Virgin Islands',AW:'Aruba',CW:'Curacao',BQ:'Bonaire',SX:'Sint Maarten',
    BZ:'Belize',GY:'Guyana',SR:'Suriname',GF:'French Guiana',
    PY:'Paraguay',FK:'Falkland Islands',GL:'Greenland',PM:'St. Pierre',
    MF:'St. Martin',BL:'St. Barthélemy',GP:'Guadeloupe',MQ:'Martinique',RE:'Réunion',
    YT:'Mayotte',TF:'French S. Territories',CG:'Republic of Congo',
    ST:'São Tomé',CV:'Cape Verde',EH:'W. Sahara'
  };

  export const CC_CENTROIDS: Record<string, [number, number]> = {AF:[67.7,33.9],AL:[20.2,41.2],DZ:[2.6,28.0],AO:[17.9,-11.2],
    AR:[-63.6,-38.4],AU:[133.8,-25.3],AT:[14.6,47.7],BD:[90.4,23.7],BE:[4.5,50.5],
    BO:[-64.7,-17.0],BR:[-51.9,-14.2],BG:[25.5,42.7],MM:[96.7,16.9],KH:[104.9,12.6],
    CM:[12.4,5.7],CA:[-96.8,56.1],LK:[80.8,7.9],CL:[-71.5,-35.7],CN:[104.2,35.9],
    CO:[-74.3,4.6],CD:[23.7,-2.9],CR:[-84.2,9.7],HR:[16.4,45.1],CU:[-79.5,21.5],
    CY:[33.4,35.1],CZ:[15.5,49.8],DK:[9.5,56.3],DO:[-70.2,18.7],EC:[-78.1,-1.8],
    EG:[30.8,26.8],SV:[-88.9,13.8],ET:[40.5,9.1],FI:[26.3,64.0],FR:[2.2,46.2],
    GA:[11.6,-0.8],DE:[10.5,51.2],GH:[-1.0,7.9],GR:[21.8,39.1],GT:[-90.2,15.8],
    HT:[-73.0,18.9],HN:[-86.2,15.2],HU:[19.5,47.2],IN:[78.7,20.6],ID:[113.9,-0.8],
    IR:[53.7,32.4],IQ:[43.7,33.2],IE:[-8.2,53.4],IL:[34.9,31.5],IT:[12.6,42.8],
    JM:[-77.3,18.1],JP:[138.3,36.2],JO:[36.2,31.2],KE:[37.9,0.0],KP:[127.5,40.3],
    KR:[127.8,35.9],KW:[47.5,29.3],LA:[102.5,17.9],LB:[35.9,33.9],LR:[-9.4,6.4],
    LY:[17.2,26.3],LU:[6.1,49.8],MX:[-102.6,23.6],MA:[-7.1,31.8],MZ:[35.5,-18.7],
    NA:[18.5,-22.3],NP:[84.1,28.4],NL:[5.3,52.1],NZ:[172.8,-41.5],NI:[-85.0,12.9],
    NG:[8.7,9.1],NO:[8.5,60.5],PK:[69.3,30.4],PA:[-80.1,8.5],PG:[143.9,-6.3],
    PE:[-75.0,-9.2],PH:[122.9,12.9],PL:[19.1,52.1],PT:[-8.2,39.6],QA:[51.2,25.4],
    RO:[24.9,45.9],RU:[99.0,61.5],SA:[44.5,24.0],SN:[-14.5,14.5],SO:[46.2,5.2],
    ZA:[25.1,-29.0],ES:[-3.7,40.2],SD:[29.9,12.9],SE:[18.6,60.1],CH:[8.2,46.8],
    SY:[38.0,35.0],TH:[101.0,15.9],TR:[35.2,39.1],UG:[32.3,1.4],UA:[31.2,48.4],
    AE:[53.8,23.4],GB:[-3.4,55.4],US:[-100.4,37.1],UY:[-55.8,-32.5],VE:[-66.6,6.4],
    VN:[108.3,14.1],YE:[47.6,15.6],ZM:[27.8,-13.1],ZW:[29.9,-19.0],BA:[17.2,44.2],
    RS:[21.0,44.0],BY:[28.0,53.5],GE:[43.4,42.3],KZ:[66.9,48.0],MN:[103.8,46.9]};

/**
 * TopoJSON country ids to ISO-3166 alpha-2.
 *
 * The world atlas identifies countries by their NUMERIC code, and everything
 * else on this page — the geo lookup, the flags, the names — speaks alpha-2.
 * Lifted with the other tables, and gated with them: a wrong pair here colours
 * the wrong country and nothing else would notice.
 */
  export const NUM_TO_ISO2: Record<number, string> = {4:'AF',8:'AL',12:'DZ',24:'AO',32:'AR',36:'AU',40:'AT',50:'BD',
    56:'BE',64:'BT',68:'BO',76:'BR',100:'BG',104:'MM',116:'KH',120:'CM',124:'CA',
    144:'LK',152:'CL',156:'CN',170:'CO',180:'CD',188:'CR',191:'HR',192:'CU',196:'CY',
    203:'CZ',204:'BJ',208:'DK',214:'DO',218:'EC',818:'EG',222:'SV',231:'ET',246:'FI',
    250:'FR',266:'GA',276:'DE',288:'GH',300:'GR',320:'GT',332:'HT',340:'HN',348:'HU',
    356:'IN',360:'ID',364:'IR',368:'IQ',372:'IE',376:'IL',380:'IT',388:'JM',392:'JP',
    400:'JO',404:'KE',408:'KP',410:'KR',414:'KW',418:'LA',422:'LB',430:'LR',434:'LY',
    442:'LU',484:'MX',504:'MA',508:'MZ',516:'NA',524:'NP',528:'NL',540:'NC',554:'NZ',
    558:'NI',566:'NG',578:'NO',586:'PK',591:'PA',598:'PG',604:'PE',608:'PH',616:'PL',
    620:'PT',630:'PR',634:'QA',642:'RO',643:'RU',682:'SA',686:'SN',694:'SL',706:'SO',
    710:'ZA',724:'ES',729:'SD',752:'SE',756:'CH',760:'SY',762:'TJ',764:'TH',792:'TR',
    800:'UG',804:'UA',784:'AE',826:'GB',840:'US',858:'UY',860:'UZ',862:'VE',704:'VN',
    887:'YE',894:'ZM',716:'ZW',70:'BA',807:'MK',499:'ME',688:'RS',51:'AM',31:'AZ',
    112:'BY',268:'GE',398:'KZ',417:'KG',498:'MD',496:'MN',795:'TM'};

/** A regional-indicator flag from an ISO-3166 alpha-2 code. */
export function iso2Flag(cc: string): string {
  if (!cc || cc.length !== 2) return '';
  return cc.split('').map((c) =>
    String.fromCodePoint(0x1F1E6 - 65 + c.toUpperCase().charCodeAt(0))).join('');
}

/** The map's pixel size. Every geometry here is in these units. */
export const MAP_W = 1000;
export const MAP_H = 500;

/** Equirectangular: longitude straight to x, latitude straight to y. */
export function project(lon: number, lat: number): [number, number] {
  return [(lon + 180) * (MAP_W / 360), (90 - lat) * (MAP_H / 180)];
}

/**
 * A country outline as an SVG path.
 *
 * THE ANTIMERIDIAN CHECK IS THE WHOLE TRICK. A ring that crosses 180 degrees
 * jumps from one edge of the map to the other, and drawing a line between those
 * two points streaks a horizontal band across the world — Russia and Fiji both
 * do it. A jump of more than 180 degrees of longitude is a wrap rather than a
 * move, so the path lifts the pen instead.
 */
export function coordsToD(coords: number[][][]): string {
  return coords.map((ring) => {
    let d = '';
    for (let i = 0; i < ring.length; i++) {
      const p = project(ring[i]![0]!, ring[i]![1]!);
      if (i === 0) {
        d += 'M' + p[0].toFixed(1) + ',' + p[1].toFixed(1);
      } else {
        const dlon = Math.abs(ring[i]![0]! - ring[i - 1]![0]!);
        d += (dlon > 180 ? 'M' : ' L') + p[0].toFixed(1) + ',' + p[1].toFixed(1);
      }
    }
    return d + 'Z';
  }).join(' ');
}

/**
 * The arc between two points on the map.
 *
 * It always arches UPWARD, whichever way the traffic goes: two arcs between the
 * same pair would otherwise overlap exactly, and a map where every link is drawn
 * twice in the same place is a map with half its links invisible. The rise grows
 * with distance so a short hop is a shallow curve rather than a tall spike.
 */
export function makeArcD(x1: number, y1: number, x2: number, y2: number): string {
  const dx = x2 - x1, dy = y2 - y1;
  const dist = Math.sqrt(dx * dx + dy * dy);
  const cx = (x1 + x2) / 2, cy = (y1 + y2) / 2;
  const rise = Math.max(40, dist * 0.35);
  let nx = -dy / dist, ny = dx / dist; // perpendicular unit
  if (ny > 0) { nx = -nx; ny = -ny; }  // negative y is up in SVG
  return 'M' + x1.toFixed(1) + ',' + y1.toFixed(1) +
    ' Q' + (cx + nx * rise).toFixed(1) + ',' + (cy + ny * rise).toFixed(1) +
    ' ' + x2.toFixed(1) + ',' + y2.toFixed(1);
}

/**
 * A country's centre on the map.
 *
 * The hand-picked centroid wins where there is one: a geometric centre puts
 * Norway's label in the sea and the United States' in the Pacific, because both
 * have territory far from the mass anyone means. Everything else falls back to
 * the average of its coordinates, which is close enough for a label.
 */
export function centroidOf(cc: string, rings: number[][][]): [number, number] | null {
  const named = CC_CENTROIDS[cc];
  if (named) return project(named[0], named[1]);
  let lon = 0, lat = 0, n = 0;
  for (const ring of rings) {
    for (const p of ring) { lon += p[0]!; lat += p[1]!; n++; }
  }
  if (!n) return null;
  return project(lon / n, lat / n);
}
