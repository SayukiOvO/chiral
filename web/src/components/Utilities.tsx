import { ThemeToggle } from "./ThemeToggle";
import { LangToggle } from "./LangToggle";

/**
 * Theme and language, pinned to the top-right of the page.
 *
 * They were in the account menu, which is where settings about a person belong
 * — except these two are not about the person, they are about the page in front
 * of them. Reading them costs nothing and changing them is a glance-and-click,
 * so they sit where they can be seen instead of behind a menu.
 *
 * Sticky rather than in the flow: a preference you reach for while looking at
 * something is one you want without scrolling back up.
 *
 * It spans the content column on the page's own background rather than
 * floating as a small plate. A plate is prettier standing still and wrong in
 * motion: every page puts its primary action in this corner, and that button
 * scrolled up under the plate and came out the other side as a white sliver.
 * A strip the width of the column has nothing to peek out from behind. It
 * fades to transparent at its lower edge rather than ending on a line: content
 * has to emerge from under a sticky element sooner or later, and emerging from
 * a fade looks like scrolling where emerging from a hard cut looks like
 * clipping. No border and no shadow either, so it reads as the top edge of the
 * page rather than as a second bar under the one the phone already has.
 *
 * Desktop only. A phone already has a bar pinned across the top and its right
 * half is empty, so there they go in it — see SideNav — rather than as a plate
 * floating over the content of a screen that has none to spare.
 */
export function Utilities() {
  return (
    <div className="sticky top-0 z-20 -mx-5 hidden justify-end gap-1 bg-gradient-to-b from-paper via-paper to-transparent px-5 pb-6 pt-3 sm:-mx-8 sm:px-8 lg:flex">
      <LangToggle />
      <ThemeToggle />
    </div>
  );
}
