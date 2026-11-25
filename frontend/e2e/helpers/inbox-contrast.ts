import type { Page } from '@playwright/test'

// Measure rendered feature text against composited ancestor backgrounds.
// Disabled controls and hidden/inert content are excluded from text contrast.
export async function inboxContrast(
  page: Page,
  includeAlerts = false,
  includePlaceholders = false,
) {
  return page.evaluate(
    ({ includeAlerts, includePlaceholders }) => {
      type Colour = [number, number, number, number]
      function colour(raw: string): Colour {
        const values = (raw.match(/[\d.]+/g) ?? []).map(Number)
        const multiplier = raw.startsWith('color(') ? 255 : 1
        return [
          (values[0] ?? 0) * multiplier,
          (values[1] ?? 0) * multiplier,
          (values[2] ?? 0) * multiplier,
          values[3] ?? 1,
        ]
      }
      function over(front: Colour, back: Colour): Colour {
        const alpha = front[3] + back[3] * (1 - front[3])
        if (!alpha) return [0, 0, 0, 0]
        return [0, 1, 2]
          .map(
            (i) =>
              (front[i]! * front[3] + back[i]! * back[3] * (1 - front[3])) /
              alpha,
          )
          .concat(alpha) as Colour
      }
      function luminance(rgb: Colour) {
        return rgb
          .slice(0, 3)
          .map((value) => {
            const channel = value / 255
            return channel <= 0.04045
              ? channel / 12.92
              : ((channel + 0.055) / 1.055) ** 2.4
          })
          .reduce(
            (sum, value, index) =>
              sum + value * [0.2126, 0.7152, 0.0722][index]!,
            0,
          )
      }
      return Array.from(
        document.querySelectorAll<HTMLElement>(
          '.inbox-ui .inbox-caption,.inbox-ui button,.inbox-ui select,.inbox-ui .text-muted-foreground' +
            (includeAlerts ? ',.inbox-ui [role=alert]' : '') +
            (includePlaceholders ? ',.inbox-ui input[placeholder]' : ''),
        ),
      )
        .filter(
          (element) =>
            element.getClientRects().length > 0 &&
            !element.closest('[inert],[aria-hidden="true"]') &&
            !element.closest('details:not([open])') &&
            !element.matches(':disabled') &&
            (!!element.textContent?.trim() ||
              (includePlaceholders && !!element.getAttribute('placeholder'))),
        )
        .slice(0, 150)
        .map((element) => {
          const placeholder =
            includePlaceholders && element.matches('input[placeholder]')
          const style = getComputedStyle(
              element,
              placeholder ? '::placeholder' : null,
            ),
            chain: HTMLElement[] = []
          for (
            let parent: HTMLElement | null = element;
            parent;
            parent = parent.parentElement
          )
            chain.push(parent)
          let background: Colour = [255, 255, 255, 1]
          for (const parent of chain.reverse())
            background = over(
              colour(getComputedStyle(parent).backgroundColor),
              background,
            )
          const foreground = over(colour(style.color), background),
            a = luminance(foreground),
            b = luminance(background),
            ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
          const size = parseFloat(style.fontSize),
            large =
              size >= 24 || (size >= 18.66 && parseInt(style.fontWeight) >= 700)
          return {
            text:
              (placeholder
                ? 'Placeholder: ' + element.getAttribute('placeholder')
                : '') ||
              element.getAttribute('aria-label') ||
              element.textContent!.trim().slice(0, 80),
            ratio,
            minimum: large ? 3 : 4.5,
          }
        })
    },
    { includeAlerts, includePlaceholders },
  )
}
