import { useEffect, useState } from "react";
import { getToken, onUnauthorized, portal, type PortalConfig } from "./api";
import { navigate, useRoute } from "./router";
import { SignIn } from "./SignIn";
import { Register } from "./Register";
import { Claim } from "./Claim";
import { Home } from "./Home";
import { AccountPage } from "./AccountPage";
import { Shell } from "./Shell";

/**
 * The subscriber's app.
 *
 * Two states, not a route guard: either there is a session or there is not.
 * The auth screens are reachable without one, everything else is not, and a
 * 401 from anywhere drops back here — see onUnauthorized in ./api.
 */
export function PortalApp() {
  const route = useRoute();
  const [authed, setAuthed] = useState(!!getToken());
  const [config, setConfig] = useState<PortalConfig | null>(null);
  const [configError, setConfigError] = useState("");

  useEffect(() => {
    onUnauthorized(() => setAuthed(false));
    portal
      .config()
      .then(setConfig)
      .catch((e) => setConfigError((e as Error).message));
  }, []);

  if (configError) {
    return (
      <Shell>
        <p className="text-sm text-danger">{configError}</p>
      </Shell>
    );
  }
  if (!config) return null;

  // Claiming happens without a session, and must win over one: an operator
  // sending someone a reset link expects it to work in a browser that is
  // already signed in as them.
  if (route.view === "claim") {
    return <Claim token={route.token} onAuthed={() => setAuthed(true)} />;
  }

  if (!authed) {
    if (route.view === "register" && config.registration_open) {
      return <Register config={config} />;
    }
    return <SignIn config={config} onAuthed={() => setAuthed(true)} />;
  }

  const signOut = async () => {
    await portal.logout().catch(() => {});
    setAuthed(false);
    navigate({ view: "home" });
  };

  return (
    <Shell signedIn onSignOut={signOut}>
      {route.view === "account" ? (
        <AccountPage onSignOut={signOut} />
      ) : (
        <Home />
      )}
    </Shell>
  );
}
