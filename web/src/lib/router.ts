import { useEffect, useState } from "react";

/**
 * A hash router, sized to this app: a handful of top-level views plus an
 * optional selected id. Hash routing keeps reloads and back/forward working
 * without a server-side rewrite rule.
 */
export type Route =
  | { view: "nodes" }
  | { view: "profiles"; id?: string }
  | { view: "users"; id?: string }
  | { view: "variables" }
  | { view: "admins" }
  | { view: "audit" }
  | { view: "alerts" }
  | { view: "kernel" }
  | { view: "rules" }
  | { view: "externals" }
  | { view: "settings" }
  | { view: "security" };

export function parseHash(hash: string): Route {
  const path = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  switch (path[0]) {
    case "profiles":
      return { view: "profiles", id: path[1] };
    case "users":
      return { view: "users", id: path[1] };
    case "variables":
      return { view: "variables" };
    case "admins":
      return { view: "admins" };
    case "audit":
      return { view: "audit" };
    case "alerts":
      return { view: "alerts" };
    case "kernel":
      return { view: "kernel" };
    case "rules":
      return { view: "rules" };
    case "externals":
      return { view: "externals" };
    case "settings":
      return { view: "settings" };
    case "security":
      return { view: "security" };
    default:
      return { view: "nodes" };
  }
}

export function href(route: Route): string {
  switch (route.view) {
    case "profiles":
      return route.id ? `#/profiles/${route.id}` : "#/profiles";
    case "users":
      return route.id ? `#/users/${route.id}` : "#/users";
    case "variables":
      return "#/variables";
    case "admins":
      return "#/admins";
    case "audit":
      return "#/audit";
    case "alerts":
      return "#/alerts";
    case "kernel":
      return "#/kernel";
    case "rules":
      return "#/rules";
    case "externals":
      return "#/externals";
    case "settings":
      return "#/settings";
    case "security":
      return "#/security";
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
