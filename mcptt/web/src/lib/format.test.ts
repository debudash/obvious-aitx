import { describe, expect, it } from 'vitest'
import { formatCoords, mapUrl, mmss, timeAgo } from './format'

describe('mmss', () => {
  it('formats sub-minute and multi-minute bursts', () => {
    expect(mmss(0)).toBe('0:00')
    expect(mmss(6500)).toBe('0:06')
    expect(mmss(75_000)).toBe('1:15')
  })
  it('never shows negative time', () => {
    expect(mmss(-5)).toBe('0:00')
  })
})

describe('timeAgo', () => {
  const now = 1_000_000_000_000
  it('buckets seconds, minutes, hours, days', () => {
    expect(timeAgo(now - 5_000, now)).toBe('just now')
    expect(timeAgo(now - 45_000, now)).toBe('45s ago')
    expect(timeAgo(now - 3 * 60_000, now)).toBe('3m ago')
    expect(timeAgo(now - 2 * 3_600_000, now)).toBe('2h ago')
    expect(timeAgo(now - 3 * 86_400_000, now)).toBe('3d ago')
  })
})

describe('formatCoords / mapUrl', () => {
  it('formats hemispheres correctly', () => {
    expect(formatCoords(47.6205, -122.3493)).toBe('47.6205 N, 122.3493 W')
    expect(formatCoords(-33.86, 151.2)).toBe('33.8600 S, 151.2000 E')
  })
  it('builds an OSM permalink', () => {
    expect(mapUrl(47.62, -122.34)).toContain('mlat=47.62')
    expect(mapUrl(47.62, -122.34)).toContain('-122.34')
  })
})
