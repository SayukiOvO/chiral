import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Self-hosted fonts, same reasoning as the console's entry: no runtime CDN
// dependency on networks where fonts.googleapis.com is slow or blocked — which
// is most of the networks this panel's users are on.
import "@fontsource-variable/inter";
import "@fontsource-variable/space-grotesk";
import "@fontsource/space-mono/400.css";
import "@fontsource/space-mono/700.css";

import { PortalApp } from "./portal/PortalApp.tsx";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PortalApp />
  </StrictMode>,
);
