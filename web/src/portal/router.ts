import { useEffect, useState } from "react";

/**
 * The portal's routes. Hash-based, like the console's, so reloads and the back
 * button work without a server rewrite — and so the two builds can be plain
 * static files behind one origin.
 */
export type Route =
  | { view: "home" }
  | { view: "account" }
  | { view: "signin" }
  | { view: "register" }
  | { view: "claim"; token: string };

export function parseHash(hash: string): Route {
  const path = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  switch (path[0]) {
    case "account":
      return { view: "account" };
    case "signin":
      return { view: "signin" };
    case "register":
      return { view: "register" };
    case "claim":
      // The token is in the fragment, which never reaches the server and does
      // not appear in a Referer header. It is still in the address bar and in
      // history, which is why it is one-time and short-lived server-side.
      return { view: "claim", token: path[1] ?? "" };
    default:
      return { view: "home" };
  }
}

export function href(route: Route): string {
  switch (route.view) {
    case "account":
      return "#/account";
    case "signin":
      return "#/signin";
    case "register":
      return "#/register";
    case "claim":
      return `#/claim/${route.token}`;
    default:
      return "#/";
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
