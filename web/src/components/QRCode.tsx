import { useMemo } from "react";
import qrcode from "qrcode-generator";

/**
 * A QR code as one SVG path.
 *
 * Encoding is delegated: Reed-Solomon over GF(256), mask selection and version
 * choice are the kind of thing that fails silently — a slightly wrong code
 * still *looks* like a QR code, and the only way to notice is that nobody's
 * phone can read it. Drawing we do ourselves, to get a crisp vector at any
 * size instead of a scaled-up PNG.
 *
 * Black on white regardless of theme. Plenty of scanners cope with an
 * inverted code, but not all of them, and this one has exactly one job.
 */
export function QRCode({ value, size = 176 }: { value: string; size?: number }) {
  const { path, extent } = useMemo(() => {
    // Type 0 = pick the smallest version that fits. Level M is the usual
    // choice for otpauth:// URIs: enough redundancy for a phone camera
    // without inflating the module count.
    const qr = qrcode(0, "M");
    qr.addData(value);
    qr.make();
    const count = qr.getModuleCount();
    const margin = 2; // the quiet zone, in modules

    let d = "";
    for (let row = 0; row < count; row++) {
      for (let col = 0; col < count; col++) {
        if (qr.isDark(row, col)) d += `M${col + margin} ${row + margin}h1v1h-1z`;
      }
    }
    return { path: d, extent: count + margin * 2 };
  }, [value]);

  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${extent} ${extent}`}
      shapeRendering="crispEdges"
      role="img"
      aria-label="QR code"
      className="rounded-lg bg-white p-0"
    >
      <rect width={extent} height={extent} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
