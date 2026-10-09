// Display formatting helpers — pure, unit-testable.

/** 6500 → "0:06"; 75_000 → "1:15". */
export function mmss(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const m = Math.floor(total / 60)
  const s = total % 60
  return `${m}:${String(s).padStart(2, '0')}`
}

/** "just now", "45s ago", "3m ago", "2h ago". */
export function timeAgo(epochMs: number, now: number): string {
  const delta = Math.max(0, Math.floor((now - epochMs) / 1000))
  if (delta < 10) return 'just now'
  if (delta < 60) return `${delta}s ago`
  if (delta < 3600) return `${Math.floor(delta / 60)}m ago`
  if (delta < 86400) return `${Math.floor(delta / 3600)}h ago`
  return `${Math.floor(delta / 86400)}d ago`
}

/** 47.6205, -122.3493 → "47.6205 N, 122.3493 W". */
export function formatCoords(lat: number, lon: number): string {
  const ns = lat >= 0 ? 'N' : 'S'
  const ew = lon >= 0 ? 'E' : 'W'
  return `${Math.abs(lat).toFixed(4)} ${ns}, ${Math.abs(lon).toFixed(4)} ${ew}`
}

/** OpenStreetMap link for a coordinate (demo convenience). */
export function mapUrl(lat: number, lon: number): string {
  return `https://www.openstreetmap.org/?mlat=${lat}&mlon=${lon}#map=16/${lat}/${lon}`
}

export function clockTime(epochMs: number): string {
  return new Date(epochMs).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}
