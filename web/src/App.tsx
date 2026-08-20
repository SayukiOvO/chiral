import { useState } from "react";
import { api, getToken, setToken } from "./api";
import { SideNav } from "./components/SideNav";
import { Utilities } from "./components/Utilities";
import { LoginPage } from "./components/LoginPage";
import { NodesPage } from "./pages/NodesPage";
import { ProfilesPage } from "./pages/ProfilesPage";
import { AdminsPage } from "./pages/AdminsPage";
import { AlertsPage } from "./pages/AlertsPage";
import { KernelPage } from "./pages/KernelPage";
import { RulesPage } from "./pages/RulesPage";
import { ExternalsPage } from "./pages/ExternalsPage";
import { SettingsPage } from "./pages/SettingsPage";
import { AuditPage } from "./pages/AuditPage";
import { SecurityPage } from "./pages/SecurityPage";
import { UsersPage } from "./pages/UsersPage";
import { GroupsPage } from "./pages/GroupsPage";
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
  // The nav is a column beside the page on a desktop and a drawer over it on a
  // phone, so the shell is a flex row that collapses to a single column.
  return (
    <div className="flex min-h-screen flex-col lg:flex-row">
      <SideNav route={route} onSignOut={onSignOut} />
      <main className="min-w-0 flex-1 px-5 pb-7 pt-6 sm:px-8 sm:pb-9 lg:pt-0">
        {/* Above the page's own heading, so the heading keeps the left edge to
            itself and every page's primary action keeps the right. */}
        <Utilities />
        {route.view === "nodes" && <NodesPage onSignOut={onSignOut} />}
        {route.view === "profiles" && <ProfilesPage id={route.id} />}
        {route.view === "users" && <UsersPage />}
        {route.view === "groups" && <GroupsPage />}
        {route.view === "variables" && <VariablesPage />}
        {route.view === "alerts" && <AlertsPage />}
        {route.view === "kernel" && <KernelPage />}
        {route.view === "rules" && <RulesPage />}
        {route.view === "externals" && <ExternalsPage />}
        {route.view === "settings" && <SettingsPage />}
        {route.view === "admins" && <AdminsPage />}
        {route.view === "audit" && <AuditPage />}
        {route.view === "security" && <SecurityPage />}
      </main>
    </div>
  );
}
