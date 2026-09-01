// GENERATED from testdata/appearance-tables.json — do not edit.
// Regenerate with `node tools/appearance-tables.js` then `node tools/appearance-tables-ts.js`.

/** r, g, b, a — the alpha is carried through brightness scaling unchanged. */
export type RGBA = [number, number, number, number];

export interface PaletteColors { main: RGBA; muted: RGBA; bgDeep: RGBA; bgCard: RGBA }

/** The neutral midpoint of all three sliders. At this level the layer REMOVES
 *  its custom properties rather than computing the base colour, so the
 *  stylesheet's own value is what applies. */
export const APPEAR_DEFAULT = 8;

/** Where each preference is remembered. Appearance is per-browser, not per-user:
 *  the server never sees any of it. */
export const KEYS = {
  "theme": "mikrodash_theme",
  "palette": "mikrodash_palette",
  "contrast": "mikrodash_contrast",
  "textBright": "mikrodash_text_bright",
  "bgBright": "mikrodash_bg_bright",
  "font": "mikrodash_font",
  "fontSize": "mikrodash_font_size"
} as const;

/** Order is load-bearing: the `<select>` renders the labels in this order and
 *  The appearance table generator pins the two against each other. */
export const FONTS: { id: string; family: string }[] = [
  {
    "id": "system",
    "family": "system-ui,-apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif"
  },
  {
    "id": "syne",
    "family": "'Syne',sans-serif"
  },
  {
    "id": "geist",
    "family": "'Geist',sans-serif"
  },
  {
    "id": "inter",
    "family": "'Inter',sans-serif"
  },
  {
    "id": "plus-jakarta",
    "family": "'Plus Jakarta Sans',sans-serif"
  },
  {
    "id": "dm-sans",
    "family": "'DM Sans',sans-serif"
  },
  {
    "id": "outfit",
    "family": "'Outfit',sans-serif"
  },
  {
    "id": "space-grotesk",
    "family": "'Space Grotesk',sans-serif"
  },
  {
    "id": "sofia-sans",
    "family": "'Sofia Sans',sans-serif"
  },
  {
    "id": "nunito",
    "family": "'Nunito',sans-serif"
  },
  {
    "id": "poppins",
    "family": "'Poppins',sans-serif"
  },
  {
    "id": "montserrat",
    "family": "'Montserrat',sans-serif"
  },
  {
    "id": "raleway",
    "family": "'Raleway',sans-serif"
  },
  {
    "id": "manrope",
    "family": "'Manrope',sans-serif"
  },
  {
    "id": "roboto",
    "family": "'Roboto',sans-serif"
  },
  {
    "id": "open-sans",
    "family": "'Open Sans',sans-serif"
  },
  {
    "id": "lato",
    "family": "'Lato',sans-serif"
  },
  {
    "id": "source-sans",
    "family": "'Source Sans 3',sans-serif"
  },
  {
    "id": "work-sans",
    "family": "'Work Sans',sans-serif"
  },
  {
    "id": "fira-sans",
    "family": "'Fira Sans',sans-serif"
  },
  {
    "id": "jetbrains-mono",
    "family": "'JetBrains Mono',monospace"
  },
  {
    "id": "fira-code",
    "family": "'Fira Code',monospace"
  },
  {
    "id": "quicksand",
    "family": "'Quicksand',sans-serif"
  },
  {
    "id": "comfortaa",
    "family": "'Comfortaa',sans-serif"
  },
  {
    "id": "ibm-plex-sans",
    "family": "'IBM Plex Sans',sans-serif"
  },
  {
    "id": "oxanium",
    "family": "'Oxanium',sans-serif"
  },
  {
    "id": "orbitron",
    "family": "'Orbitron',sans-serif"
  }
];

/** `px: null` is the browser default — the layer REMOVES font-size rather than
 *  setting a number, which is not the same thing as 16px. */
export const FONT_SIZES: { id: string; px: number | null }[] = [
  {
    "id": "xs",
    "px": 12
  },
  {
    "id": "sm",
    "px": 14
  },
  {
    "id": "normal",
    "px": null
  },
  {
    "id": "md",
    "px": 18
  },
  {
    "id": "lg",
    "px": 20
  },
  {
    "id": "xl",
    "px": 22
  }
];

