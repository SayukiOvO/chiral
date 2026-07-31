import { useState } from "react";
import { api, getToken, setToken } from "./api";
import { TopBar } from "./components/TopBar";
import { LoginPage } from "./components/LoginPage";
import { NodesPage } from "./pages/NodesPage";
import { ProfilesPage } from "./pages/ProfilesPage";
import { AdminsPage } from "./pages/AdminsPage";
import { AlertsPage } from "./pages/AlertsPage";
import { KernelPage } from "./pages/KernelPage";
import { RulesPage } from "./pages/RulesPage";
import { AuditPage } from "./pages/AuditPage";
import { SecurityPage } from "./pages/SecurityPage";
import { UsersPage } from "./pages/UsersPage";
import { VariablesPage } from "./pages/VariablesPage";
import { useRoute } from "./lib/router";

export function App() {
  const [authed, setAuthed] = useState(!!getToken());

  // Revoke the session server-side before dropping it locally: a token left
  // valid until it expires is a token someone else can still use.
  async function signOut() {
    await api.logout().catch(() => {});
    setToken("");
    setAuthed(false);
  }

  if (!authed) return <LoginPage onAuthed={() => setAuthed(true)} />;
  return <Console onSignOut={signOut} />;
}

function Console({ onSignOut }: { onSignOut: () => void }) {
  const route = useRoute();
  return (
    <div className="min-h-screen">
      <TopBar route={route} onSignOut={onSignOut} />
      <main className="mx-auto max-w-[1120px] px-5 py-8 sm:px-8 sm:py-10">
        {route.view === "nodes" && <NodesPage onSignOut={onSignOut} />}
        {route.view === "profiles" && <ProfilesPage id={route.id} />}
        {route.view === "users" && <UsersPage />}
        {route.view === "variables" && <VariablesPage />}
        {route.view === "alerts" && <AlertsPage />}
        {route.view === "kernel" && <KernelPage />}
        {route.view === "rules" && <RulesPage />}
        {route.view === "admins" && <AdminsPage />}
        {route.view === "audit" && <AuditPage />}
        {route.view === "security" && <SecurityPage />}
      </main>
    </div>
  );
}
