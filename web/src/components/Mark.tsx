/**
 * Chiral mark — a circle divided by an S-curve into two handed halves, one
 * filled. Chirality is rotational, not mirror, asymmetry; the taijitu-style
 * split says that in one glyph. Inherits currentColor.
 */
export function Mark({ size = 22, className }: { size?: number; className?: string }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      className={className}
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="9.25" stroke="currentColor" strokeWidth="1.5" opacity="0.55" />
      <path
        d="M12 2.75 A9.25 9.25 0 0 1 12 21.25 A4.625 4.625 0 0 1 12 12 A4.625 4.625 0 0 0 12 2.75 Z"
        fill="currentColor"
      />
    </svg>
  );
}
