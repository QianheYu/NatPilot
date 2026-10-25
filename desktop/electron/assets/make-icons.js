// Generates the tray icons for the Electron shell.
//
//   node desktop/electron/assets/make-icons.js
//
// Pure node (zlib only): no canvas, no image library, so the icons can be
// regenerated on any machine. Two variants are produced:
//
//   tray.png / tray@2x.png         colour icon (windows / linux)
//   trayTemplate.png               monochrome + alpha (macOS menu bar template)
//   trayTemplate@2x.png
//
// The glyph is a rounded square with three "port slots", which reads at 16px
// and matches the console's accent colour (#6aa8ff).

const fs = require('node:fs')
const path = require('node:path')
const zlib = require('node:zlib')

/** crc32 for PNG chunks. */
const CRC_TABLE = (() => {
  const table = new Int32Array(256)
  for (let n = 0; n < 256; n++) {
    let c = n
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    table[n] = c
  }
  return table
})()

function crc32(buf) {
  let c = 0xffffffff
  for (let i = 0; i < buf.length; i++) c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length, 0)
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(body), 0)
  return Buffer.concat([len, body, crc])
}

/** Encodes RGBA pixel data as a PNG (8 bit, colour type 6, no interlace). */
function encodePNG(width, height, rgba) {
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(width, 0)
  ihdr.writeUInt32BE(height, 4)
  ihdr[8] = 8 // bit depth
  ihdr[9] = 6 // colour type: RGBA
  ihdr[10] = 0
  ihdr[11] = 0
  ihdr[12] = 0
  const raw = Buffer.alloc((width * 4 + 1) * height)
  for (let y = 0; y < height; y++) {
    raw[y * (width * 4 + 1)] = 0 // filter: none
    rgba.copy(raw, y * (width * 4 + 1) + 1, y * width * 4, (y + 1) * width * 4)
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', zlib.deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

/**
 * Draws the glyph.
 * @param {number} size   canvas size in pixels
 * @param {boolean} mono  true -> black on transparent (macOS template image)
 */
function draw(size, mono) {
  const px = Buffer.alloc(size * size * 4)
  const s = size / 32 // scale factor relative to the 32px master
  const put = (x, y, r, g, b, a) => {
    if (x < 0 || y < 0 || x >= size || y >= size) return
    const i = (y * size + x) * 4
    const na = a / 255
    // straight alpha compositing over whatever is already there
    const da = px[i + 3] / 255
    const out = na + da * (1 - na)
    if (out === 0) return
    px[i] = Math.round((r * na + px[i] * da * (1 - na)) / out)
    px[i + 1] = Math.round((g * na + px[i + 1] * da * (1 - na)) / out)
    px[i + 2] = Math.round((b * na + px[i + 2] * da * (1 - na)) / out)
    px[i + 3] = Math.round(out * 255)
  }

  const main = mono ? [0, 0, 0] : [0x6a, 0xa8, 0xff]
  const accent = mono ? [0, 0, 0] : [0x0f, 0x11, 0x15]

  // rounded square body
  const radius = 7 * s
  const x0 = 2 * s
  const y0 = 2 * s
  const x1 = size - 2 * s
  const y1 = size - 2 * s
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const cx = Math.min(Math.max(x + 0.5, x0 + radius), x1 - radius)
      const cy = Math.min(Math.max(y + 0.5, y0 + radius), y1 - radius)
      const dx = x + 0.5 - cx
      const dy = y + 0.5 - cy
      const d = Math.sqrt(dx * dx + dy * dy)
      const inside = x + 0.5 >= x0 && x + 0.5 <= x1 && y + 0.5 >= y0 && y + 0.5 <= y1 && d <= radius
      if (!inside) continue
      // 1px soft edge keeps 16px icons from looking ragged
      const edge = Math.min(1, Math.max(0, radius - d + 0.5))
      put(x, y, main[0], main[1], main[2], Math.round(255 * (mono ? 1 : edge)))
    }
  }

  // three port slots (the "mapping" motif)
  const slots = [
    [7, 11],
    [7, 16],
    [7, 21],
  ]
  for (const [sx, sy] of slots) {
    const left = Math.round(sx * s)
    const top = Math.round((sy - 1.1) * s)
    const w = Math.round(11 * s)
    const h = Math.max(1, Math.round(2.2 * s))
    const knob = Math.round(3.2 * s)
    for (let y = top; y < top + h; y++) {
      for (let x = left; x < left + w; x++) {
        put(x, y, accent[0], accent[1], accent[2], mono ? 255 : 235)
      }
    }
    for (let y = top + Math.round(h / 2) - Math.round(knob / 2); y < top + Math.round(h / 2) + Math.round(knob / 2); y++) {
      for (let x = left + w - knob; x < left + w; x++) {
        put(x, y, accent[0], accent[1], accent[2], mono ? 255 : 235)
      }
    }
  }
  return px
}

const outDir = __dirname
const files = [
  ['tray.png', 32, false],
  ['tray@2x.png', 64, false],
  ['trayTemplate.png', 16, true],
  ['trayTemplate@2x.png', 32, true],
]
for (const [name, size, mono] of files) {
  const png = encodePNG(size, size, draw(size, mono))
  fs.writeFileSync(path.join(outDir, name), png)
  console.log(`wrote ${name} (${size}x${size}, ${png.length} bytes)`)
}

// Application icon for electron-builder (it derives the .ico/.icns from this).
// electron-builder resolves `buildResources` relative to package.json, i.e.
// desktop/build/icon.png.
const buildDir = path.join(__dirname, '..', '..', 'build')
fs.mkdirSync(buildDir, { recursive: true })
const appIcon = encodePNG(512, 512, draw(512, false))
fs.writeFileSync(path.join(buildDir, 'icon.png'), appIcon)
console.log(`wrote build/icon.png (512x512, ${appIcon.length} bytes)`)
