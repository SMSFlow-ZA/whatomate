type RGB = [number, number, number]
const rgb = (hex: string): RGB => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16)) as RGB
function luminance(c: RGB) {
  const linear = c.map(v => v / 255 <= .04045 ? v / 255 / 12.92 : ((v / 255 + .055) / 1.055) ** 2.4)
  return linear[0]! * .2126 + linear[1]! * .7152 + linear[2]! * .0722
}
function contrast(a: RGB, b: RGB) { const x = luminance(a), y = luminance(b); return (Math.max(x, y) + .05) / (Math.min(x, y) + .05) }
const hex = (c: RGB) => '#' + c.map(v => Math.round(v).toString(16).padStart(2, '0')).join('')
const blend = (c: RGB, target: number, amount: number): RGB => c.map(v => Math.round(v + (target - v) * amount)) as RGB
function readable(c: RGB, background: RGB, target: number): RGB {
  let result = c
  for (let i = 0; i < 40 && contrast(result, background) < 4.5; i++) result = blend(result, target, .1)
  return result
}
export function hsl(hexValue: string) {
  const [r, g, b] = rgb(hexValue).map(v => v / 255) as RGB
  const max = Math.max(r, g, b), min = Math.min(r, g, b), d = max - min, l = (max + min) / 2
  let hue = 0, s = 0
  if (d) { s = d / (1 - Math.abs(2 * l - 1)); hue = (max === r ? ((g - b) / d + (g < b ? 6 : 0)) : max === g ? (b - r) / d + 2 : (r - g) / d + 4) * 60 }
  return hue.toFixed(2) + ' ' + (s * 100).toFixed(2) + '% ' + (l * 100).toFixed(2) + '%'
}
export function validateBranding(name: string, accent: string) {
  const count = Array.from(name.trim()).length
  return count >= 1 && count <= 80 && !/[\u0000-\u001f\u007f]/.test(name) && /^#[0-9a-fA-F]{6}$/.test(accent)
}
export function brandPalette(accent: string) {
  const c = rgb(/^#[0-9a-fA-F]{6}$/.test(accent) ? accent : '#25D366')
  const white: RGB = [255, 255, 255], black: RGB = [0, 0, 0]
  const foreground = contrast(c, white) >= contrast(c, black) ? white : black
  const lightBackground = rgb('#f0f3f6'), darkBackground = rgb('#073756')
  const light = readable(c, lightBackground, 0), dark = readable(c, darkBackground, 255)
  return {
    accent: hex(c), foreground: hex(foreground), hover: hex(blend(c, foreground === black ? 255 : 0, .08)),
    lightText: hex(light), darkText: hex(dark),
    controlContrast: contrast(c, foreground), lightTextContrast: contrast(light, lightBackground), darkTextContrast: contrast(dark, darkBackground),
  }
}
