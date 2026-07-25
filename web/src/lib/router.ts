import { useEffect, useState } from "react";

/**
 * A hash router, sized to this app: three top-level views plus an optional
 * selected id. Hash routing keeps reloads and back/forward working without a
 * server-side rewrite rule.
 */
export type Route =
  | { view: "nodes" }
  | { view: "profiles"; id?: string }
  | { view: "variables" };

export function parseHash(hash: string): Route {
  const path = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  switch (path[0]) {
    case "profiles":
      return { view: "profiles", id: path[1] };
    case "variables":
      return { view: "variables" };
    default:
      return { view: "nodes" };
  }
}

export function href(route: Route): string {
  switch (route.view) {
    case "profiles":
      return route.id ? `#/profiles/${route.id}` : "#/profiles";
    case "variables":
      return "#/variables";
    default:
      return "#/nodes";
  }
}

export function navigate(route: Route) {
  window.location.hash = href(route);
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseHash(window.location.hash));
  useEffect(() => {
    const onChange = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return route;
}
