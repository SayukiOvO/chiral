/**
 * Copying to the clipboard, including where navigator.clipboard is not there.
 *
 * The modern API is gated on a secure context. A panel reached over plain http
 * on a LAN address — which is how plenty of these get run — has
 * navigator.clipboard undefined, and the copy button silently does nothing.
 * The console has had this latent bug all along and nobody noticed, because
 * localhost counts as secure. The portal is reached from phones, so it cannot
 * rely on that.
 *
 * The fallback also selects the text, so if even execCommand is refused the
 * reader can copy by hand rather than being left with a button that lies.
 */
export async function copyText(value: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Fall through: permission denied, or a browser that exposes the API
      // without honouring it.
    }
  }
  return legacyCopy(value);
}

function legacyCopy(value: string): boolean {
  const area = document.createElement("textarea");
  area.value = value;
  // Off-screen rather than hidden: a display:none element cannot be selected,
  // and scrolling to it would jump the page.
  area.style.position = "fixed";
  area.style.top = "-1000px";
  area.setAttribute("readonly", "");
  document.body.appendChild(area);
  try {
    area.select();
    area.setSelectionRange(0, value.length);
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    document.body.removeChild(area);
  }
}
