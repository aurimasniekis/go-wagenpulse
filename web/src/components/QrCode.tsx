import { useMemo } from 'react'
import { encode } from 'uqr'

/**
 * A QR code drawn as SVG from the module matrix (no HTML injection). Always dark on white with a quiet zone so
 * authenticator apps scan it in either theme.
 */
export function QrCode({ value, size = 184, label }: { value: string; size?: number; label: string }) {
  const { path, n } = useMemo(() => {
    const qr = encode(value, { ecc: 'M', border: 2 })
    let d = ''
    qr.data.forEach((row, y) => {
      row.forEach((on, x) => {
        if (on) d += `M${String(x)} ${String(y)}h1v1h-1z`
      })
    })
    return { path: d, n: qr.size }
  }, [value])
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${String(n)} ${String(n)}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
      style={{ borderRadius: 4 }}
    >
      <rect width={n} height={n} fill="#ffffff" />
      <path d={path} fill="#0f2233" />
    </svg>
  )
}
