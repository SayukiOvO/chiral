import { useState } from "react";
import { getToken } from "./api";
import { TopBar } from "./components/TopBar";
import { TokenGate } from "./components/TokenGate";
import { NodesPage } from "./pages/NodesPage";
import { ProfilesPage } from "./pages/ProfilesPage";
import { UsersPage } from "./pages/UsersPage";
import { VariablesPage } from "./pages/VariablesPage";
import { useRoute } from "./lib/router";

export function App() {
  const [authed, setAuthed] = useState(!!getToken());
  if (!authed) return <TokenGate onAuthed={() => setAuthed(true)} />;
  return <Console onSignOut={() => setAuthed(false)} />;
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
      </main>
    </div>
  );
}