/** Indexed by `level - 1`, clamped at both ends. */
export const CONTRAST_FACTORS: number[] = [0.15,0.25,0.35,0.5,0.65,0.8,0.92,1,1.2,1.5,2,2.75,3.5,4.5,6];
export const TEXT_BRIGHT_FACTORS: number[] = [0.2,0.3,0.42,0.55,0.65,0.78,0.9,1,1.05,1.1,1.17,1.25,1.33,1.42,1.5];
export const BG_BRIGHT_FACTORS: number[] = [0.2,0.3,0.42,0.55,0.65,0.78,0.9,1,1.05,1.1,1.17,1.25,1.33,1.42,1.5];

/** Keyed `palette:scheme`. A miss falls back to `default:dark` SILENTLY, which
 *  is why the generator checks every key against the swatch that offers it. */
export const PALETTE_COLORS: Record<string, PaletteColors> = {
  "default:dark": {
    "main": [
      200,
      215,
      240,
      0.9
    ],
    "muted": [
      148,
      163,
      190,
      0.55
    ],
    "bgDeep": [
      7,
      9,
      15,
      1
    ],
    "bgCard": [
      13,
      18,
      30,
      0.85
    ]
  },
  "default:light": {
    "main": [
      26,
      32,
      48,
      1
    ],
    "muted": [
      95,
      113,
      150,
      1
    ],
    "bgDeep": [
      232,
      234,
      238,
      1
    ],
    "bgCard": [
      255,
      255,
      255,
      0.92
    ]
  },
  "nord:dark": {
    "main": [
      236,
      239,
      244,
      0.9
    ],
    "muted": [
      216,
      222,
      233,
      0.5
    ],
    "bgDeep": [
      30,
      36,
      48,
      1
    ],
    "bgCard": [
      46,
      52,
      64,
      0.9
    ]
  },
  "nord:light": {
    "main": [
      46,
      52,
      64,
      0.9
    ],
    "muted": [
      98,
      104,
      118,
      1
    ],
    "bgDeep": [
      216,
      220,
      227,
      1
    ],
    "bgCard": [
      236,
      239,
      244,
      0.95
    ]
  },
  "catppuccin:dark": {
    "main": [
      205,
      214,
      244,
      0.9
    ],
    "muted": [
      166,
      173,
      200,
      0.55
    ],
    "bgDeep": [
      17,
      17,
      27,
      1
    ],
    "bgCard": [
      30,
      30,
      46,
      0.9
    ]
  },
  "catppuccin:light": {
    "main": [
      69,
      71,
      89,
      1
    ],
    "muted": [
      101,
      104,
      128,
      1
    ],
    "bgDeep": [
      218,
      222,
      230,
      1
    ],
    "bgCard": [
      239,
      241,
      245,
      0.95
    ]
  },
  "dracula:dark": {
    "main": [
      248,
      248,
      242,
      0.9
    ],
    "muted": [
      98,
      114,
      164,
      0.7
    ],
    "bgDeep": [
      28,
      30,
      38,
      1
    ],
    "bgCard": [
      40,
      42,
      54,
      0.9
    ]
  },
  "tokyo:dark": {
    "main": [
      192,
      202,
      245,
      0.9
    ],
    "muted": [
      86,
      95,
      137,
      0.7
    ],
    "bgDeep": [
      19,
      20,
      30,
      1
    ],
    "bgCard": [
      26,
      27,
      38,
      0.9
    ]
  },
  "gruvbox:dark": {
    "main": [
      235,
      219,
      178,
      0.9
    ],
    "muted": [
      168,
      153,
      132,
      0.55
    ],
    "bgDeep": [
      29,
      32,
      33,
      1
    ],
    "bgCard": [
      40,
      40,
      40,
      0.9
    ]
  },
  "gruvbox:light": {
    "main": [
      76,
      71,
      66,
      1
    ],
    "muted": [
      110,
      105,
      92,
      1
    ],
    "bgDeep": [
      234,
      221,
      181,
      1
    ],
    "bgCard": [
      251,
      241,
      199,
      0.95
    ]
  },
  "rosepine:dark": {
    "main": [
      224,
      222,
      244,
      0.9
    ],
    "muted": [
      110,
      106,
      134,
      0.6
    ],
    "bgDeep": [
      20,
      18,
      30,
      1
    ],
    "bgCard": [
      31,
      29,
      46,
      0.9
    ]
  },
  "rosepine:light": {
    "main": [
      75,
      71,
      97,
      1
    ],
    "muted": [
      109,
      106,
      118,
      1
    ],
    "bgDeep": [
      229,
      224,
      217,
      1
    ],
    "bgCard": [
      250,
      244,
      237,
      0.95
    ]
  },
  "rosepine-moon:dark": {
    "main": [
      224,
      222,
      244,
      0.9
    ],
    "muted": [
      110,
      106,
      134,
      0.6
    ],
    "bgDeep": [
      29,
      27,
      48,
      1
    ],
    "bgCard": [
      42,
      40,
      55,
      0.9
    ]
  },
  "onedark:dark": {
    "main": [
      171,
      178,
      191,
      0.9
    ],
    "muted": [
      171,
      178,
      191,
      0.5
    ],
    "bgDeep": [
      33,
      37,
      43,
      1
    ],
    "bgCard": [
      40,
      44,
      52,
      0.9
    ]
  },
  "onedark:light": {
    "main": [
      56,
      58,
      66,
      0.9
    ],
    "muted": [
      110,
      110,
      115,
      1
    ],
    "bgDeep": [
      229,
      230,
      231,
      1
    ],
    "bgCard": [
      250,
      250,
      250,
      0.95
    ]
  },
  "solarized:dark": {
    "main": [
      131,
      148,
      150,
      0.9
    ],
    "muted": [
      131,
      148,
      150,
      0.55
    ],
    "bgDeep": [
      0,
      43,
      54,
      1
    ],
    "bgCard": [
      7,
      54,
      66,
      0.9
    ]
  },
  "solarized:light": {
    "main": [
      66,
      77,
      81,
      1
    ],
    "muted": [
      92,
      112,
      119,
      1
    ],
    "bgDeep": [
      232,
      226,
      208,
      1
    ],
    "bgCard": [
      253,
      246,
      227,
      0.95
    ]
  },
  "everforest:dark": {
    "main": [
      211,
      198,
      170,
      0.9
    ],
    "muted": [
      211,
      198,
      170,
      0.5
    ],
    "bgDeep": [
      30,
      37,
      40,
      1
    ],
    "bgCard": [
      45,
      53,
      59,
      0.9
    ]
  },
  "kanagawa:dark": {
    "main": [
      220,
      215,
      186,
      0.9
    ],
    "muted": [
      114,
      113,
      105,
      0.6
    ],
    "bgDeep": [
      22,
      22,
      29,
      1
    ],
    "bgCard": [
      31,
      31,
      40,
      0.9
    ]
  },
  "monokai:dark": {
    "main": [
      248,
      248,
      242,
      0.9
    ],
    "muted": [
      117,
      113,
      94,
      0.65
    ],
    "bgDeep": [
      29,
      30,
      25,
      1
    ],
    "bgCard": [
      39,
      40,
      34,
      0.9
    ]
  },
  "monokai-pro:dark": {
    "main": [
      252,
      252,
      250,
      0.9
    ],
    "muted": [
      128,
      122,
      136,
      0.65
    ],
    "bgDeep": [
      30,
      28,
      32,
      1
    ],
    "bgCard": [
      45,
      42,
      46,
      0.9
    ]
  },
  "material:dark": {
    "main": [
      238,
      255,
      255,
      0.9
    ],
    "muted": [
      176,
      190,
      197,
      0.55
    ],
    "bgDeep": [
      27,
      37,
      40,
      1
    ],
    "bgCard": [
      38,
      50,
      56,
      0.9
    ]
  },
  "material:light": {
    "main": [
      33,
      33,
      33,
      0.9
    ],
    "muted": [
      111,
      111,
      111,
      1
    ],
    "bgDeep": [
      230,
      230,
      230,
      1
    ],
    "bgCard": [
      250,
      250,
      250,
      0.95
    ]
  },
  "palenight:dark": {
    "main": [
      191,
      199,
      213,
      0.9
    ],
    "muted": [
      191,
      199,
      213,
      0.5
    ],
    "bgDeep": [
      32,
      35,
      54,
      1
    ],
    "bgCard": [
      41,
      45,
      62,
      0.9
    ]
  },
  "github:dark": {
    "main": [
      201,
      209,
      217,
      0.9
    ],
    "muted": [
      139,
      148,
      158,
      0.6
    ],
    "bgDeep": [
      1,
      4,
      9,
      1
    ],
    "bgCard": [
      22,
      27,
      34,
      0.9
    ]
  },
  "github:light": {
    "main": [
      36,
      41,
      47,
      0.9
    ],
    "muted": [
      102,
      110,
      120,
      1
    ],
    "bgDeep": [
      224,
      229,
      233,
      1
    ],
    "bgCard": [
      246,
      248,
      250,
      0.95
    ]
  }
};
