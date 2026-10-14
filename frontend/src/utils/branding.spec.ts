import { describe, expect, it } from 'vitest'
import { brandPalette, validateBranding } from './branding'

describe('runtime branding', () => {
  it('chooses readable controls and distinct text variants for both themes', () => {
    for (const accent of ['#00AEEF', '#ffffff', '#000000', '#ff0000', '#073756']) {
      const palette = brandPalette(accent)
      expect(palette.controlContrast).toBeGreaterThanOrEqual(4.5)
      expect(palette.lightTextContrast).toBeGreaterThanOrEqual(4.5)
      expect(palette.darkTextContrast).toBeGreaterThanOrEqual(4.5)
    }
  })
  it('accepts plain display text and a hex colour without treating names as markup', () => {
    expect(validateBranding('Acme <Team>', '#12aB34')).toBe(true)
    expect(validateBranding('', '#123456')).toBe(false)
    expect(validateBranding('x'.repeat(81), '#123456')).toBe(false)
    expect(validateBranding('Acme', 'red;url(x)')).toBe(false)
  })
})
